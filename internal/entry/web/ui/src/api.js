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

export function getSnapshot(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/snapshot`);
}

export function resumeProject(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/resume`, {
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

export function sendCoCreate(projectId, text) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/cocreate/send`, {
    method: 'POST',
    body: JSON.stringify({ text })
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

export function getBackendStatus(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/status`);
}

export function testBackend(projectId) {
  return request(`/api/projects/${encodeURIComponent(projectId)}/backend/test`, {
    method: 'POST',
    body: JSON.stringify({})
  });
}
