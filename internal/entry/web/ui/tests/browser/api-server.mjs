import http from 'node:http';
import { manuscriptMutationWebEvent } from '../../src/manuscript/manuscript-events.js';

const port = 4180;
const chapterId = 'ch_0123456789abcdef0123456789abcdef';
const revisionId = 'rev_0123456789abcdef0123456789abcdef';
const historyId = 'rev_11111111111111111111111111111111';
const deepReviewId = 'rev_00000000000000000000000000000105';
const paragraphs = Array.from({ length: 240 }, (_, index) => `第${index + 1}段${'长篇正文'.repeat(130)}`);
let generation = 1;
let failNextChapter = false;
let historyTombstoned = false;
let delayNextHistory = false;
let delayNextTree = false;
let failDelayedTree = false;
let delayNextChapter = false;
let manuscriptPhase = 'writing';
let manuscriptActionDialogue = null;
const streams = new Set();
let expansionMetadata = await fetch('http://127.0.0.1:4182/api/test/expansion-metadata').then((response) => response.json());
const json = (response, status, body, headers = {}) => {
	if (response.destroyed || response.writableEnded) return;
	try {
		response.writeHead(status, { 'content-type': 'application/json; charset=utf-8', ...headers });
		response.end(JSON.stringify(body));
	} catch { /* aborted race-test requests are intentionally discarded */ }
};
const tree = () => ({ phase: manuscriptPhase, mode: 'adaptation', structure_revision: expansionMetadata.structure_revision, structure_signature: expansionMetadata.structure_signature, active_revision: { revision_id: revisionId, stage: 'audit_pending' }, nodes: [{ kind: 'volume', stable_id: 'vol_0123456789abcdef0123456789abcdef', display_label: '第一卷', state: 'planned', children: [{ kind: 'arc', stable_id: 'arc_0123456789abcdef0123456789abcdef', display_label: '第一故事弧', state: 'planned', children: [{ kind: 'chapter', stable_id: chapterId, display_order: 1, display_label: '真实后端长章', state: 'review_pending', has_current: true, has_candidate: true, has_history: true, content_signature: 'outline-signature', target_display: '目标第 1 章', source_display: '原著第 7–8 章' }] }] }] });
const extraTreeChapters = Array.from({ length: 129 }, (_, index) => ({
  kind: 'chapter', stable_id: `ch_${(index + 2).toString(16).padStart(32, '0')}`, display_order: index + 2,
  display_label: `keyboard chapter ${index + 2}`, state: 'planned', has_current: false, has_candidate: false, has_history: false
}));
const treeWithLargeCatalog = () => {
  const data = tree();
  data.nodes[0].children[0].children.push(...extraTreeChapters);
  return data;
};

