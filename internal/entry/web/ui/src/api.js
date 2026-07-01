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

export function getRuntime() {
  return request('/api/runtime');
}

export function listProjects() {
  return request('/api/projects');
}

export function createProject(name) {
  return request('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ name })
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

export function switchGlobalDefaultModel(provider, model) {
  return request('/api/models/default', {
    method: 'POST',
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
