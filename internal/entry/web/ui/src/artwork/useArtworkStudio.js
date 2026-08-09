import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import {
  applyArtworkAsset,
  confirmArtworkStalePrompt,
  createArtworkDraft,
  deleteArtworkAsset,
  deleteArtworkDraft,
  downloadArtworkAsset,
  generateArtworkImage,
  generateArtworkPrompt,
  getArtworkDraft,
  listArtworkAssets,
  listArtworkDrafts,
  loadArtworkGatewayConfig,
  loadArtworkRegistry,
  loadArtworkScopeCatalog,
  loadArtworkWorkspace,
  newArtworkIdempotencyKey,
  reuseArtworkAsset,
  saveArtworkGatewayConfig,
  unapplyArtworkAsset,
  updateArtworkDraft,
  verifyArtworkGatewayConfig
} from './artwork-api.js';
import {
  ARTWORK_TYPES,
  ARTWORK_VIEWS,
  artworkViewForType,
  findArtworkModel,
  normalizeArtworkModelSize,
  normalizeArtworkScope,
  parsePromptModelOption,
  promptModelOptions,
  selectedPromptModel,
  workTypesForView
} from './artwork-options.js';
import {
  artworkErrorCode,
  artworkErrorMessage,
  artworkReducer,
  createArtworkState,
  hasActiveArtworkJobs,
  newTerminalArtworkJobs
} from './artwork-state.js';
import { readArtworkLocation, writeArtworkLocation } from './artwork-url.js';
import { createSerializedAutosave } from './autosave-controller.js';

const IDLE_AUTOSAVE = Object.freeze({ revision: 0, savedRevision: 0, dirty: false, saving: false, failed: false, error: null });

