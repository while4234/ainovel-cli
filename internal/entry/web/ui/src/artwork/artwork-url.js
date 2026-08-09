import { ARTWORK_VIEWS } from './artwork-options.js';

export function readArtworkLocation(search = '') {
  const params = new URLSearchParams(search);
  const viewValue = params.get('view');
  return {
    projectId: String(params.get('projectId') || '').trim(),
    view: Object.values(ARTWORK_VIEWS).includes(viewValue) ? viewValue : '',
    draftId: String(params.get('draft') || '').trim()
  };
}

export function writeArtworkLocation({ projectId = '', view = '', draftId = '' } = {}, target = globalThis) {
  if (!target?.location || !target?.history?.replaceState) return '';
  const url = new URL(target.location.href);
  setOrDelete(url.searchParams, 'projectId', projectId);
  setOrDelete(url.searchParams, 'view', Object.values(ARTWORK_VIEWS).includes(view) ? view : '');
  setOrDelete(url.searchParams, 'draft', draftId);
  const next = `${url.pathname}${url.search}${url.hash}`;
  target.history.replaceState(target.history.state, '', next);
  return next;
}

export function clearArtworkLocation(target = globalThis) {
  return writeArtworkLocation({}, target);
}

function setOrDelete(params, name, value) {
  const normalized = String(value || '').trim();
  if (normalized) params.set(name, normalized);
  else params.delete(name);
}
