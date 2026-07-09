import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  addGlobalProviderModel,
  buildAdaptationProposal,
  clearProjectTrash,
  confirmAdaptationProposal,
  confirmAdaptationProposalDetails,
  createProject,
  deleteGlobalProviderModel,
  deleteProviderModel,
  discoverGlobalProviderModels,
  discoverProjectProviderModels,
  emptyTrashProjects,
  exportProjectDownload,
  getChapter,
  getCodexAuthStatus,
  getGlobalModels,
  getProjectEvents,
  inheritProjectModel,
  listNovelLibrary,
  listProjectTrash,
  listSimulationLibrary,
  listStyles,
  listTrashProjects,
  previewProjectRollback,
  renameProject,
  restoreTrashProject,
  resumeCoCreate,
  reviseAdaptationProposal,
  reviseAdaptationVolumeReview,
  reviseChapter,
  reviseCoCreatePlanning,
  reviseCoCreate,
  resolveCoCreateDecision,
  resolveCoCreateDecisions,
  rollbackProject,
  saveNovelToLibrary,
  sendCoCreate,
  setGlobalCoCreateMaxTokens,
  setGlobalCoCreateTimeout,
  setGlobalRetrySettings,
  setProjectCoCreateMaxTokens,
  setProjectCoCreateTimeout,
  setProjectStyle,
  startGrokLogin,
  switchGlobalDefaultModel,
  switchGlobalModel,
  switchProjectModel,
  testGlobalProviderModel,
  testProjectProviderModel,
  trashProject
} from './api.js';

function mockJSONResponse(body = {}) {
  return {
    ok: true,
    text: () => Promise.resolve(JSON.stringify(body))
  };
}

