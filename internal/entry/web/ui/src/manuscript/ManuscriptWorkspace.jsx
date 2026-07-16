import { useEffect, useMemo, useRef, useState } from 'react';
import { ManuscriptRevisionWorkbench } from '../ManuscriptRevisionWorkbench.jsx';
import { ManuscriptTree } from './ManuscriptTree.jsx';
import { ManuscriptOutlineView } from './ManuscriptOutlineView.jsx';
import { ManuscriptReviewView } from './ManuscriptReviewView.jsx';
import { RevisionCompare } from './RevisionCompare.jsx';
import { RevisionHistory } from './RevisionHistory.jsx';
import { RevisionStatus } from './RevisionStatus.jsx';
import { ManuscriptReader } from './ManuscriptReader.jsx';
import { discussManuscriptContext, invalidateManuscriptCache, invalidateManuscriptViews, loadManuscriptArtifact, loadManuscriptChunk, loadManuscriptHistory, loadManuscriptReviewDetail, loadManuscriptReviewPage, loadManuscriptTree, loadManuscriptVersion, previewManuscriptRestore, restoreManuscriptVersion } from './manuscript-api.js';
import { flattenManuscriptTree, MANUSCRIPT_TABS, mergeParagraphChunk } from './manuscript-state.js';
import { normalizeManuscriptMutationEvent } from './manuscript-events.js';
import './manuscript.css';

const newKey = () => `manuscript-workspace:${globalThis.crypto?.randomUUID?.() || Date.now()}`;