export function useArtworkStudio({ projectId, modelConfig, runtime, onPromptModelChange, onDirtyChange }) {
  const [state, dispatch] = useReducer(artworkReducer, undefined, createArtworkState);
  const initialLocation = readArtworkLocation(globalThis.location?.search || '');
  const [view, setViewState] = useState(initialLocation.view || ARTWORK_VIEWS.STORY);
  const [selectedDraftId, setSelectedDraftId] = useState('');
  const [editor, setEditor] = useState(null);
  const [editorEpoch, setEditorEpoch] = useState(0);
  const [autosave, setAutosave] = useState(IDLE_AUTOSAVE);
  const [newDraftTouched, setNewDraftTouched] = useState(false);
  const [operation, setOperation] = useState('');
  const stateRef = useRef(state);
  const editorRef = useRef(editor);
  const requestSequenceRef = useRef(0);
  const autosaveRef = useRef(null);
  const serverVersionRef = useRef(0);

  useEffect(() => { stateRef.current = state; }, [state]);
  useEffect(() => { editorRef.current = editor; }, [editor]);

  const applyDraft = useCallback((draft, nextView = artworkViewForType(draft?.work_type)) => {
    if (!draft?.id) return;
    serverVersionRef.current = Number(draft.version || 0);
    setViewState(nextView || ARTWORK_VIEWS.STORY);
    setSelectedDraftId(draft.id);
    setEditor({ ...draft });
    setEditorEpoch((value) => value + 1);
    setAutosave(IDLE_AUTOSAVE);
    setNewDraftTouched(false);
    writeArtworkLocation({ projectId, view: nextView, draftId: draft.id });
    dispatch({ type: 'draft-upsert', draft });
  }, [projectId]);

  const showNewDraft = useCallback((nextView, registry = stateRef.current.registry, gateway = stateRef.current.gateway, catalog = stateRef.current.catalog) => {
    const draft = newDraftForView(nextView, registry, gateway, catalog);
    setViewState(nextView);
    setSelectedDraftId('');
    setEditor(draft);
    setEditorEpoch((value) => value + 1);
    setAutosave(IDLE_AUTOSAVE);
    setNewDraftTouched(false);
    writeArtworkLocation({ projectId, view: nextView, draftId: '' });
  }, [projectId]);

  useEffect(() => {
    if (!projectId) return undefined;
    const sequence = requestSequenceRef.current + 1;
    requestSequenceRef.current = sequence;
    const controller = new AbortController();
    dispatch({ type: 'loading' });
    void Promise.all([
      loadArtworkRegistry({ signal: controller.signal }),
      loadArtworkGatewayConfig({ signal: controller.signal }),
      loadArtworkWorkspace(projectId, { signal: controller.signal }),
      loadArtworkScopeCatalog(projectId, { signal: controller.signal })
    ]).then(async ([registry, gateway, workspace, catalog]) => {
      if (controller.signal.aborted || requestSequenceRef.current !== sequence) return;
      dispatch({ type: 'loaded', registry, gateway, workspace, catalog });
      const location = readArtworkLocation(globalThis.location?.search || '');
      const requestedView = location.projectId === projectId && location.view ? location.view : ARTWORK_VIEWS.STORY;
      const candidates = (workspace.drafts?.items || []).filter((draft) => artworkViewForType(draft.work_type) === requestedView);
      const requested = candidates.find((draft) => draft.id === location.draftId) || candidates[0];
      if (!requested) {
        showNewDraft(requestedView, registry, gateway?.config || gateway, catalog);
        return;
      }
      try {
        const detail = await getArtworkDraft(projectId, requested.id, { signal: controller.signal });
        if (!controller.signal.aborted && requestSequenceRef.current === sequence) applyDraft(detail.draft, requestedView);
      } catch (error) {
        if (error?.name !== 'AbortError') {
          dispatch({ type: 'error', code: artworkErrorCode(error) });
          applyDraft(requested, requestedView);
        }
      }
    }).catch((error) => {
      if (error?.name !== 'AbortError' && requestSequenceRef.current === sequence) {
        dispatch({ type: 'error', code: artworkErrorCode(error) });
      }
    });
    return () => controller.abort();
  }, [applyDraft, projectId, showNewDraft]);

  useEffect(() => {
    autosaveRef.current?.destroy();
    autosaveRef.current = null;
    if (!projectId || !selectedDraftId || !editor?.id) return undefined;
    const controller = createSerializedAutosave({
      save: async (value) => updateArtworkDraft(projectId, selectedDraftId, {
        expected_version: serverVersionRef.current,
        ...draftMutationFields(value)
      }),
      onChange: setAutosave,
      onSaved: (response, meta) => {
        const savedDraft = response?.draft;
        if (!savedDraft) return;
        serverVersionRef.current = Number(savedDraft.version || serverVersionRef.current);
        dispatch({ type: 'draft-upsert', draft: savedDraft });
        setEditor((current) => meta.current ? { ...savedDraft } : { ...current, version: savedDraft.version });
      },
      onError: (error) => dispatch({ type: 'error', code: artworkErrorCode(error) })
    });
    autosaveRef.current = controller;
    return () => controller.destroy();
  }, [editor?.id, editorEpoch, projectId, selectedDraftId]);

  useEffect(() => {
    const dirty = selectedDraftId
      ? autosave.dirty || autosave.saving || autosave.failed
      : newDraftTouched;
    onDirtyChange?.(dirty);
    if (!dirty) return undefined;
    const warn = (event) => { event.preventDefault(); event.returnValue = ''; };
    globalThis.addEventListener?.('beforeunload', warn);
    return () => globalThis.removeEventListener?.('beforeunload', warn);
  }, [autosave.dirty, autosave.failed, autosave.saving, newDraftTouched, onDirtyChange, selectedDraftId]);

  const refreshWorkspace = useCallback(async () => {
    if (!projectId) return;
    const previous = stateRef.current;
    try {
      let workspace = await loadArtworkWorkspace(projectId);
      const terminals = newTerminalArtworkJobs(previous, workspace);
      if (terminals.length > 0) {
        const [refreshed, detail] = await Promise.all([
          loadArtworkWorkspace(projectId),
          selectedDraftId ? getArtworkDraft(projectId, selectedDraftId).catch(() => null) : Promise.resolve(null)
        ]);
        workspace = refreshed;
        const saveState = autosaveRef.current?.getState();
        if (detail?.draft && !saveState?.dirty && !saveState?.saving && !saveState?.failed) {
          applyDraft(detail.draft, artworkViewForType(detail.draft.work_type));
        }
      }
      dispatch({ type: 'workspace', workspace });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    }
  }, [applyDraft, projectId, selectedDraftId]);

  useEffect(() => {
    if (!hasActiveArtworkJobs(state)) return undefined;
    const timer = globalThis.setInterval(() => { void refreshWorkspace(); }, 3000);
    return () => globalThis.clearInterval(timer);
  }, [refreshWorkspace, state.jobs, state.promptJobs]);

  const flushAutosave = useCallback(async () => {
    if (autosaveRef.current) await autosaveRef.current.flush();
  }, []);

  const updateEditor = useCallback((patch) => {
    setEditor((current) => {
      if (!current) return current;
      let next = { ...current, ...patch };
      if (patch.work_type && patch.work_type !== current.work_type) next.size = '';
      if (patch.work_type) {
        const normalizedScope = normalizeArtworkScope(next.work_type, next.scope, next.scope_id, stateRef.current.catalog);
        next = { ...next, scope: normalizedScope.scope, scope_id: normalizedScope.scopeId };
      }
      if (patch.scope) {
        const normalizedScope = normalizeArtworkScope(next.work_type, patch.scope, next.scope_id, stateRef.current.catalog);
        next = { ...next, scope: normalizedScope.scope, scope_id: normalizedScope.scopeId };
      }
      if (patch.model_id || patch.work_type) {
        const normalized = normalizeArtworkModelSize(
          stateRef.current.registry,
          next.model_id,
          next.size,
          next.work_type,
          stateRef.current.gateway?.default_model
        );
        next = { ...next, model_id: normalized.modelId, size: normalized.size };
      }
      editorRef.current = next;
      if (selectedDraftId) autosaveRef.current?.edit(next);
      else setNewDraftTouched(true);
      return next;
    });
  }, [selectedDraftId]);

  const selectDraft = useCallback(async (draftId) => {
    if (!draftId || draftId === selectedDraftId) return true;
    try {
      await flushAutosave();
      setOperation('draft-load');
      const response = await getArtworkDraft(projectId, draftId);
      applyDraft(response.draft, artworkViewForType(response.draft.work_type));
      return true;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return false;
    } finally {
      setOperation('');
    }
  }, [applyDraft, flushAutosave, projectId, selectedDraftId]);

  const changeView = useCallback(async (nextView) => {
    if (!Object.values(ARTWORK_VIEWS).includes(nextView) || nextView === view) return;
    try {
      await flushAutosave();
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return;
    }
    const first = stateRef.current.drafts.find((draft) => artworkViewForType(draft.work_type) === nextView);
    if (first) await selectDraft(first.id);
    else showNewDraft(nextView);
  }, [flushAutosave, selectDraft, showNewDraft, view]);

  const startNewDraft = useCallback(async () => {
    try {
      await flushAutosave();
      showNewDraft(view);
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    }
  }, [flushAutosave, showNewDraft, view]);

  const createDraft = useCallback(async () => {
    const current = editorRef.current;
    if (!current || !draftCanPersist(current)) {
      dispatch({ type: 'error', code: 'invalid_artwork_request' });
      return null;
    }
    setOperation('draft-create');
    try {
      const response = await createArtworkDraft(projectId, {
        ...draftMutationFields(current),
        idempotency_key: newArtworkIdempotencyKey('draft')
      });
      applyDraft(response.draft, view);
      dispatch({ type: 'notice', notice: '草稿已创建，后续编辑会自动保存。' });
      return response.draft;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return null;
    } finally {
      setOperation('');
    }
  }, [applyDraft, projectId, view]);

  const retryAutosave = useCallback(async () => {
    try {
      await autosaveRef.current?.retry();
      dispatch({ type: 'notice', notice: '草稿已保存。' });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    }
  }, []);

  const runPromptGeneration = useCallback(async () => {
    if (!selectedDraftId) return;
    setOperation('prompt');
    try {
      await flushAutosave();
      const response = await generateArtworkPrompt(projectId, selectedDraftId, {
        expected_version: serverVersionRef.current,
        idempotency_key: newArtworkIdempotencyKey('prompt')
      });
      if (response.draft) applyDraft(response.draft, view);
      await refreshWorkspace();
      dispatch({ type: 'notice', notice: response.reused ? '已恢复同一次提示词请求。' : 'AI 提示词已生成，可继续手动编辑。' });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally {
      setOperation('');
    }
  }, [applyDraft, flushAutosave, projectId, refreshWorkspace, selectedDraftId, view]);

  const confirmStale = useCallback(async () => {
    const current = editorRef.current;
    if (!current?.current_source_signature) return false;
    setOperation('stale-confirm');
    try {
      await flushAutosave();
      const response = await confirmArtworkStalePrompt(projectId, selectedDraftId, {
        expected_version: serverVersionRef.current,
        source_signature: current.current_source_signature
      });
      applyDraft(response.draft, view);
      dispatch({ type: 'notice', notice: '已确认当前来源；提示词内容未被自动改写。' });
      return true;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return false;
    } finally {
      setOperation('');
    }
  }, [applyDraft, flushAutosave, projectId, selectedDraftId, view]);

  const submitImageGeneration = useCallback(async () => {
    if (!selectedDraftId) return false;
    const current = editorRef.current;
    if (current?.is_stale) {
      dispatch({ type: 'error', code: 'stale_prompt_confirmation_required' });
      return false;
    }
    setOperation('image');
    try {
      await flushAutosave();
      await generateArtworkImage(projectId, selectedDraftId, {
        expected_version: serverVersionRef.current,
        idempotency_key: newArtworkIdempotencyKey('image')
      });
      await refreshWorkspace();
      dispatch({ type: 'notice', notice: '已提交 1 张图片任务；不会自动追加或重试。' });
      return true;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return false;
    } finally {
      setOperation('');
    }
  }, [flushAutosave, projectId, refreshWorkspace, selectedDraftId]);

  const loadMoreDrafts = useCallback(async () => {
    if (!stateRef.current.draftsCursor) return;
    setOperation('drafts-more');
    try {
      const page = await listArtworkDrafts(projectId, { cursor: stateRef.current.draftsCursor });
      dispatch({ type: 'drafts-append', page });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, [projectId]);

  const loadMoreAssets = useCallback(async () => {
    if (!stateRef.current.assetsCursor) return;
    setOperation('assets-more');
    try {
      const page = await listArtworkAssets(projectId, { cursor: stateRef.current.assetsCursor });
      dispatch({ type: 'assets-append', page });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, [projectId]);

  const reuseAsset = useCallback(async (asset) => {
    setOperation(`reuse:${asset.id}`);
    try {
      await flushAutosave();
      const response = await reuseArtworkAsset(projectId, asset.id, { idempotency_key: newArtworkIdempotencyKey('reuse') });
      applyDraft(response.draft, artworkViewForType(response.draft.work_type));
      dispatch({ type: 'notice', notice: '已按图片生成时的不可变参数创建新草稿。' });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, [applyDraft, flushAutosave, projectId]);

  const applyAsset = useCallback(async (asset) => {
    setOperation(`apply:${asset.id}`);
    try {
      const response = await applyArtworkAsset(projectId, asset.id);
      dispatch({ type: 'asset-apply', asset: response.asset });
      dispatch({ type: 'notice', notice: `图片已应用到 ${response.applied_to || '当前目标'}。` });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, [projectId]);

  const unapplyAsset = useCallback(async (asset) => {
    setOperation(`unapply:${asset.id}`);
    try {
      const response = await unapplyArtworkAsset(projectId, asset.id);
      dispatch({ type: 'asset-unapply', asset: response.asset });
      dispatch({ type: 'notice', notice: '已取消应用；原始图片仍保留在图库。' });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, [projectId]);

  const removeAsset = useCallback(async (asset) => {
    if (asset.applied) {
      dispatch({ type: 'error', code: 'asset_is_applied' });
      return false;
    }
    setOperation(`delete:${asset.id}`);
    try {
      await deleteArtworkAsset(projectId, asset.id);
      dispatch({ type: 'asset-remove', id: asset.id });
      dispatch({ type: 'notice', notice: '图片已删除。' });
      return true;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return false;
    } finally { setOperation(''); }
  }, [projectId]);

  const removeDraft = useCallback(async () => {
    if (!selectedDraftId) return false;
    setOperation('draft-delete');
    try {
      await flushAutosave();
      await deleteArtworkDraft(projectId, selectedDraftId, serverVersionRef.current);
      dispatch({ type: 'draft-remove', id: selectedDraftId });
      const remaining = stateRef.current.drafts.filter((draft) => draft.id !== selectedDraftId && artworkViewForType(draft.work_type) === view);
      if (remaining[0]) await selectDraft(remaining[0].id);
      else showNewDraft(view);
      dispatch({ type: 'notice', notice: '草稿已删除，历史图片仍保留在图库。' });
      return true;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return false;
    } finally { setOperation(''); }
  }, [flushAutosave, projectId, selectDraft, selectedDraftId, showNewDraft, view]);

  const downloadAsset = useCallback(async (asset) => {
    setOperation(`download:${asset.id}`);
    try {
      await downloadArtworkAsset(asset);
      dispatch({ type: 'notice', notice: '图片下载已开始。' });
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
    } finally { setOperation(''); }
  }, []);

  const saveGateway = useCallback(async (draft) => {
    setOperation('gateway-save');
    try {
      const response = await saveArtworkGatewayConfig(gatewayPayload(draft));
      dispatch({ type: 'gateway', config: response.config, notice: 'AI2API 网关设置已保存；密钥不会回显。' });
      return response.config;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return null;
    } finally { setOperation(''); }
  }, []);

  const verifyGateway = useCallback(async (draft) => {
    setOperation('gateway-verify');
    try {
      const response = await verifyArtworkGatewayConfig(gatewayPayload(draft));
      dispatch({ type: 'notice', notice: `验证通过，发现 ${Number(response.model_count || 0)} 个模型；未生成图片。` });
      return response;
    } catch (error) {
      dispatch({ type: 'error', code: artworkErrorCode(error) });
      return null;
    } finally { setOperation(''); }
  }, []);

  const changePromptModel = useCallback(async (value) => {
    const next = parsePromptModelOption(value);
    if (!next.provider || !next.model) return;
    setOperation('prompt-model');
    try {
      await onPromptModelChange?.(next.provider, next.model);
      dispatch({ type: 'notice', notice: '项目默认提示词模型已更新。' });
    } catch {
      dispatch({ type: 'error', code: 'prompt_model_unavailable' });
    } finally { setOperation(''); }
  }, [onPromptModelChange]);

  const filteredDrafts = useMemo(
    () => state.drafts.filter((draft) => artworkViewForType(draft.work_type) === view),
    [state.drafts, view]
  );
  const imageModel = findArtworkModel(state.registry, editor?.model_id);

  return {
    state, view, editor, selectedDraftId, filteredDrafts, autosave, operation,
    errorMessage: state.errorCode ? artworkErrorMessage(state.errorCode) : '',
    promptModels: promptModelOptions(modelConfig),
    selectedPromptModel: selectedPromptModel(modelConfig, runtime),
    imageModel,
    workTypes: workTypesForView(view),
    actions: {
      updateEditor, selectDraft, changeView, startNewDraft, createDraft, retryAutosave,
      runPromptGeneration, confirmStale, submitImageGeneration, refreshWorkspace,
      loadMoreDrafts, loadMoreAssets, reuseAsset, applyAsset, unapplyAsset, removeAsset, removeDraft,
      downloadAsset, saveGateway, verifyGateway, changePromptModel, flushAutosave
    }
  };
}

export function newDraftForView(view, registry, gateway, catalog) {
  const workType = view === ARTWORK_VIEWS.CHARACTER ? ARTWORK_TYPES.PORTRAIT : ARTWORK_TYPES.COVER;
  const scope = normalizeArtworkScope(workType, '', '', catalog);
  const model = normalizeArtworkModelSize(registry, gateway?.default_model, '', workType, gateway?.default_model);
  return {
    id: '', version: 0, work_type: workType, scope: scope.scope, scope_id: scope.scopeId,
    prompt: '', prompt_source: 'manual', model_id: model.modelId, size: model.size,
    source_status: 'not_applicable', is_stale: false
  };
}

export function draftMutationFields(draft = {}) {
  return {
    work_type: draft.work_type,
    scope: draft.scope,
    scope_id: draft.scope_id || '',
    prompt: draft.prompt || '',
    model_id: draft.model_id,
    size: draft.size
  };
}

export function draftCanPersist(draft = {}) {
  if (!draft.work_type || !draft.scope || !draft.model_id || !draft.size || !String(draft.prompt || '').trim()) return false;
  return draft.scope === 'project' || Boolean(draft.scope_id);
}

export function gatewayPayload(draft = {}) {
  const payload = {
    base_url: String(draft.base_url || '').trim(),
    default_model: String(draft.default_model || '').trim(),
    request_timeout_seconds: Number(draft.request_timeout_seconds || 0)
  };
  const apiKey = String(draft.api_key || '').trim();
  if (draft.clear_api_key) payload.clear_api_key = true;
  else if (apiKey) payload.api_key = apiKey;
  return payload;
}
