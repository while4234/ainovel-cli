import { describe, expect, it, vi } from 'vitest';
import { clearArtworkLocation, readArtworkLocation, writeArtworkLocation } from './artwork-url.js';

describe('artwork URL state', () => {
  it('restores only supported artwork views', () => {
    expect(readArtworkLocation('?projectId=p1&view=character&draft=d1')).toEqual({ projectId: 'p1', view: 'character', draftId: 'd1' });
    expect(readArtworkLocation('?projectId=p1&view=storyboard&draft=d1').view).toBe('');
  });

  it('persists project, view, and draft while preserving unrelated params', () => {
    const target = { location: { href: 'http://127.0.0.1:9898/?keep=1' }, history: { state: { a: 1 }, replaceState: vi.fn() } };
    expect(writeArtworkLocation({ projectId: 'p 1', view: 'story', draftId: 'd/1' }, target)).toBe('/?keep=1&projectId=p+1&view=story&draft=d%2F1');
    expect(target.history.replaceState).toHaveBeenCalledWith({ a: 1 }, '', '/?keep=1&projectId=p+1&view=story&draft=d%2F1');
    clearArtworkLocation(target);
    expect(target.history.replaceState).toHaveBeenLastCalledWith({ a: 1 }, '', '/?keep=1');
  });
});