function manuscriptChapter(url, stableId = chapterId) {
  const view = url.searchParams.get('view') || 'current';
  if (view === 'candidate' && url.searchParams.get('version') !== revisionId) return null;
  const cursor = Number(url.searchParams.get('cursor') || 0), limit = Number(url.searchParams.get('limit') || 40);
  const prefix = view === 'candidate' ? '候选：' : generation > 1 ? '发布后：' : '当前：';
  const values = paragraphs.slice(cursor, cursor + limit).map((paragraph) => prefix + paragraph);
  return { chapter: { stable_id: stableId, display_chapter: 1, view, revision_id: view === 'candidate' ? revisionId : undefined, content_signature: `${stableId}-${view}-signature-${generation}`, paragraphs: values.map((value) => stableId === chapterId ? value : `B章节：${value}`), next_cursor: cursor + limit < paragraphs.length ? cursor + limit : null, total_paragraphs: paragraphs.length } };
}
const server = http.createServer(async (request, response) => {
	response.on('error', () => {});
  const url = new URL(request.url, `http://${request.headers.host}`), path = url.pathname;
  if (path === '/health') return json(response, 200, { ok: true });
	if (path === '/api/test/reset' && request.method === 'POST') { generation = 1; manuscriptPhase = 'writing'; manuscriptActionDialogue = null; failNextChapter = false; historyTombstoned = false; delayNextHistory = false; delayNextTree = false; failDelayedTree = false; delayNextChapter = false; return json(response, 200, { reset: true }); }
	if (path === '/api/test/refresh-expansion-metadata' && request.method === 'POST') { expansionMetadata = await fetch('http://127.0.0.1:4182/api/test/expansion-metadata').then((result) => result.json()); generation += 1; return json(response, 200, expansionMetadata); }
	if (path === '/api/test/phase-complete' && request.method === 'POST') { manuscriptPhase = 'complete'; generation += 1; return json(response, 200, { phase: manuscriptPhase }); }
	if (path === '/api/test/tombstone-history' && request.method === 'POST') { historyTombstoned = true; return json(response, 200, { tombstoned: true }); }
  if (path === '/api/test/delay-next-history' && request.method === 'POST') { delayNextHistory = true; return json(response, 200, { delayed: true }); }
  if (path === '/api/test/delay-next-tree' && request.method === 'POST') { delayNextTree = true; failDelayedTree = url.searchParams.get('fail') === '1'; return json(response, 200, { delayed: true, fail: failDelayedTree }); }
  if (path === '/api/test/delay-next-chapter' && request.method === 'POST') { delayNextChapter = true; return json(response, 200, { delayed: true }); }
  if (path.endsWith('/events')) {
    response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache', connection: 'keep-alive' });
    response.write(': connected\n\n'); streams.add(response);
    request.on('close', () => streams.delete(response)); return;
  }
  if (path === '/api/projects/browser-project/manuscript/revision/command' && request.method === 'POST') {
    generation += 1;
		const event = manuscriptMutationWebEvent({ scope: 'prose_publish', stable_id: chapterId }, { seq: generation });
		for (const stream of streams) stream.write(`event: action\ndata: ${JSON.stringify(event)}\n\n`);
    return json(response, 200, { generation });
  }
  if (path === '/api/test/emit-mutation' && request.method === 'POST') {
    const event = manuscriptMutationWebEvent({ scope: 'prose_publish', stable_id: chapterId }, { seq: generation + 1 });
    for (const stream of streams) stream.write(`event: action\ndata: ${JSON.stringify(event)}\n\n`);
    return json(response, 200, { emitted: true });
  }
  if (path === '/api/test/fail-next-chapter' && request.method === 'POST') { failNextChapter = true; return json(response, 200, { armed: true }); }
  if (path.endsWith('/manuscript/workspace/tree')) {
    if (delayNextTree) {
      const shouldFail = failDelayedTree;
      delayNextTree = false; failDelayedTree = false;
      setTimeout(() => shouldFail ? json(response, 503, { error: { code: 'temporary_failure', message: 'STALE_TREE_ERROR' } }) : json(response, 200, treeWithLargeCatalog(), { etag: `"tree-${generation}"` }), 350);
      return;
    }
    return json(response, 200, treeWithLargeCatalog(), { etag: `"tree-${generation}"` });
  }
  if (path.includes('/manuscript/workspace/chapters/')) {
    if (failNextChapter) { failNextChapter = false; return json(response, 503, { error: { code: 'temporary_failure', message: 'temporary backend failure' } }); }
    const stableId = decodeURIComponent(path.split('/chapters/')[1]?.split('/')[0] || chapterId);
    const body = manuscriptChapter(url, stableId);
    if (delayNextChapter) { delayNextChapter = false; setTimeout(() => json(response, 200, body, { etag: `"prose-${generation}"` }), 650); return; }
    return body ? json(response, 200, body, { etag: `"prose-${generation}"` }) : json(response, 409, { error: { code: 'preview_stale', message: 'candidate version changed' } });
  }
  if (path.includes('/artifacts/outline/')) return json(response, 200, { artifact: { kind: 'outline', stable_id: chapterId, signature: 'outline-signature', content: { title: '真实后端长章', core_event: '完成真实验收', hook: '发布刷新', scenes: ['真实 API'] } } });
  if (path.includes('/artifacts/volume/')) return json(response, 200, { artifact: { kind: 'volume', stable_id: 'vol_0123456789abcdef0123456789abcdef', signature: 'volume-signature', content: { title: '第一卷', theme: '可靠性', arcs: [{ id: 'arc_0123456789abcdef0123456789abcdef', title: '第一故事弧', goal: '验证真实链路' }] } } });
  if (path.includes(`/artifacts/review/${chapterId}/${historyId}`) || path.includes(`/artifacts/review/${chapterId}/${deepReviewId}`)) return json(response, 200, { artifact: { kind: 'review_detail', stable_id: chapterId, signature: 'review-detail-signature', content: { report: path.includes(deepReviewId) ? '第 101+ 条真实审核详情' : '真实延迟加载审核报告', findings: [] } } });
  if (path.includes('/artifacts/review/')) {
		const cursor = Number(url.searchParams.get('cursor') || 0);
		const revisions = Array.from({ length: Math.min(20, 126 - cursor) }, (_, index) => ({ revision_id: cursor + index === 105 ? deepReviewId : `rev_${(cursor + index + 300).toString(16).padStart(32, '0')}` }));
		const audits = cursor === 0 ? [{ revision_id: historyId, signature: 'audit-signature', content_loaded: false }] : cursor === 100 ? [{ revision_id: deepReviewId, signature: 'deep-audit-signature', content_loaded: false }] : [];
		return json(response, 200, { artifact: { kind: 'review', stable_id: chapterId, signature: `review-signature-${cursor}`, content: { status: 'audit_pending', revisions, audits, next_cursor: Math.min(126, cursor + revisions.length), has_more: cursor + revisions.length < 126 } } });
	}
  if (path.endsWith('/manuscript/workspace/history')) {
    const cursor = Number(url.searchParams.get('cursor') || 0);
    const body = cursor === 0 ? { items: [{ revision_id: historyId, updated_at: '2026-07-16', stage: 'completed' }], next_cursor: 1, has_more: true } : { items: [{ revision_id: 'rev_22222222222222222222222222222222', updated_at: '2026-07-15', stage: 'completed' }], next_cursor: 0, has_more: false };
    if (delayNextHistory) { delayNextHistory = false; setTimeout(() => json(response, 200, body), 350); return; }
    return json(response, 200, body);
  }
  if (path.includes(`/manuscript/workspace/versions/${historyId}`)) {
		if (historyTombstoned) return json(response, 410, { error: { code: 'version_gone', message: 'historical version is no longer available', action: 'reload_history' } });
    const cursor = Number(url.searchParams.get('cursor') || 0), limit = Number(url.searchParams.get('limit') || 40);
    const historyParagraphs = paragraphs.map((paragraph, index) => `历史正式正文${index + 1}：${paragraph}`);
    return json(response, 200, { chapter: { stable_id: chapterId, view: 'history', version_id: historyId, content_signature: 'history-signature', paragraphs: historyParagraphs.slice(cursor, cursor + limit), next_cursor: cursor + limit < historyParagraphs.length ? cursor + limit : null, total_paragraphs: historyParagraphs.length } });
  }
  if (path.endsWith('/manuscript/workspace/restore/preview') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (incoming.revision_id !== historyId || incoming.chapter_id !== chapterId || incoming.expected_content_signature !== 'history-signature') return json(response, 410, { error: { code: 'version_gone', message: 'historical version is unavailable' } });
      json(response, 200, { preview: { source_revision_id: historyId, chapter_id: chapterId, impact: '创建新的 audit_pending 修订；不覆盖当前正式稿', requires_confirmation: true, preview_signature: 'restore-preview-signature' } });
    }); return;
  }
  if (path.endsWith('/manuscript/workspace/restore') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (incoming.preview_signature !== 'restore-preview-signature') return json(response, 409, { error: { code: 'preview_stale', message: 'restore preview does not match' } });
      json(response, 202, { revision: { revision_id: 'rev_restored', stage: 'audit_pending', baseline: { chapter_id: chapterId } } });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/active') && request.method === 'GET') return json(response, 200, { dialogue: manuscriptActionDialogue });
  if (path.endsWith('/manuscript/actions/dialogues') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      if (!incoming.chapter_id || !incoming.content_signature || incoming.prose) return json(response, 400, { error: { code: 'invalid_request', message: 'identifier-only boundary required' } });
      manuscriptActionDialogue = incoming.type === 'expand'
        ? { id: 'mad_browser', type: incoming.type, status: 'ready', chapter_id: incoming.chapter_id, original_chapter_label: '第 1 章', version: 2, round: 1, initial_input: incoming.initial_input, resolved_instruction: incoming.initial_input, expansion: incoming.expansion, messages: [{ role: 'user', content: incoming.initial_input }, { role: 'assistant', content: '扩写要求已明确。' }], questions: [] }
        : { id: 'mad_browser', type: incoming.type, status: 'needs_input', chapter_id: incoming.chapter_id, original_chapter_label: '第 1 章', version: 2, round: 1, initial_input: incoming.initial_input, messages: [{ role: 'user', content: incoming.initial_input }, { role: 'assistant', content: '修改范围会实质影响剧情。' }, { role: 'assistant', question_id: 'r1-scope', content: '只修改冲突场景，还是整章？' }], questions: [{ id: 'r1-scope', prompt: '只修改冲突场景，还是整章？' }] };
      json(response, 201, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/reply') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', () => {
      const incoming = JSON.parse(raw || '{}');
      manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'ready', version: 3, questions: [], resolved_instruction: `只修改冲突场景：${incoming.answer}`, messages: [...manuscriptActionDialogue.messages, { role: 'user', question_id: incoming.question_id, content: incoming.answer }, { role: 'assistant', content: '范围已确认。' }] };
      json(response, 200, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/cancel') && request.method === 'POST') {
    manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'cancelled', version: (manuscriptActionDialogue?.version || 0) + 1 };
    return json(response, 200, { dialogue: manuscriptActionDialogue });
  }
  if (path.endsWith('/manuscript/actions/dialogues/mad_browser/execute') && request.method === 'POST') {
    let raw = ''; request.on('data', (chunk) => { raw += chunk; }); request.on('end', async () => {
      const incoming = JSON.parse(raw || '{}');
      if (manuscriptActionDialogue?.type !== 'expand') return json(response, 409, { error: { code: 'invalid_state', message: 'fixture only executes expansion dialogues' } });
      const expansion = manuscriptActionDialogue.expansion || {};
      const planned = await fetch('http://127.0.0.1:4182/api/projects/browser-project/manuscript/expansion/plan', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ location: expansion.location, reference_ids: expansion.reference_ids || [], sentence: manuscriptActionDialogue.resolved_instruction, adjustment: expansion.adjustment || 'default', expected_structure_revision: expansion.expected_structure_revision, expected_structure_signature: expansion.expected_structure_signature, idempotency_key: incoming.idempotency_key }) });
      const body = await planned.json();
      if (!planned.ok) return json(response, planned.status, body);
      manuscriptActionDialogue = { ...manuscriptActionDialogue, status: 'completed', version: manuscriptActionDialogue.version + 2, result: { kind: 'expansion', preview: body.preview, awaiting_human_confirmation: true } };
      json(response, 202, { dialogue: manuscriptActionDialogue });
    }); return;
  }
  json(response, 404, { error: { code: 'not_found', message: path } });
});

server.on('clientError', (_error, socket) => socket.destroy());

server.listen(port, '127.0.0.1');
const shutdown = () => server.close(() => process.exit(0));
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);
