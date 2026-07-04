async function request(path, options = {}) {
  const isFormData = options.body instanceof FormData;
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body && !isFormData ? { 'content-type': 'application/json' } : {}),
      ...options.headers
    }
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    const error = new Error(data?.error || `${response.status} ${response.statusText}`);
    error.data = data;
    throw error;
  }
  return data;
}

function queryPath(path, q) {
  const query = String(q || '').trim();
  return query ? `${path}?q=${encodeURIComponent(query)}` : path;
}

export function getRuntime() {
  return request('/api/runtime');
}

export function listStyles() {
  return request('/api/styles');
}

export function listProjects() {
  return request('/api/projects');
}

export function listTrashProjects() {
  return request('/api/trash/projects');
}

export function createProject(name, style) {
  return request('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ name, style })
  });
}

export function renameProject(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, {
    method: 'PATCH',
    body: JSON.stringify({ name })
  });
}

export function trashProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}`, {
    method: 'DELETE'
  });
}

export function listProjectTrash() {
  return request('/api/projects/trash');
}

export function clearProjectTrash() {
  return request('/api/projects/trash', {
    method: 'DELETE'
  });
}

export function restoreTrashProject(projectId) {
  return request(`/api/trash/projects/${encodeURIComponent(projectId)}/restore`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function emptyTrashProjects() {
  return request('/api/trash/projects', {
    method: 'DELETE'
  });
}

export function setProjectStyle(projectId, style) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/style`, {
    method: 'PUT',
    body: JSON.stringify({ style })
  });
}

export function getSnapshot(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/snapshot`);
}

export function resumeProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/resume`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function startProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/start`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function pauseProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/pause`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function continueProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/continue`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function steerProject(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/steer`, {
    method: 'POST',
    body: JSON.stringify({ text })
  });
}

export function uploadSimulationFiles(projectId, files) {
  const body = new FormData();
  for (const file of files) {
    body.append('files', file);
  }
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/files`, {
    method: 'POST',
    body
  });
}

export function analyzeSimulation(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/analyze`, {
    method: 'POST'
  });
}

export function importSimulationProfile(projectId, file) {
  const body = new FormData();
  body.append('profile', file);
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/import`, {
    method: 'POST',
    body
  });
}

export function listSimulationLibrary(q) {
  return request(queryPath('/api/libraries/simulation', q));
}

export function uploadSimulationLibrary(files) {
  const body = new FormData();
  for (const file of files) {
    body.append('files', file);
  }
  return request('/api/libraries/simulation/upload', {
    method: 'POST',
    body
  });
}

export function saveSimulationToLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/library/save`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function loadSimulationFromLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/simulate/library/load`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function importExternalNovel(projectId, file, from = '') {
  const body = new FormData();
  body.append('source', file);
  if (String(from || '').trim()) {
    body.append('from', String(from).trim());
  }
  return request(`/api/projects/${encodeURIComponent(projectId)}/import`, {
    method: 'POST',
    body
  });
}

export function exportProject(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/export`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function runProjectDiagnostic(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/diag`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function uploadAdaptationSource(projectId, file) {
  const body = new FormData();
  body.append('source', file);
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/source`, {
    method: 'POST',
    body
  });
}

export function analyzeAdaptationSource(projectId, sourceFile) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/analyze`, {
    method: 'POST',
    body: JSON.stringify({ source_file: sourceFile })
  });
}

export function startAdaptation(projectId, sourceFile, mode, brief) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/start`, {
    method: 'POST',
    body: JSON.stringify({ source_file: sourceFile, mode, brief })
  });
}

export function listNovelLibrary(q) {
  return request(queryPath('/api/libraries/novels', q));
}

