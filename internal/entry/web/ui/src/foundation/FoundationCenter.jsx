import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import { CoreCastEditor } from '../components/CoreCastEditor.jsx';
import { applyFoundation, foundationError, foundationIdempotencyKey, loadFoundation, previewFoundation, retryFoundation } from './foundationApi.js';
import { canApplyFoundation, createFoundationState, foundationReducer } from './foundationReducer.js';
import { cloneFoundation, sourceMajorCharacters } from './foundationModel.js';
import { FoundationOverview } from './FoundationOverview.jsx';
import { CharacterEditor } from './CharacterEditor.jsx';
import { RelationshipEditor } from './RelationshipEditor.jsx';
import { WorldRuleEditor } from './WorldRuleEditor.jsx';
import { FoundationPreview } from './FoundationPreview.jsx';
import { FoundationRevisionStatus } from './FoundationRevisionStatus.jsx';
import './foundation.css';

const tabs = [
  ['overview', '概览'], ['core', '核心角色'], ['characters', '全部角色'], ['relationships', '计划关系'],
  ['rules', '世界规则'], ['preview', '差异与影响'], ['revision', '修订状态']
];

export function FoundationCenter({ projectId, onClose, onOpenCoreCast, onOpenReview }) {
  const [state, dispatch] = useReducer(foundationReducer, projectId, createFoundationState);
  const [tab, setTab] = useState('overview');
  const versionRef = useRef(0);
  const abortRef = useRef(null);
  const applyKeyRef = useRef({ previewID: '', key: '' });

  const requestContext = useCallback(() => ({ projectId, requestVersion: versionRef.current }), [projectId]);
  const load = useCallback(async ({ preserveStale = false, preserveBusy = false, classifiedError = null } = {}) => {
    const version = versionRef.current;
    const controller = new AbortController();
    abortRef.current?.abort();
    abortRef.current = controller;
    if (!preserveStale && !preserveBusy) dispatch({ type: 'load_start', projectId, requestVersion: version });
    try {
      const response = await loadFoundation(projectId, controller.signal);
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: preserveStale ? 'stale_server_loaded' : preserveBusy ? 'busy_server_loaded' : 'load_success', projectId, requestVersion: version, response, error: classifiedError });
    } catch (error) {
      if (controller.signal.aborted || version !== versionRef.current) return;
      dispatch({ type: 'load_failed', projectId, requestVersion: version, error: foundationError(error) });
    }
  }, [projectId]);

  useEffect(() => {
    versionRef.current += 1;
    setTab('overview');
    applyKeyRef.current = { previewID: '', key: '' };
    load();
    return () => { versionRef.current += 1; abortRef.current?.abort(); };
  }, [projectId, load]);

  useEffect(() => {
    if (!['applying', 'auditing', 'regenerating'].includes(state.status)) return undefined;
    let cancelled = false;
    const poll = async () => {
      const version = versionRef.current;
      try {
        const response = await loadFoundation(projectId);
        if (!cancelled && version === versionRef.current) dispatch({ type: 'refresh_runtime', ...requestContext(), response });
      } catch {
        // Poll failures remain recoverable via the explicit refresh action.
      }
    };
    const timer = globalThis.setInterval(poll, 1800);
    poll();
    return () => { cancelled = true; globalThis.clearInterval(timer); };
  }, [projectId, requestContext, state.status, state.server?.activeRevision?.updated_at]);

  useEffect(() => {
    if (state.status === 'completed') onOpenReview?.(state.server?.mode);
  }, [state.status, state.server?.mode, onOpenReview]);

  if (!projectId) return <div className="foundation-center empty-state">请先打开项目。</div>;
  if (state.status === 'loading' || !state.server || !state.draft) return <div className="foundation-center foundation-loading" aria-live="polite" role="status">正在加载 StoryFoundation…</div>;

  const disabled = !state.server.editable || ['previewing', 'applying', 'auditing', 'regenerating', 'awaiting_outline_approval', 'completed', 'failed', 'stale', 'readonly'].includes(state.status);
  const edit = (change) => dispatch({ type: 'edit', ...requestContext(), draft: { ...state.draft, ...change } });
  const runPreview = async () => {
    dispatch({ type: 'preview_start', ...requestContext() });
    if (state.status !== 'dirty' || !state.validation.valid) return;
    const version = versionRef.current;
    const controller = new AbortController(); abortRef.current = controller;
    try {
      const response = await previewFoundation(projectId, state.server, cloneFoundation(state.draft), controller.signal);
      if (version !== versionRef.current || controller.signal.aborted) return;
      dispatch({ type: 'preview_success', ...requestContext(), preview: response.preview });
      setTab('preview');
    } catch (error) {
      if (version !== versionRef.current || controller.signal.aborted) return;
      const classified = foundationError(error);
      dispatch({ type: 'preview_failed', ...requestContext(), error: classified });
      if (classified.code.includes('stale')) await load({ preserveStale: true });
      else if (['foundation_busy', 'foundation_readonly'].includes(classified.code)) await load({ preserveBusy: true, classifiedError: classified });
    }
  };
  const runApply = async () => {
    if (!canApplyFoundation(state)) return dispatch({ type: 'apply_start', ...requestContext(), idempotencyKey: '' });
    const previewID = state.preview.id;
    if (applyKeyRef.current.previewID !== previewID) applyKeyRef.current = { previewID, key: foundationIdempotencyKey('apply') };
    const key = applyKeyRef.current.key;
    dispatch({ type: 'apply_start', ...requestContext(), idempotencyKey: key });
    const version = versionRef.current;
    try {
      const response = await applyFoundation(projectId, previewID, key);
      if (version !== versionRef.current) return;
      dispatch({ type: 'apply_success', ...requestContext(), revision: response.revision }); setTab('revision');
      if (['awaiting_outline_approval', 'completed'].includes(response.revision?.stage)) onOpenReview?.(state.server.mode);
    } catch (error) {
      if (version !== versionRef.current) return;
      const classified = foundationError(error);
      dispatch({ type: 'apply_failed', ...requestContext(), error: classified });
      if (classified.code.includes('stale')) await load({ preserveStale: true });
      else if (['foundation_busy', 'foundation_readonly'].includes(classified.code)) await load({ preserveBusy: true, classifiedError: classified });
    }
  };
  const runRetry = async () => {
    const key = foundationIdempotencyKey('retry');
    dispatch({ type: 'retry_start', ...requestContext() });
    if (state.status !== 'failed' || !state.server.allowedOperations.includes('retry')) return;
    try {
      const response = await retryFoundation(projectId, key);
      dispatch({ type: 'retry_success', ...requestContext(), revision: response.revision });
    } catch (error) { dispatch({ type: 'retry_failed', ...requestContext(), error: foundationError(error) }); }
  };

  return <div className="foundation-center">
    <header className="foundation-header"><div><span className="eyebrow">StoryFoundation</span><h1>设定中心</h1><p>统一管理原创与改编的目标故事设定；SourceFoundation 始终只读。</p></div><button className="tool-button" type="button" onClick={onClose}>返回创作</button></header>
    <div className="foundation-state-strip" aria-live="polite" role="status"><strong>{statusLabel(state.status)}</strong><span>target rev {state.server.baseRevision}</span>{state.server.readonlyReason ? <span>只读原因：{state.server.readonlyReason}</span> : null}</div>
    {state.error ? <div className="error-banner" role="alert"><strong>{state.error.code}</strong><span>{state.error.message}</span></div> : null}
    {state.illegalAction ? <div className="warning-note" role="status">{state.illegalAction}</div> : null}
    {state.status === 'stale' ? <div className="foundation-stale" role="alert"><strong>服务器基线已变化，草稿仍完整保留。</strong><span>先加载最新基线，再用当前草稿重新生成 preview。</span><button className="tool-button" disabled={!state.staleServer} type="button" onClick={() => dispatch({ type: 'rebase_stale', ...requestContext() })}>以最新基线重新对比</button></div> : null}
    {state.validation.summary.length ? <div aria-live="assertive" className="foundation-validation-summary" role="alert"><strong>请处理 {state.validation.summary.length} 个字段问题</strong><ul>{state.validation.summary.map((message) => <li key={message}>{message}</li>)}</ul></div> : null}
    <nav aria-label="设定中心区域" className="foundation-tabs" role="tablist">{tabs.map(([id, label], index) => <button aria-controls={`foundation-panel-${id}`} aria-selected={tab === id} className={tab === id ? 'active' : ''} id={`foundation-tab-${id}`} key={id} role="tab" tabIndex={tab === id ? 0 : -1} type="button" onClick={() => setTab(id)} onKeyDown={(event) => moveTab(event, index, setTab)}>{label}</button>)}</nav>
    <main aria-labelledby={`foundation-tab-${tab}`} className="foundation-panel" id={`foundation-panel-${tab}`} role="tabpanel">
      {tab === 'overview' ? <FoundationOverview server={state.server} draft={state.draft} disabled={disabled} premiseError={state.validation.fields.premise} onPremiseChange={(premise) => edit({ premise })} onOpenCoreCast={onOpenCoreCast} /> : null}
      {tab === 'core' ? <><div className="foundation-section-head"><div><h2>核心角色契约（复用现有编辑器）</h2><p>此处只读展示当前已持久化 CoreCast；修改和确认继续使用现有入口。</p></div><button className="tool-button" type="button" onClick={onOpenCoreCast}>打开现有确认入口</button></div><CoreCastEditor readOnly mode={state.server.mode === 'adaptation' ? 'adapt' : 'normal'} value={state.server.coreCast} completion={state.server.coreCastCompletion} confirmed={state.server.coreCastConfirmed} sourceMajorCharacters={sourceMajorCharacters(state.server.sourceFoundation)} /></> : null}
      {tab === 'characters' ? <CharacterEditor value={state.draft.characters} coreCast={state.server.coreCast} disabled={disabled} errors={state.validation.fields} onChange={(characters) => edit({ characters })} /> : null}
		{tab === 'relationships' ? <RelationshipEditor projectId={projectId} auditSignature={state.server.baseAuditSignature} coreCast={state.server.coreCast} value={state.draft.relationships} characters={state.draft.characters} reviewed={state.draft.relationships_reviewed} disabled={disabled} errors={state.validation.fields} onChange={(relationships) => edit({ relationships })} onReviewedChange={(relationships_reviewed) => edit({ relationships_reviewed })} /> : null}
      {tab === 'rules' ? <WorldRuleEditor value={state.draft.world_rules} disabled={disabled} errors={state.validation.fields} onChange={(world_rules) => edit({ world_rules })} /> : null}
      {tab === 'preview' ? <FoundationPreview preview={state.preview} dirty={state.status === 'dirty'} disabled={['previewing', 'applying'].includes(state.status)} canApply={canApplyFoundation(state)} onPreview={runPreview} onApply={runApply} /> : null}
      {tab === 'revision' ? <FoundationRevisionStatus server={state.server} status={state.status} busy={['applying', 'auditing', 'regenerating'].includes(state.status)} onRefresh={() => load({ preserveStale: state.status === 'stale' })} onRetry={runRetry} onOpenReview={() => onOpenReview?.(state.server.mode)} /> : null}
    </main>
    <footer className="foundation-actions"><span>{state.status === 'dirty' ? '有未预览的设定修改' : state.status === 'preview_ready' ? '预览已持久化，可应用' : '服务端状态已同步'}</span><button className="tool-button accent" disabled={state.status !== 'dirty' || !state.validation.valid} type="button" onClick={runPreview}>预览差异与影响</button></footer>
  </div>;
}

function moveTab(event, index, setTab) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  let next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
  const id = tabs[next][0]; setTab(id); globalThis.requestAnimationFrame?.(() => document.getElementById(`foundation-tab-${id}`)?.focus());
}
function statusLabel(status) { return ({ loading: '加载中', clean: '已同步', dirty: '有修改', previewing: '正在预览', preview_ready: '预览就绪', applying: '正在应用', auditing: '正在审查', regenerating: '正在重新生成', awaiting_outline_approval: '等待大纲 / 提案确认', completed: '已完成', failed: '修订失败', stale: '基线已过期', readonly: '只读' })[status] || status; }
