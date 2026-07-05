import { describe, expect, it } from 'vitest';
import {
  buildExistingModelActionPayload,
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
      network_disconnect_max_attempts: 0,
      auto_switch_candidate_pool: false,
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

  it('hydrates existing provider configs for editing', () => {
    const state = modelAddModeDefaults({
      mode: 'existing',
      role: 'default'
    }, [
      {
        name: 'openai',
        label: 'OpenAI',
        template_provider: 'openai',
        type: 'openai',
        api: 'responses',
        base_url: 'https://api.openai.com/v1',
        use_proxy: true,
        request_timeout_seconds: 90,
        connectivity_timeout_seconds: 8,
        network_disconnect_max_attempts: 4,
        auto_switch_candidate_pool: true,
        models: ['gpt-5.1']
      }
    ]);

    expect(state).toMatchObject({
      mode: 'existing',
      original_provider: 'openai',
      provider: 'openai',
      label: 'OpenAI',
      template_provider: 'openai',
      type: 'openai',
      api: 'responses',
      base_url: 'https://api.openai.com/v1',
      model: 'gpt-5.1',
      use_proxy: true,
      request_timeout_seconds: '90',
      connectivity_timeout_seconds: '8',
      network_disconnect_max_attempts: '4',
      auto_switch_candidate_pool: true,
      api_key: ''
    });
  });

  it('builds editable existing provider payloads without empty API keys', () => {
    const payload = buildModelAddPayload({
      mode: 'existing',
      role: 'writer',
      original_provider: 'openai',
      provider: 'openai-proxy',
      label: 'OpenAI Proxy',
      template_provider: 'openai',
      type: 'openai',
      api: 'responses',
      auth: 'api_key',
      model: 'gpt-5.1',
      base_url: 'https://proxy.example/v1',
      api_key: '',
      use_proxy: true,
      request_timeout_seconds: '90',
      connectivity_timeout_seconds: '8',
      network_disconnect_max_attempts: '4',
      auto_switch_candidate_pool: true
    }, null);

    expect(payload).toEqual({
      role: 'writer',
      original_provider: 'openai',
      provider: 'openai-proxy',
      model: 'gpt-5.1',
      label: 'OpenAI Proxy',
      template_provider: 'openai',
      use_proxy: true,
      request_timeout_seconds: 90,
      connectivity_timeout_seconds: 8,
      network_disconnect_max_attempts: 4,
      auto_switch_candidate_pool: true,
      type: 'openai',
      auth: 'api_key',
      api: 'responses',
      base_url: 'https://proxy.example/v1'
    });
    expect(payload).not.toHaveProperty('api_key');
    expect(canSubmitModelAdd({ ...payload, mode: 'existing' }, null)).toBe(true);
  });

  it('sends an existing provider API key only when the user enters one', () => {
    expect(buildModelAddPayload({
      mode: 'existing',
      original_provider: 'openai',
      provider: 'openai',
      model: 'gpt-5.1',
      api_key: 'sk-new'
    }, null)).toMatchObject({
      original_provider: 'openai',
      provider: 'openai',
      model: 'gpt-5.1',
      api_key: 'sk-new'
    });
  });

  it('keeps the current default model visible even when it is not listed', () => {
    expect(modelOptionsForProvider([
      { name: 'openai', models: ['gpt-a', 'gpt-b'] }
    ], 'openai', 'gpt-custom')).toEqual(['gpt-custom', 'gpt-a', 'gpt-b']);
  });

  it('builds a minimal payload for testing configured models', () => {
    expect(buildExistingModelActionPayload('writer', 'grok-oauth', 'grok-4.3-latest')).toEqual({
      role: 'writer',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest'
    });
  });
});
