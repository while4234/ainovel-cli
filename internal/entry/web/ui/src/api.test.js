import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  addGlobalProviderModel,
  buildAdaptationProposal,
  clearProjectTrash,
  confirmAdaptationProposal,
  createProject,
  emptyTrashProjects,
  getGlobalModels,
  listNovelLibrary,
  listProjectTrash,
  listSimulationLibrary,
  listStyles,
  listTrashProjects,
  renameProject,
  restoreTrashProject,
  reviseCoCreate,
  sendCoCreate,
  setProjectStyle,
  startGrokLogin,
  switchGlobalDefaultModel,
  switchGlobalModel,
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
    await listTrashProjects();
    await restoreTrashProject('project-1');
    await emptyTrashProjects();

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ name: 'Renamed' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1', expect.objectContaining({
      method: 'DELETE'
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/trash/projects', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/trash/projects/project-1/restore', expect.objectContaining({
      method: 'POST'
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/trash/projects', expect.objectContaining({
      method: 'DELETE'
    }));
  });

  it('keeps legacy style and trash helper routes available', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await listStyles();
    await createProject('New Book', 'fantasy');
    await setProjectStyle('project-1', 'romance');
    await listProjectTrash();
    await clearProjectTrash();

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/styles', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'New Book', style: 'fantasy' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1/style', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ style: 'romance' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/trash', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/projects/trash', expect.objectContaining({
      method: 'DELETE'
    }));
  });

  it('encodes library search queries', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ items: [] }));

    await listSimulationLibrary('cool profile');
    await listNovelLibrary('source book');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/libraries/simulation?q=cool%20profile', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/libraries/novels?q=source%20book', expect.objectContaining({
      headers: {}
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

  it('uses explicit adaptation proposal and confirm routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await buildAdaptationProposal('project-1', 'source.txt', 'free', 'Make it a mystery');
    await confirmAdaptationProposal('project-1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/adapt/proposal', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        source_file: 'source.txt',
        mode: 'free',
        brief: 'Make it a mystery'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/adapt/confirm', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({})
    }));
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

  it('uses the legacy global model switch route for role controls', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await switchGlobalModel('writer', 'deepseek', 'deepseek-chat');

    expect(fetchMock).toHaveBeenCalledWith('/api/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        provider: 'deepseek',
        model: 'deepseek-chat'
      })
    }));
  });

  it('adds provider models through the global route when no project is active', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await addGlobalProviderModel({
      role: 'default',
      provider: 'grok-oauth',
      model: 'grok-4.3-latest',
      type: 'grok',
      auth: 'grok_oauth',
      account_id: 'default'
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/models/add', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'default',
        provider: 'grok-oauth',
        model: 'grok-4.3-latest',
        type: 'grok',
        auth: 'grok_oauth',
        account_id: 'default'
      })
    }));
  });
});