function mockBlobResponse(body = 'book') {
  return {
    ok: true,
    headers: new Headers({
      'x-ainovel-export-name': 'book.txt',
      'x-ainovel-export-chapters': '59',
      'x-ainovel-export-bytes': '893100',
      'x-ainovel-export-skipped': '2,4'
    }),
    blob: () => Promise.resolve(new Blob([body], { type: 'text/plain' }))
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

  it('uses rollback preview and irreversible confirm routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await previewProjectRollback('project-1');
    await rollbackProject('project-1', { confirm: true, preview_hash: 'hash-1' });

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/rollback/preview', expect.objectContaining({
      headers: {}
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/rollback', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ confirm: true, preview_hash: 'hash-1' })
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
    await sendCoCreate('project-1', 'Re-scan this direction', 'custom', { forceRebrief: true });
    await reviseCoCreate('project-1', 'm3', 'Keep a slower burn');
    await resolveCoCreateDecision('project-1', 'q1', 'a', '');
    await resolveCoCreateDecisions('project-1', [{ decision_id: 'q2', option_id: 'b', custom_answer: '' }]);
    await resumeCoCreate('project-1');

    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      text: 'Use the heroine arc',
      source: 'suggestion'
    });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/project-1/cocreate/send');
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      text: 'Re-scan this direction',
      source: 'custom',
      force_rebrief: true
    });
    expect(fetchMock.mock.calls[1][0]).toBe('/api/projects/project-1/cocreate/send');
    expect(JSON.parse(fetchMock.mock.calls[2][1].body)).toEqual({
      message_id: 'm3',
      text: 'Keep a slower burn'
    });
    expect(fetchMock.mock.calls[2][0]).toBe('/api/projects/project-1/cocreate/revise');
    expect(JSON.parse(fetchMock.mock.calls[3][1].body)).toEqual({
      decision_id: 'q1',
      option_id: 'a',
      custom_answer: ''
    });
    expect(fetchMock.mock.calls[3][0]).toBe('/api/projects/project-1/cocreate/decision');
    expect(JSON.parse(fetchMock.mock.calls[4][1].body)).toEqual({
      decisions: [{ decision_id: 'q2', option_id: 'b', custom_answer: '' }]
    });
    expect(fetchMock.mock.calls[4][0]).toBe('/api/projects/project-1/cocreate/decision');
    expect(JSON.parse(fetchMock.mock.calls[5][1].body)).toEqual({});
    expect(fetchMock.mock.calls[5][0]).toBe('/api/projects/project-1/cocreate/resume');
  });

  it('sends completed chapter revision payloads', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await reviseChapter('project-1', {
      chapter: 3,
      mode: 'polish',
      instruction: 'tighten the ending'
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/chapters/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        chapter: 3,
        mode: 'polish',
        instruction: 'tighten the ending'
      })
    }));
  });

  it('fetches completed chapter content', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ chapter: { chapter: 3 } }));

    await getChapter('project-1', 3);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/chapters/3', expect.objectContaining({}));
  });

  it('sends novel library replace requests explicitly', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await saveNovelToLibrary('project-1', 'Source Book', 'source.txt', { replace: true });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/adapt/library/save', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'Source Book', source_file: 'source.txt', replace: true })
    }));
  });

  it('fetches project event history with an after cursor', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ events: [] }));

    await getProjectEvents('project 1', 7);

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project%201/events/history?after=7', expect.objectContaining({
      headers: {}
    }));
  });

  it('downloads exported novel blobs with metadata', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockBlobResponse('novel body'));

    const result = await exportProjectDownload('project-1', {
      path: 'book.txt',
      format: 'txt',
      from: 1,
      to: 59
    });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/export/download', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        path: 'book.txt',
        format: 'txt',
        from: 1,
        to: 59
      })
    }));
    expect(result.export).toMatchObject({
      name: 'book.txt',
      chapters: 59,
      bytes: 893100,
      skipped: [2, 4]
    });
    expect(await result.blob.text()).toBe('novel body');
  });

  it('uses staged adaptation proposal, revise, details, and final confirm routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await buildAdaptationProposal('project-1', 'source.txt', 'free', 'Make it a mystery');
    await reviseAdaptationVolumeReview('project-1', { volume_index: 3, instruction: 'raise tension' });
    await confirmAdaptationProposalDetails('project-1');
    await reviseAdaptationProposal('project-1', { from_chapter: 4, to_chapter: 6, instruction: 'tighten the reveal' });
    await confirmAdaptationProposal('project-1');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/adapt/proposal/volumes', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        source_file: 'source.txt',
        mode: 'free',
        brief: 'Make it a mystery'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/adapt/proposal/volumes/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        volume_index: 3,
        instruction: 'raise tension'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/projects/project-1/adapt/proposal/details', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({})
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/adapt/proposal/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        from_chapter: 4,
        to_chapter: 6,
        instruction: 'tighten the reveal'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/projects/project-1/adapt/confirm', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({})
    }));
  });

  it('uses the normal co-create planning revision route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await reviseCoCreatePlanning('project-1', { feedback: 'tighten the opening' });

    expect(fetchMock).toHaveBeenCalledWith('/api/projects/project-1/cocreate/planning/revise', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        feedback: 'tighten the opening'
      })
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

  it('checks Codex auth status through global and project routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await getCodexAuthStatus('', '');
    await getCodexAuthStatus('project-1', 'D:/codex/auth.json');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/codex-auth/status', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ auth_file: '' })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/codex-auth/status', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ auth_file: 'D:/codex/auth.json' })
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

  it('switches project model routes and clears project role overrides', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await switchProjectModel('project-1', 'writer', 'deepseek', 'deepseek-chat');
    await inheritProjectModel('project-1', 'writer');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/projects/project-1/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        provider: 'deepseek',
        model: 'deepseek-chat'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/switch', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        role: 'writer',
        inherit: true
      })
    }));
  });

  it('sends co-create generation setting updates to global and project model routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await setGlobalCoCreateTimeout(60);
    await setProjectCoCreateTimeout('project-1', 30);
    await setGlobalCoCreateMaxTokens(8192);
    await setProjectCoCreateMaxTokens('project-1', 12288);

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/cocreate-timeout', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ seconds: 60 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/cocreate-timeout', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ seconds: 30 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/models/cocreate-max-tokens', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ tokens: 8192 })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/models/cocreate-max-tokens', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ tokens: 12288 })
    }));
  });

  it('adds provider models through the global route when no project is active', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await addGlobalProviderModel({
      select_after_save: false,
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
        select_after_save: false,
        role: 'default',
        provider: 'grok-oauth',
        model: 'grok-4.3-latest',
        type: 'grok',
        auth: 'grok_oauth',
        account_id: 'default'
      })
    }));
  });

  it('sends retry settings to the global route', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await setGlobalRetrySettings(14, 8, 3);

    expect(fetchMock).toHaveBeenCalledWith('/api/models/retry-settings', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        model_call_max_attempts: 14,
        structure_repair_max_attempts: 8,
        budget_quality_max_attempts: 3
      })
    }));
  });

  it('tests and discovers provider models through global and project routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));
    const payload = {
      role: 'default',
      provider: 'codex',
      model: 'gpt-5.1-codex',
      type: 'openai',
      api: 'responses',
      use_proxy: true
    };

    await testGlobalProviderModel(payload);
    await testProjectProviderModel('project-1', payload);
    await discoverGlobalProviderModels(payload);
    await discoverProjectProviderModels('project-1', payload);

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models/test', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models/test', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/models/discover', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/projects/project-1/models/discover', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(payload)
    }));
  });

  it('deletes provider models through global and project model routes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(mockJSONResponse({ ok: true }));

    await deleteGlobalProviderModel('proxy', 'proxy-model');
    await deleteProviderModel('project-1', 'proxy', 'proxy-model');

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/models', expect.objectContaining({
      method: 'DELETE',
      body: JSON.stringify({
        provider: 'proxy',
        model: 'proxy-model'
      })
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/projects/project-1/models', expect.objectContaining({
      method: 'DELETE',
      body: JSON.stringify({
        provider: 'proxy',
        model: 'proxy-model'
      })
    }));
  });
});
