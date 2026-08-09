import { request } from '../api.js';
import { flattenArtworkScopeCatalog } from './artwork-options.js';

const projectBase = (projectId) => `/api/projects/${encodeURIComponent(projectId)}/artwork`;

export const loadArtworkRegistry = (options = {}) => request('/api/artwork/models', options);
export const loadArtworkGatewayConfig = (options = {}) => request('/api/artwork/config', options);
export const saveArtworkGatewayConfig = (payload, options = {}) => request('/api/artwork/config', {
  ...options, method: 'PUT', body: JSON.stringify(payload)
});
export const verifyArtworkGatewayConfig = (payload, options = {}) => request('/api/artwork/config/verify', {
  ...options, method: 'POST', body: JSON.stringify(payload)
});

export function loadArtworkWorkspace(projectId, { limit = 50, signal } = {}) {
  return request(`${projectBase(projectId)}?limit=${limit}`, { signal });
}

export function listArtworkDrafts(projectId, { cursor = '', limit = 50, signal } = {}) {
  return request(pageURL(`${projectBase(projectId)}/drafts`, cursor, limit), { signal });
}

export const getArtworkDraft = (projectId, draftId, options = {}) =>
  request(`${projectBase(projectId)}/drafts/${encodeURIComponent(draftId)}`, options);

export function createArtworkDraft(projectId, payload, options = {}) {
  return request(`${projectBase(projectId)}/drafts`, {
    ...options, method: 'POST', body: JSON.stringify(payload)
  });
}

export function updateArtworkDraft(projectId, draftId, payload, options = {}) {
  return request(`${projectBase(projectId)}/drafts/${encodeURIComponent(draftId)}`, {
    ...options, method: 'PATCH', body: JSON.stringify(payload)
  });
}

export function deleteArtworkDraft(projectId, draftId, expectedVersion, options = {}) {
  return request(`${projectBase(projectId)}/drafts/${encodeURIComponent(draftId)}?expected_version=${expectedVersion}`, {
    ...options, method: 'DELETE'
  });
}

export const generateArtworkPrompt = (projectId, draftId, payload, options = {}) =>
  artworkMutation(projectId, `drafts/${encodeURIComponent(draftId)}/generate-prompt`, payload, options);
export const generateArtworkImage = (projectId, draftId, payload, options = {}) =>
  artworkMutation(projectId, `drafts/${encodeURIComponent(draftId)}/generate-image`, payload, options);
export const confirmArtworkStalePrompt = (projectId, draftId, payload, options = {}) =>
  artworkMutation(projectId, `drafts/${encodeURIComponent(draftId)}/confirm-stale-prompt`, payload, options);

export function listArtworkAssets(projectId, { cursor = '', limit = 50, signal } = {}) {
  return request(pageURL(`${projectBase(projectId)}/assets`, cursor, limit), { signal });
}

export const reuseArtworkAsset = (projectId, assetId, payload, options = {}) =>
  artworkMutation(projectId, `assets/${encodeURIComponent(assetId)}/reuse-as-draft`, payload, options);
export const applyArtworkAsset = (projectId, assetId, options = {}) =>
  artworkMutation(projectId, `assets/${encodeURIComponent(assetId)}/apply`, {}, options);
export const unapplyArtworkAsset = (projectId, assetId, options = {}) =>
  request(`${projectBase(projectId)}/assets/${encodeURIComponent(assetId)}/apply`, { ...options, method: 'DELETE' });
export const deleteArtworkAsset = (projectId, assetId, options = {}) =>
  request(`${projectBase(projectId)}/assets/${encodeURIComponent(assetId)}`, { ...options, method: 'DELETE' });

export async function loadArtworkScopeCatalog(projectId, { signal } = {}) {
  const base = `/api/projects/${encodeURIComponent(projectId)}`;
  const [treeResult, foundationResult] = await Promise.allSettled([
    request(`${base}/manuscript/workspace/tree`, { signal }),
    request(`${base}/foundation`, { signal })
  ]);
  if (signal?.aborted) throw Object.assign(new Error('aborted'), { name: 'AbortError' });
  return flattenArtworkScopeCatalog(
    treeResult.status === 'fulfilled' ? treeResult.value : {},
    foundationResult.status === 'fulfilled' ? foundationResult.value : {}
  );
}

export async function downloadArtworkAsset(asset, dependencies = {}) {
  const fetcher = dependencies.fetch || globalThis.fetch;
  const response = await fetcher(asset.download_url, { credentials: 'same-origin' });
  if (!response.ok) {
    const error = new Error('artwork download failed');
    error.data = { error: { code: 'artwork_download_failed' } };
    throw error;
  }
  const blob = await response.blob();
  const fileName = asset.file_name || `artwork-${asset.id || 'image'}`;
  triggerBlobDownload(blob, fileName, dependencies);
  return { blob, fileName };
}

export function triggerBlobDownload(blob, fileName, dependencies = {}) {
  const documentRef = dependencies.document || globalThis.document;
  const urlRef = dependencies.URL || globalThis.URL;
  if (!documentRef || !urlRef?.createObjectURL) return;
  const objectURL = urlRef.createObjectURL(blob);
  const link = documentRef.createElement('a');
  link.href = objectURL;
  link.download = fileName;
  link.hidden = true;
  documentRef.body.appendChild(link);
  link.click();
  link.remove();
  globalThis.setTimeout?.(() => urlRef.revokeObjectURL(objectURL), 1000);
}

export function newArtworkIdempotencyKey(scope) {
  const id = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `artwork-${scope}:${id}`;
}

function artworkMutation(projectId, suffix, payload, options) {
  return request(`${projectBase(projectId)}/${suffix}`, {
    ...options, method: 'POST', body: JSON.stringify(payload)
  });
}

function pageURL(base, cursor, limit) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set('cursor', cursor);
  return `${base}?${params.toString()}`;
}
