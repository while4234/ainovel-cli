import { describe, expect, it } from 'vitest';
import {
  buildModelAddPayload,
  canSubmitModelAdd,
  modelAddModeDefaults,
  modelOptionsForProvider
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
      label: 'Grok',
      template_provider: 'grok',
      use_proxy: true,
      request_timeout_seconds: 0,
      connectivity_timeout_seconds: 0,
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
    expect(state.use_proxy).toBe(true);
  });

  it('defaults Codex to proxy and DeepSeek to direct access', () => {
    const codex = modelAddModeDefaults({
      mode: 'preset',
      preset: 'codex',
      request_timeout_seconds: '120',
      connectivity_timeout_seconds: '12'
    });
    const deepseek = modelAddModeDefaults({
      mode: 'preset',
      preset: 'deepseek',
      request_timeout_seconds: '120',
      connectivity_timeout_seconds: '12'
    });

    expect(buildModelAddPayload(codex, null)).toMatchObject({
      provider: 'codex',
      template_provider: 'codex',
      api: 'responses',
      use_proxy: true,
      request_timeout_seconds: 120,
      connectivity_timeout_seconds: 12
    });
    expect(buildModelAddPayload(deepseek, null)).toMatchObject({
      provider: 'deepseek',
      template_provider: 'deepseek',
      use_proxy: false
    });
  });

  it('keeps the current default model visible even when it is not listed', () => {
    expect(modelOptionsForProvider([
      { name: 'openai', models: ['gpt-a', 'gpt-b'] }
    ], 'openai', 'gpt-custom')).toEqual(['gpt-custom', 'gpt-a', 'gpt-b']);
  });
});
