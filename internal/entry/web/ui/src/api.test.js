import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  listNovelLibrary,
  listSimulationLibrary,
  loadNovelFromLibrary,
  saveNovelToLibrary,
  saveSimulationToLibrary,
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
