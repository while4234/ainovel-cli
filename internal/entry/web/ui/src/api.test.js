import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  renameProject,
  reviseCoCreate,
  sendCoCreate,
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
});
