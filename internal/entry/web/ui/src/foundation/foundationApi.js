import { applyProjectFoundation, getProjectFoundation, previewProjectFoundation, retryProjectFoundation } from '../api.js';

export async function loadFoundation(projectId, signal) {
  return getProjectFoundation(projectId, { signal });
}

export async function previewFoundation(projectId, server, candidate, signal) {
  return previewProjectFoundation(projectId, server.baseRevision, server.baseAuditSignature, candidate, { signal });
}

export async function applyFoundation(projectId, previewId, idempotencyKey, signal) {
  return applyProjectFoundation(projectId, previewId, idempotencyKey, { signal });
}

export async function retryFoundation(projectId, idempotencyKey, signal) {
  return retryProjectFoundation(projectId, idempotencyKey, { signal });
}

export function foundationError(error) {
  const envelope = error?.data?.error;
  return {
    code: String(envelope?.code || 'foundation_network_error'),
    message: String(envelope?.message || error?.message || 'Foundation 请求失败')
  };
}

export function foundationIdempotencyKey(scope) {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `foundation-${scope}:${random}`;
}
