import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  clearProjectTrash,
  createProject,
  listProjectTrash,
  listStyles,
  listNovelLibrary,
  listSimulationLibrary,
  loadNovelFromLibrary,
  renameProject,
  saveNovelToLibrary,
  saveSimulationToLibrary,
  setProjectStyle,
  trashProject,
  uploadSimulationLibrary
} from './api.js';

function mockFetch(data = {}) {
  vi.stubGlobal('fetch', vi.fn(async () => ({
    ok: true,
    status: 200,
    statusText: 'OK',
    text: async () => JSON.stringify(data)
  })));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('library API helpers', () => {
  it('lists styles from the style catalog route', async () => {
    mockFetch({ styles: [{ id: 'fantasy', label: 'Fantasy' }] });

    await listStyles();

    expect(fetch.mock.calls[0][0]).toBe('/api/styles');
  });

  it('sends project style when creating and updating projects', async () => {
    mockFetch({ ok: true });

    await createProject('New Book', 'fantasy');
    await setProjectStyle('project 1', 'romance');

    expect(fetch.mock.calls[0][0]).toBe('/api/projects');
    expect(fetch.mock.calls[0][1].method).toBe('POST');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ name: 'New Book', style: 'fantasy' });
    expect(fetch.mock.calls[1][0]).toBe('/api/projects/project%201/style');
    expect(fetch.mock.calls[1][1].method).toBe('PUT');
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ style: 'romance' });
  });

  it('sends project rename, trash, and trash clearing requests', async () => {
    mockFetch({ ok: true });

    await renameProject('project 1', 'Renamed Book');
    await trashProject('project 1');
    await listProjectTrash();
    await clearProjectTrash();

    expect(fetch.mock.calls[0][0]).toBe('/api/projects/project%201');
    expect(fetch.mock.calls[0][1].method).toBe('PATCH');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ name: 'Renamed Book' });
    expect(fetch.mock.calls[1][0]).toBe('/api/projects/project%201');
    expect(fetch.mock.calls[1][1].method).toBe('DELETE');
    expect(fetch.mock.calls[2][0]).toBe('/api/projects/trash');
    expect(fetch.mock.calls[3][0]).toBe('/api/projects/trash');
    expect(fetch.mock.calls[3][1].method).toBe('DELETE');
  });

  it('encodes simulation and novel library search queries', async () => {
    mockFetch({ items: [] });

    await listSimulationLibrary(' 古风 画像 ');
    await listNovelLibrary(' 长篇 原文 ');

    expect(fetch.mock.calls[0][0]).toBe('/api/libraries/simulation?q=%E5%8F%A4%E9%A3%8E%20%E7%94%BB%E5%83%8F');
    expect(fetch.mock.calls[1][0]).toBe('/api/libraries/novels?q=%E9%95%BF%E7%AF%87%20%E5%8E%9F%E6%96%87');
  });

  it('sends project simulation save payloads to the simulate library route', async () => {
    mockFetch({ ok: true });

    await saveSimulationToLibrary('project 1', '克制冷峻');

    const [path, options] = fetch.mock.calls[0];
    expect(path).toBe('/api/projects/project%201/simulate/library/save');
    expect(options.method).toBe('POST');
    expect(JSON.parse(options.body)).toEqual({ name: '克制冷峻' });
  });

  it('sends analyzed novel save and load requests to adaptation library routes', async () => {
    mockFetch({ ok: true });

    await saveNovelToLibrary('project/1', '第一卷分析', 'uploads/adaptation/source.txt');
    await loadNovelFromLibrary('project/1', '第一卷分析');

    expect(fetch.mock.calls[0][0]).toBe('/api/projects/project%2F1/adapt/library/save');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      name: '第一卷分析',
      source_file: 'uploads/adaptation/source.txt'
    });
    expect(fetch.mock.calls[1][0]).toBe('/api/projects/project%2F1/adapt/library/load');
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ name: '第一卷分析' });
  });

  it('uploads simulation library JSON as multipart form data without forcing JSON headers', async () => {
    mockFetch({ items: [] });

    await uploadSimulationLibrary([new Blob(['{}'], { type: 'application/json' })]);

    const [path, options] = fetch.mock.calls[0];
    expect(path).toBe('/api/libraries/simulation/upload');
    expect(options.method).toBe('POST');
    expect(options.body).toBeInstanceOf(FormData);
    expect(options.headers).not.toHaveProperty('content-type');
  });
});
