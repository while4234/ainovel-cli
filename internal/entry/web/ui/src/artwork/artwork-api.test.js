import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createArtworkDraft,
  downloadArtworkAsset,
  generateArtworkImage,
  listArtworkAssets,
  loadArtworkScopeCatalog,
  saveArtworkGatewayConfig,
  updateArtworkDraft
} from './artwork-api.js';

afterEach(() => vi.unstubAllGlobals());

function jsonResponse(body = {}, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
}

describe('artwork API helpers', () => {
  it('encodes project paths and preserves explicit mutation payloads', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse({ draft: { id: 'd1' } }));
    vi.stubGlobal('fetch', fetchMock);
    await createArtworkDraft('project / one', { work_type: 'cover', idempotency_key: 'create-1' });
    await updateArtworkDraft('project / one', 'draft / one', { expected_version: 1, prompt: 'fog' });
    await generateArtworkImage('project / one', 'draft / one', { expected_version: 2, idempotency_key: 'image-1' });
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      '/api/projects/project%20%2F%20one/artwork/drafts',
      '/api/projects/project%20%2F%20one/artwork/drafts/draft%20%2F%20one',
      '/api/projects/project%20%2F%20one/artwork/drafts/draft%20%2F%20one/generate-image'
    ]);
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ expected_version: 1, prompt: 'fog' });
  });

  it('keeps API keys write-only and sends clear only when requested', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse({ config: { has_api_key: true } }));
    vi.stubGlobal('fetch', fetchMock);
    const result = await saveArtworkGatewayConfig({ base_url: 'http://127.0.0.1:9000/v1', clear_api_key: true });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ base_url: 'http://127.0.0.1:9000/v1', clear_api_key: true });
    expect(result).not.toHaveProperty('api_key');
    expect(result.config).not.toHaveProperty('api_key');
  });

  it('uses cursor pagination and tolerates missing optional scope catalogs', async () => {
    const fetchMock = vi.fn(async (url) => {
      if (String(url).includes('/foundation')) return jsonResponse({ foundation: { target_foundation: { characters: [{ id: 'hero', name: '林舟' }] } } });
      if (String(url).includes('/manuscript/')) return jsonResponse({ error: { code: 'not_ready' } }, 409);
      return jsonResponse({ items: [], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetchMock);
    await listArtworkAssets('p1', { cursor: 'opaque+/=', limit: 20 });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/projects/p1/artwork/assets?limit=20&cursor=opaque%2B%2F%3D');
    expect(await loadArtworkScopeCatalog('p1')).toEqual({ volumes: [], chapters: [], characters: [{ id: 'hero', label: '林舟' }] });
  });

  it('downloads through fetch Blob instead of navigating to a host alias', async () => {
    const click = vi.fn();
    const link = { hidden: false, click, remove: vi.fn() };
    const dependencies = {
      fetch: vi.fn().mockResolvedValue(new Response(new Blob(['image']), { status: 200 })),
      document: { createElement: vi.fn(() => link), body: { appendChild: vi.fn() } },
      URL: { createObjectURL: vi.fn(() => 'blob:local'), revokeObjectURL: vi.fn() }
    };
    const result = await downloadArtworkAsset({ id: 'a1', file_name: 'cover.png', download_url: '/api/projects/p/artwork/assets/a1/download' }, dependencies);
    expect(dependencies.fetch).toHaveBeenCalledWith('/api/projects/p/artwork/assets/a1/download', { credentials: 'same-origin' });
    expect(result.fileName).toBe('cover.png');
    expect(link.href).toBe('blob:local');
    expect(click).toHaveBeenCalledOnce();
  });
});