export function ManuscriptWorkspace({ projectId, onDiscussionReady }) {
  const [open, setOpen] = useState(false), [drawerOpen, setDrawerOpen] = useState(false);
  const [tree, setTree] = useState([]), [selectedId, setSelectedId] = useState(''), [activeRevision, setActiveRevision] = useState(null);
  const [tab, setTab] = useState('prose'), [current, setCurrent] = useState(null), [candidate, setCandidate] = useState(null);
  const [history, setHistory] = useState({ items: [], nextCursor: 0, hasMore: false }), [historyVersion, setHistoryVersion] = useState(null);
  const [restorePreview, setRestorePreview] = useState(null), [artifacts, setArtifacts] = useState({}), [reviewDetails, setReviewDetails] = useState({});
  const [historyRecovery, setHistoryRecovery] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const requestRef = useRef({}), busyOwnerRef = useRef(''), projectEpochRef = useRef(0), selectionEpochRef = useRef(0), selectionIdRef = useRef(''), tabRef = useRef('prose'), previousProjectRef = useRef(projectId);
  const restoreKeys = useRef(new Map()), treeButtonRef = useRef(null), drawerRef = useRef(null);
  const chapters = useMemo(() => flattenManuscriptTree(tree), [tree]);
  const node = chapters.find((item) => item.stable_id === selectedId);
  function beginRequest(kind, stableId = selectionIdRef.current) {
    requestRef.current[kind]?.controller.abort();
    const controller = new AbortController(), sequence = (requestRef.current[kind]?.sequence || 0) + 1;
    const epoch = projectEpochRef.current, selectionEpoch = selectionEpochRef.current;
    requestRef.current[kind] = { controller, sequence, epoch, selectionEpoch, stableId };
    return { controller, sequence, epoch, selectionEpoch, stableId };
  }
  const isLatest = (kind, sequence, epoch = projectEpochRef.current, selectionEpoch, stableId) => {
    const request = requestRef.current[kind];
    if (request?.sequence !== sequence || projectEpochRef.current !== epoch) return false;
    if (kind === 'tree') return true;
    return selectionEpochRef.current === (selectionEpoch ?? request.selectionEpoch) && selectionIdRef.current === (stableId ?? request.stableId);
  };
  const selectionSnapshot = () => ({ projectId, projectEpoch: projectEpochRef.current, selectionEpoch: selectionEpochRef.current, stableId: selectionIdRef.current, tab: tabRef.current });
  const isCurrentSelection = (snapshot, includeTab = false) => snapshot.projectId === projectId
    && snapshot.projectEpoch === projectEpochRef.current
    && snapshot.selectionEpoch === selectionEpochRef.current
    && snapshot.stableId === selectionIdRef.current
    && (!includeTab || snapshot.tab === tabRef.current);
  function beginBusy(kind, request) {
    const owner = `${kind}:${request.sequence}:${request.epoch}:${request.selectionEpoch ?? ''}:${request.stableId ?? ''}`;
    busyOwnerRef.current = owner;
    setBusy(true);
    return owner;
  }
  function endBusy(owner) {
    if (busyOwnerRef.current !== owner) return;
    busyOwnerRef.current = '';
    setBusy(false);
  }
  function beginSelection(stableId) {
    selectionEpochRef.current += 1;
    selectionIdRef.current = stableId;
    Object.entries(requestRef.current).forEach(([kind, request]) => {
      if (kind !== 'tree') request.controller.abort();
    });
  }
  useEffect(() => () => {
    Object.values(requestRef.current).forEach((entry) => entry.controller.abort());
    invalidateManuscriptCache(projectId);
  }, [projectId]);
  useEffect(() => {
    if (previousProjectRef.current === projectId) return;
    previousProjectRef.current = projectId;
    projectEpochRef.current += 1;
    selectionEpochRef.current += 1;
    selectionIdRef.current = '';
    Object.values(requestRef.current).forEach((entry) => entry.controller.abort());
    requestRef.current = {};
    restoreKeys.current.clear();
    setTree([]); setSelectedId(''); setActiveRevision(null); setCurrent(null); setCandidate(null);
    setHistory({ items: [], nextCursor: 0, hasMore: false }); setHistoryVersion(null); setRestorePreview(null); setHistoryRecovery(false);
    busyOwnerRef.current = '';
    tabRef.current = 'prose';
    setArtifacts({}); setReviewDetails({}); setDrawerOpen(false); setError(''); setNotice(''); setBusy(false);
    invalidateManuscriptCache(projectId);
    if (open && projectId) queueMicrotask(() => void loadTree());
  }, [projectId]);
  useEffect(() => {
    if (!drawerOpen) return undefined;
    const drawer = drawerRef.current;
    queueMicrotask(() => drawer?.querySelector('[data-manuscript-drawer-initial]')?.focus());
    const keepFocusInside = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setDrawerOpen(false);
        queueMicrotask(() => treeButtonRef.current?.focus());
        return;
      }
      if (event.key !== 'Tab' || !drawer) return;
      const focusable = [...drawer.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')];
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    drawer.addEventListener('keydown', keepFocusInside);
    return () => drawer.removeEventListener('keydown', keepFocusInside);
  }, [drawerOpen]);
  useEffect(() => {
    if (!projectId || !open || typeof EventSource === 'undefined') return undefined;
    const source = new EventSource(`/api/projects/${encodeURIComponent(projectId)}/events`);
    const refresh = (event) => {
      let detail = {};
      try { detail = event?.data ? JSON.parse(event.data) : {}; } catch { detail = {}; }
      const mutation = normalizeManuscriptMutationEvent(detail);
      if (!mutation) return;
      invalidateManuscriptViews(projectId, mutation);
      void refreshVisible(mutation);
    };
    source.onmessage = refresh;
    ['snapshot', 'progress', 'host_event', 'action'].forEach((name) => source.addEventListener(name, refresh));
    return () => source.close();
  }, [open, projectId, selectedId, tab]);
  useEffect(() => {
    const refresh = (event) => {
      if (!String(event.detail?.path || '').includes(`/projects/${encodeURIComponent(projectId)}/`)) return;
      invalidateManuscriptViews(projectId, event.detail || {});
      if (open) void refreshVisible(event.detail || {});
    };
    window.addEventListener('ainovel:manuscript-mutated', refresh);
    return () => window.removeEventListener('ainovel:manuscript-mutated', refresh);
  }, [open, projectId, selectedId, tab]);

  async function refreshVisible() {
    const snapshot = selectionSnapshot();
    const result = await loadTree(false, snapshot);
    if (!result || !isCurrentSelection(snapshot)) return;
    if (snapshot.stableId) await selectChapter(snapshot.stableId, false, result.nodes, result.active_revision, true);
    if (!isCurrentSelection(snapshot, true)) return;
    if (snapshot.tab === 'history') await loadHistory(true);
    if (['outline', 'volume', 'review'].includes(snapshot.tab)) {
      setArtifacts((old) => { const next = { ...old }; delete next[snapshot.tab]; return next; });
      await chooseTab(snapshot.tab, true);
    }
  }
  async function loadTree(selectFirst = true, selectionGuard = null) {
    if (!projectId) return null;
    const request = beginRequest('tree');
    const { controller, sequence, epoch } = request;
    const busyOwner = beginBusy('tree', request);
    setError('');
    try {
      const data = await loadManuscriptTree(projectId, { signal: controller.signal });
      if (!isLatest('tree', sequence, epoch)) return null;
      const nodes = data.nodes || [];
      setTree(nodes); setActiveRevision(data.active_revision || null);
      const first = flattenManuscriptTree(nodes)[0]?.stable_id || '';
      if (selectFirst && !selectedId && first) await selectChapter(first, false, nodes, data.active_revision);
      return data;
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('tree', sequence, epoch) && (!selectionGuard || isCurrentSelection(selectionGuard))) setError(cause.message); }
    finally { endBusy(busyOwner); }
    return null;
  }
  async function selectChapter(stableId, focus = false, knownTree = tree, knownActive = activeRevision, refreshOnly = false) {
    if (refreshOnly && stableId !== selectionIdRef.current) return;
    if (!refreshOnly) beginSelection(stableId);
    const request = beginRequest('chapter', stableId);
    const { controller, sequence, epoch, selectionEpoch } = request;
    const busyOwner = beginBusy('chapter', request);
    const retainsLastSuccessful = stableId === selectedId && current?.stable_id === stableId;
    if (!refreshOnly) {
      setDrawerOpen(false);
      setSelectedId(stableId);
      if (!retainsLastSuccessful) { setCurrent(null); setCandidate(null); }
      setHistoryVersion(null); setRestorePreview(null); setHistoryRecovery(false); setHistory({ items: [], nextCursor: 0, hasMore: false }); setArtifacts({}); setReviewDetails({}); setError('');
    }
    try {
      const next = await loadManuscriptChunk(projectId, stableId, { signal: controller.signal });
      if (!isLatest('chapter', sequence, epoch, selectionEpoch, stableId) || !next?.chapter?.content_signature || !(next.chapter.paragraphs || []).length) throw new Error('正文响应为空，已保留上次成功内容');
      setCurrent(next.chapter);
      const selectedNode = flattenManuscriptTree(knownTree).find((item) => item.stable_id === stableId);
      if (selectedNode?.has_candidate && knownActive?.revision_id) {
        const draft = await loadManuscriptChunk(projectId, stableId, { view: 'candidate', version: knownActive.revision_id, signal: controller.signal });
        if (isLatest('chapter', sequence, epoch, selectionEpoch, stableId) && draft?.chapter?.content_signature && (draft.chapter.paragraphs || []).length) setCandidate({ ...draft.chapter, revision_id: knownActive.revision_id });
      } else if (isLatest('chapter', sequence, epoch, selectionEpoch, stableId)) setCandidate(null);
      if (focus) queueMicrotask(() => document.querySelector('[role="treeitem"][aria-selected="true"]')?.focus());
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('chapter', sequence, epoch, selectionEpoch, stableId)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function more(view) {
    const old = view === 'candidate' ? candidate : current;
    if (!old || old.next_cursor == null) return;
    const kind = `more:${view}`, { controller, sequence } = beginRequest(kind);
    try {
      const data = await loadManuscriptChunk(projectId, selectedId, { view, version: old.revision_id, signature: old.content_signature, cursor: old.next_cursor, signal: controller.signal });
      if (isLatest(kind, sequence)) (view === 'candidate' ? setCandidate : setCurrent)((previous) => mergeParagraphChunk(previous, data.chapter));
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest(kind, sequence)) setError(cause.message); }
  }
  async function chooseTab(next, force = false) {
    tabRef.current = next;
    setTab(next);
    if (['outline', 'volume', 'review'].includes(next) && (force || !artifacts[next])) {
      const kind = `artifact:${next}`, request = beginRequest(kind), { controller, sequence } = request;
      const volume = tree.find((item) => (item.children || []).some((arc) => (arc.children || []).some((chapter) => chapter.stable_id === selectedId)));
      const stableId = next === 'volume' ? volume?.stable_id : selectedId;
      if (!stableId) return;
      const busyOwner = beginBusy(kind, request);
      try { const data = await loadManuscriptArtifact(projectId, next, stableId, next === 'outline' ? node?.content_signature : '', controller.signal); if (isLatest(kind, sequence)) setArtifacts((old) => ({ ...old, [next]: data.artifact })); }
      catch (cause) { if (cause.name !== 'AbortError' && isLatest(kind, sequence)) setError(cause.message); }
      finally { endBusy(busyOwner); }
      return;
    }
    if (next === 'history' && (force || !history.items.length)) await loadHistory(true);
  }
  function tabKeyDown(event, index) {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % MANUSCRIPT_TABS.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + MANUSCRIPT_TABS.length) % MANUSCRIPT_TABS.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = MANUSCRIPT_TABS.length - 1;
    else return;
    event.preventDefault();
    const id = MANUSCRIPT_TABS[next][0]; void chooseTab(id); document.getElementById(`manuscript-tab-${id}`)?.focus();
  }
  async function loadHistory(reset = false) {
    const cursor = reset ? 0 : history.nextCursor;
    const request = beginRequest('history'), { controller, sequence } = request;
    const busyOwner = beginBusy('history', request);
    try { const data = await loadManuscriptHistory(projectId, selectedId, cursor, controller.signal); if (isLatest('history', sequence)) setHistory((old) => ({ items: reset ? (data.items || []) : [...old.items, ...(data.items || [])], nextCursor: data.next_cursor || 0, hasMore: Boolean(data.has_more) })); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('history', sequence)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function openReview(audit) {
    if (reviewDetails[audit.revision_id]) return setReviewDetails((old) => { const next = { ...old }; delete next[audit.revision_id]; return next; });
    const request = beginRequest('review-detail'), { controller, sequence } = request;
    const busyOwner = beginBusy('review-detail', request);
    try { const data = await loadManuscriptReviewDetail(projectId, selectedId, audit.revision_id, audit.signature, controller.signal); if (isLatest('review-detail', sequence)) setReviewDetails((old) => ({ ...old, [audit.revision_id]: data.artifact })); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('review-detail', sequence)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  async function loadMoreReview() {
    const currentReview = artifacts.review;
    const cursor = currentReview?.content?.next_cursor;
    if (cursor == null || !currentReview?.content?.has_more) return;
    const request = beginRequest('artifact:review-more'), { controller, sequence, epoch } = request;
    const busyOwner = beginBusy('artifact:review-more', request);
    try {
      const data = await loadManuscriptReviewPage(projectId, selectedId, cursor, controller.signal);
      if (!isLatest('artifact:review-more', sequence, epoch)) return;
      setArtifacts((old) => {
        const previous = old.review;
        if (!previous || previous.stable_id !== selectedId) return old;
        return { ...old, review: { ...data.artifact, content: { ...data.artifact.content, revisions: [...(previous.content?.revisions || []), ...(data.artifact.content?.revisions || [])], audits: [...(previous.content?.audits || []), ...(data.artifact.content?.audits || [])] } } };
      });
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('artifact:review-more', sequence, epoch)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  function recoverGoneVersion(cause, kind, sequence, epoch) {
    if (cause.status !== 410 && cause.data?.error?.code !== 'version_gone') return false;
    if (!isLatest(kind, sequence, epoch)) return true;
    requestRef.current['version-more']?.controller.abort();
    requestRef.current['restore-preview']?.controller.abort();
    requestRef.current['restore-confirm']?.controller.abort();
    setHistoryVersion(null);
    setRestorePreview(null);
    setHistoryRecovery(true);
    setError(cause.message);
    setNotice('历史版本已被清理；当前正式正文仍保留。请重新加载历史并选择可用版本。');
    return true;
  }
  async function openVersion(item) {
	requestRef.current['version-more']?.controller.abort();
    const request = beginRequest('version'), { controller, sequence, epoch } = request, busyOwner = beginBusy('version', request);
    try { const data = await loadManuscriptVersion(projectId, item.revision_id, selectedId, 0, controller.signal); if (isLatest('version', sequence, epoch)) { setHistoryVersion({ ...data.chapter, revision_id: item.revision_id }); setRestorePreview(null); setHistoryRecovery(false); } }
    catch (cause) { if (cause.name !== 'AbortError' && !recoverGoneVersion(cause, 'version', sequence, epoch) && isLatest('version', sequence, epoch)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
	async function moreVersion() {
		const opened = historyVersion;
		if (!opened || opened.next_cursor == null) return;
		const { controller, sequence, epoch } = beginRequest('version-more');
		try {
			const data = await loadManuscriptVersion(projectId, opened.revision_id, selectedId, opened.next_cursor, controller.signal);
			const incoming = data.chapter;
			if (!isLatest('version-more', sequence, epoch)) return;
			if (incoming.version_id !== opened.revision_id || incoming.stable_id !== opened.stable_id || incoming.content_signature !== opened.content_signature) {
				throw new Error('历史版本分页签名发生变化，请重新打开该版本');
			}
			setHistoryVersion((previous) => previous?.revision_id === opened.revision_id ? mergeParagraphChunk(previous, incoming) : previous);
		} catch (cause) {
			if (cause.name !== 'AbortError' && !recoverGoneVersion(cause, 'version-more', sequence, epoch) && isLatest('version-more', sequence, epoch)) setError(cause.message);
		}
	}
  async function previewRestore(item) {
    if (!historyVersion || historyVersion.revision_id !== item.revision_id) return;
    const key = restoreKeys.current.get(item.revision_id) || newKey(); restoreKeys.current.set(item.revision_id, key);
    const request = beginRequest('restore-preview'), { controller, sequence, epoch } = request, busyOwner = beginBusy('restore-preview', request); setError('');
    try { const data = await previewManuscriptRestore(projectId, { revision_id: item.revision_id, chapter_id: selectedId, expected_content_signature: historyVersion.content_signature, idempotency_key: key }, controller.signal); if (isLatest('restore-preview', sequence, epoch)) setRestorePreview(data.preview); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('restore-preview', sequence, epoch)) { setRestorePreview(null); setError(cause.message); setNotice(cause.data?.error?.action === 'refresh_preview' ? '版本已变化，请重新打开历史正文后再预览。' : '恢复当前被阻断；请按错误提示处理后重试。'); } }
    finally { endBusy(busyOwner); }
  }
  async function confirmRestore(item) {
    if (!restorePreview || restorePreview.source_revision_id !== item.revision_id) return;
    const request = beginRequest('restore-confirm'), { controller, sequence, epoch } = request, busyOwner = beginBusy('restore-confirm', request); setError('');
    try { await restoreManuscriptVersion(projectId, { revision_id: item.revision_id, chapter_id: selectedId, expected_content_signature: historyVersion.content_signature, idempotency_key: restoreKeys.current.get(item.revision_id), preview_signature: restorePreview.preview_signature }, controller.signal); if (!isLatest('restore-confirm', sequence, epoch)) return; setRestorePreview(null); setNotice('已从历史版本创建新的修订；当前正式稿未被覆盖，仍需独立审核与确认。'); await refreshVisible(); }
    catch (cause) { if (cause.name !== 'AbortError' && isLatest('restore-confirm', sequence, epoch)) { setRestorePreview(null); setError(cause.message); setNotice('恢复条件已变化，请重新预览后再确认。'); } }
    finally { endBusy(busyOwner); }
  }
  async function discuss() {
    if (!current) return; const request = beginRequest('discuss'), { controller, sequence, epoch } = request, busyOwner = beginBusy('discuss', request); setError('');
    try {
      const data = await discussManuscriptContext(projectId, { stable_id: selectedId, artifact_kind: 'prose', view: 'current', selection_start: 0, selection_end: 0, intent: '讨论当前章节并提出可执行修改建议', content_signature: current.content_signature }, controller.signal);
      if (!isLatest('discuss', sequence, epoch)) return;
      if (data.discussion?.target !== 'cocreate' || !data.discussion?.message) throw new Error('讨论入口未返回可消费的共创消息');
		onDiscussionReady?.(data.discussion.message);
		setNotice(`已将服务端裁剪的受限上下文送入共创：${(data.chips || []).join('、')}`);
    } catch (cause) { if (cause.name !== 'AbortError' && isLatest('discuss', sequence, epoch)) setError(cause.message); }
    finally { endBusy(busyOwner); }
  }
  return <section className="manuscript-workspace-shell">
    <button type="button" className="manuscript-workspace-toggle" aria-expanded={open} onClick={() => { setOpen(!open); if (!open && !tree.length) void loadTree(); }}>专业长篇稿件工作区</button>
    {open ? <div className="manuscript-workspace">
      <button ref={treeButtonRef} className="manuscript-tree-open" type="button" tabIndex={drawerOpen ? -1 : 0} aria-haspopup="dialog" aria-expanded={drawerOpen} onClick={() => setDrawerOpen(true)}>打开稿件目录</button>
      <div ref={drawerRef} className={drawerOpen ? 'manuscript-tree-drawer open' : 'manuscript-tree-drawer'} role="dialog" aria-modal={drawerOpen ? 'true' : undefined} aria-label="稿件目录抽屉"><ManuscriptTree nodes={tree} selectedId={selectedId} onSelect={selectChapter} onClose={() => { setDrawerOpen(false); queueMicrotask(() => treeButtonRef.current?.focus()); }} /></div>
      <main inert={drawerOpen || undefined}><RevisionStatus node={node} /><div role="tablist" aria-label="稿件视图">{MANUSCRIPT_TABS.map(([id, label], index) => <button id={`manuscript-tab-${id}`} key={id} role="tab" aria-selected={tab === id} aria-controls={`manuscript-panel-${id}`} tabIndex={tab === id ? 0 : -1} onKeyDown={(event) => tabKeyDown(event, index)} onClick={() => void chooseTab(id)}>{label}</button>)}</div>
        <div role="tabpanel" id={`manuscript-panel-${tab}`} aria-labelledby={`manuscript-tab-${tab}`} tabIndex="0">
          {tab === 'prose' ? <RevisionCompare current={current} candidate={candidate} busy={busy} error={error} onMoreCurrent={() => more('current')} onMoreCandidate={() => more('candidate')} onRetry={() => selectChapter(selectedId)} /> : null}
          {tab === 'outline' ? <ManuscriptOutlineView artifact={artifacts.outline} busy={busy} /> : null}
          {tab === 'volume' ? <section aria-busy={busy}><h3>所属分卷</h3>{artifacts.volume ? <><h4>{artifacts.volume.content?.title}</h4><p>{artifacts.volume.content?.theme}</p><ol>{(artifacts.volume.content?.arcs || []).map((arc) => <li key={arc.id}>{arc.title}：{arc.goal}</li>)}</ol><small>分卷内容已校验</small></> : <p>正在加载已校验的分卷视图…</p>}</section> : null}
          {tab === 'review' ? <ManuscriptReviewView artifact={artifacts.review} details={reviewDetails} busy={busy} onOpen={openReview} onMore={loadMoreReview} /> : null}
			{tab === 'history' ? <><RevisionHistory items={history.items} selected={historyVersion} preview={restorePreview} onOpen={openVersion} onPreview={previewRestore} onConfirm={confirmRestore} onMore={() => loadHistory(false)} hasMore={history.hasMore} loading={busy} />{historyRecovery ? <button type="button" disabled={busy} onClick={() => { setHistoryRecovery(false); void loadHistory(true); }}>重新加载历史</button> : null}{historyVersion ? <section><h3>历史版本正文</h3><ManuscriptReader chapter={historyVersion} busy={busy} error={error} onMore={moreVersion} onRetry={() => openVersion({ revision_id: historyVersion.revision_id })} /></section> : null}</> : null}
        </div>
        <button type="button" onClick={discuss} disabled={!current || busy}>带当前上下文去讨论</button><div role="status" aria-live="polite">{notice}</div>{error ? <div role="alert">{error}</div> : null}
      </main>
    </div> : null}
    <ManuscriptRevisionWorkbench projectId={projectId} />
  </section>;
}