export function saveNovelToLibrary(projectId, name, sourceFile) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/library/save`, {
    method: 'POST',
    body: JSON.stringify({ name, source_file: sourceFile })
  });
}

export function loadNovelFromLibrary(projectId, name) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/library/load`, {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export function buildAdaptationProposal(projectId, sourceFile, mode, brief) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/volumes`, {
    method: 'POST',
    body: JSON.stringify({ source_file: sourceFile, mode, brief })
  });
}

export function reviseAdaptationProposal(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function reviseAdaptationVolumeReview(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/volumes/revise`, {
    method: 'POST',
    body: JSON.stringify(payload || {})
  });
}

export function confirmAdaptationProposalDetails(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/proposal/details`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function confirmAdaptationProposal(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/adapt/confirm`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function beginCoCreate(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/begin`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function sendCoCreate(projectId, text, source = 'custom') {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/send`, {
    method: 'POST',
    body: JSON.stringify({ text, source })
  });
}

export function reviseCoCreate(projectId, messageId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/revise`, {
    method: 'POST',
    body: JSON.stringify({ message_id: messageId, text })
  });
}

export function commitCoCreate(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/commit`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function cancelCoCreate(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/cancel`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function getProjectModels(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models`);
}

export function getGlobalModels() {
  return request('/api/models');
}

export function switchGlobalModel(role, provider, model) {
  return request('/api/models/switch', {
    method: 'POST',
    body: JSON.stringify({ role, provider, model })
  });
}

export function switchGlobalDefaultModel(provider, model) {
  return request('/api/models/default', {
    method: 'POST',
    body: JSON.stringify({ provider, model })
  });
}

export function setGlobalCoCreateTimeout(seconds) {
  return request('/api/models/cocreate-timeout', {
    method: 'POST',
    body: JSON.stringify({ seconds })
  });
}

export function deleteGlobalProviderModel(provider, model) {
  return request('/api/models', {
    method: 'DELETE',
    body: JSON.stringify({ provider, model })
  });
}

export function switchProjectModel(projectId, role, provider, model) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/switch`, {
    method: 'POST',
    body: JSON.stringify({ role, provider, model })
  });
}

export function setProjectThinking(projectId, role, level) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/thinking`, {
    method: 'POST',
    body: JSON.stringify({ role, level })
  });
}

export function setProjectCoCreateTimeout(projectId, seconds) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/cocreate-timeout`, {
    method: 'POST',
    body: JSON.stringify({ seconds })
  });
}

export function deleteProviderModel(projectId, provider, model) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models`, {
    method: 'DELETE',
    body: JSON.stringify({ provider, model })
  });
}

export function addOpenAICompatibleModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/add-openai-compatible`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function addProviderModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/add`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function addGlobalProviderModel(payload) {
  return request('/api/models/add', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function testGlobalProviderModel(payload) {
  return request('/api/models/test', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function testProjectProviderModel(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/test`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function discoverGlobalProviderModels(payload) {
  return request('/api/models/discover', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

export function discoverProjectProviderModels(projectId, payload) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/models/discover`, {
    method: 'POST',
    body: JSON.stringify(payload)
  });
}

function grokLoginPath(projectId, action) {
  if (!projectId) {
    return `/api/models/grok-login/${action}`;
  }
  return `/api/projects/${encodeURIComponent(projectId)}/models/grok-login/${action}`;
}

export function startGrokLogin(projectId, accountId, accountName, openBrowser = false) {
  return request(grokLoginPath(projectId, 'start'), {
    method: 'POST',
    body: JSON.stringify({ account_id: accountId, account_name: accountName, open_browser: openBrowser })
  });
}

export function pollGrokLogin(projectId) {
  return request(grokLoginPath(projectId, 'poll'), {
    method: 'POST',
    body: JSON.stringify({})
  });
}

export function completeGrokLogin(projectId, callback) {
  return request(grokLoginPath(projectId, 'complete'), {
    method: 'POST',
    body: JSON.stringify({ callback })
  });
}

export function getGrokLoginStatus(projectId, accountId) {
  return request(grokLoginPath(projectId, 'status'), {
    method: 'POST',
    body: JSON.stringify({ account_id: accountId })
  });
}

export function getBackendStatus(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/status`);
}

export function testBackend(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/test`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}
