import { describe, expect, it } from 'vitest';
import { artworkErrorMessage, artworkReducer, createArtworkState, hasActiveArtworkJobs, newTerminalArtworkJobs } from './artwork-state.js';

describe('artwork state', () => {
  it('merges cursor pages without duplicating records', () => {
    const state = { ...createArtworkState(), assets: [{ id: 'a1', applied: false }] };
    const next = artworkReducer(state, { type: 'assets-append', page: { items: [{ id: 'a1', applied: true }, { id: 'a2' }], next_cursor: 'next' } });
    expect(next.assets).toEqual([{ id: 'a1', applied: true }, { id: 'a2' }]);
    expect(next.assetsCursor).toBe('next');
  });

  it('polls only active jobs and detects each new terminal transition', () => {
    const previous = { jobs: [{ id: 'j1', status: 'running' }], promptJobs: [{ id: 'p1', status: 'succeeded' }] };
    expect(hasActiveArtworkJobs(previous)).toBe(true);
    expect(newTerminalArtworkJobs(previous, {
      jobs: { items: [{ id: 'j1', status: 'succeeded' }] },
      prompt_jobs: { items: [{ id: 'p1', status: 'succeeded' }, { id: 'p2', status: 'failed' }] }
    }).map((job) => `${job.job_kind}:${job.id}`)).toEqual(['image:j1', 'prompt:p2']);
    expect(hasActiveArtworkJobs({ jobs: [{ status: 'failed' }], promptJobs: [] })).toBe(false);
  });

  it('localizes safe error codes without forwarding raw provider messages', () => {
    const error = { data: { error: { code: 'gateway_request_failed', message: 'RAW UPSTREAM SECRET DETAIL' } } };
    expect(artworkErrorMessage(error)).toContain('网关验证失败');
    expect(artworkErrorMessage(error)).not.toContain('RAW');
  });

  it('moves protected applied state only within the selected target', () => {
    const previous = {
      ...createArtworkState(),
      assets: [
        { id: 'old', work_type: 'illustration', scope: 'chapter', scope_id: 'one', applied: true },
        { id: 'other', work_type: 'illustration', scope: 'chapter', scope_id: 'two', applied: true }
      ]
    };
    const next = artworkReducer(previous, {
      type: 'asset-apply',
      asset: { id: 'new', work_type: 'illustration', scope: 'chapter', scope_id: 'one', applied: true }
    });
    expect(next.assets.find((asset) => asset.id === 'old').applied).toBe(false);
    expect(next.assets.find((asset) => asset.id === 'new').applied).toBe(true);
    expect(next.assets.find((asset) => asset.id === 'other').applied).toBe(true);

    const unapplied = artworkReducer(next, {
      type: 'asset-unapply',
      asset: { id: 'new', work_type: 'illustration', scope: 'chapter', scope_id: 'one' }
    });
    expect(unapplied.assets.find((asset) => asset.id === 'new').applied).toBe(false);
    expect(unapplied.assets.find((asset) => asset.id === 'other').applied).toBe(true);
  });
});
