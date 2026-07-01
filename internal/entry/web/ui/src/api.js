async function request(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: {
      ...(options.body ? { 'content-type': 'application/json' } : {}),
      ...options.headers
    }
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw new Error(data?.error || `${response.status} ${response.statusText}`);
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
