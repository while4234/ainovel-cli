import { describe, expect, it } from 'vitest';
import {
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
