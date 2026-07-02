import { describe, expect, it } from 'vitest';
import {
  applyAdaptationHostEvent,
  buildModelAddPayload,
  canEditProjectStyle,
  canSubmitModelAdd,
  firstAvailableStyle,
  modelAddModeDefaults,
  styleItemsFromResponse,
  styleLabelForID
} from './App.jsx';

describe('model add helpers', () => {
  it('builds a Grok OAuth provider config without API key fields', () => {
    const state = {
      mode: 'grok_oauth',
      role: 'writer',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      account_id: 'work',
      api_key: 'should-not-be-sent',
      base_url: 'https://example.invalid',
      grok_status: { logged_in: true }
    };

    expect(buildModelAddPayload(state, null)).toEqual({
      role: 'writer',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      type: 'grok',
      auth: 'grok_oauth',
      account_id: 'work'
    });
    expect(canSubmitModelAdd(state, null)).toBe(true);
  });

  it('requires a confirmed Grok login before adding the provider', () => {
    const state = {
      mode: 'grok_oauth',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      account_id: 'default',
      grok_status: { needs_reauth: true }
    };

    expect(canSubmitModelAdd(state, null)).toBe(false);
  });

  it('uses stable defaults when switching into Grok OAuth mode', () => {
    const state = modelAddModeDefaults({
      mode: 'grok_oauth',
      role: 'default',
      provider: 'openrouter',
      model: ''
    });

    expect(state.provider).toBe('grok-oauth');
    expect(state.type).toBe('grok');
    expect(state.auth).toBe('grok_oauth');
    expect(state.account_id).toBe('default');
    expect(state.model).toBe('grok-4.3-latest');
  });

  it('does not invent a placeholder model name for custom proxy mode', () => {
    const state = modelAddModeDefaults({
      mode: 'custom',
      role: 'default',
      provider: '',
      model: ''
    });

    expect(state.provider).toBe('custom-openai');
    expect(state.model).toBe('');
    expect(canSubmitModelAdd({ ...state, base_url: 'https://proxy.example/v1' }, null)).toBe(false);
  });
});

describe('adaptation event helpers', () => {
  it('keeps refreshed adaptation analysis running from ADAPT stream events', () => {
    const previous = {
      analysisStatus: 'paused',
      analysisEvents: [{ stage: 'paused', message: '原文分析未完成' }],
      error: 'stale'
    };

    const next = applyAdaptationHostEvent(previous, {
      type: 'host_event',
      event: {
        category: 'ADAPT',
        kind: 'chapter',
        summary: '分析原文第 49/140 章：第49章',
        time: '2026-07-02T08:10:13Z'
      }
    });

    expect(next.analysisStatus).toBe('running');
    expect(next.error).toBe('');
    expect(next.analysisEvents.at(-1)).toMatchObject({
      stage: 'chapter',
      message: '分析原文第 49/140 章：第49章'
    });
  });

  it('marks adaptation analysis as done or error from terminal ADAPT events', () => {
    const running = { analysisStatus: 'running', analysisEvents: [], error: '' };

    const done = applyAdaptationHostEvent(running, {
      type: 'host_event',
      event: { category: 'ADAPT', kind: 'done', summary: '原书分析完成：140 章快照已保存' }
    });
    const failed = applyAdaptationHostEvent(done, {
      type: 'host_event',
      event: { category: 'ADAPT', kind: 'error', level: 'error', detail: 'source reports incomplete' }
    });

    expect(done.analysisStatus).toBe('done');
    expect(failed.analysisStatus).toBe('error');
    expect(failed.error).toBe('source reports incomplete');
  });
});

describe('style helpers', () => {
  const styles = [
    { id: 'default', label: 'General style' },
    { id: 'fantasy', label: 'Fantasy adventure style' }
  ];

  it('normalizes style catalog responses for display', () => {
    expect(styleItemsFromResponse({
      styles: [
        { id: ' fantasy ', label: ' Fantasy adventure style ' },
        { id: 'plain', label: '' },
        { id: '', label: 'ignored' }
      ]
    })).toEqual([
      { id: 'fantasy', label: 'Fantasy adventure style' },
      { id: 'plain', label: 'plain' }
    ]);
  });

  it('uses stable fallback and label behavior for styles', () => {
    expect(firstAvailableStyle('fantasy', styles)).toBe('fantasy');
    expect(firstAvailableStyle('missing', styles, 'default')).toBe('default');
    expect(firstAvailableStyle('missing', [{ id: 'suspense', label: 'Suspense' }], 'default')).toBe('suspense');
    expect(styleLabelForID('fantasy', styles)).toBe('Fantasy adventure style');
    expect(styleLabelForID('unknown', styles)).toBe('unknown');
  });

  it('allows style editing only before a book has started', () => {
    expect(canEditProjectStyle({})).toBe(true);
    expect(canEditProjectStyle({ NovelName: 'Started book' })).toBe(false);
    expect(canEditProjectStyle({ TotalChapters: 12 })).toBe(false);
    expect(canEditProjectStyle(null)).toBe(false);
  });
});
