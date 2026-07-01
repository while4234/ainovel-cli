import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  getGlobalModels,
  renameProject,
  reviseCoCreate,
  sendCoCreate,
  startGrokLogin,
  switchGlobalDefaultModel,
  trashProject
} from './api.js';

function mockJSONResponse(body = {}) {
  return {
    ok: true,
    text: () => Promise.resolve(JSON.stringify(body))
  };
}

describe('web API helpers', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('sends project rename and trash requests to the project resource', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await renameProject('project-1', 'Renamed');
    await trashProject('project-1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ name: 'Renamed' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1', expect.objectContaining({
      method: 'DELETE'
    }));
  });

  it('sends co-create source and revise payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await sendCoCreate('project-1', 'Use the heroine arc', 'suggestion');
    await reviseCoCreate('project-1', 'm3', 'Keep a slower burn');

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      text: 'Use the heroine arc',
      source: 'suggestion'
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/project-1/cocreate/send');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      message_id: 'm3',
      text: 'Keep a slower burn'
    });
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/project-1/cocreate/revise');
  });

  it('can ask the backend to open the Grok authorization page', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await startGrokLogin('project-1', 'work', 'Work', true);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/models/grok-login/start', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        account_id: 'work',
        account_name: 'Work',
        open_browser: true
      })
    }));
  });

  it('uses global Grok login routes when no project is active', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await startGrokLogin('', 'default', 'Default', true);

    expect(fetchMock).toHaveBeenCalledWith('/api/models/grok-login/start', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        account_id: 'default',
        account_name: 'Default',
        open_browser: true
      })
    }));
  });

  it('uses global model routes for default model controls', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getGlobalModels();
    await switchGlobalDefaultModel('openai', 'gpt-next');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/models/default', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        provider: 'openai',
        model: 'gpt-next'
      })
    }));
  });
});
