import { afterEach, describe, expect, it, vi } from 'vitest';
import { applyFoundation, foundationError, previewFoundation, retryFoundation } from './foundationApi.js';

afterEach(() => vi.unstubAllGlobals());

describe('foundation API adapter', () => {
  it('preview 只发送基线与完整 candidate', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ preview: { id: 'p' } }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const candidate = { schema_version: 1, premise: '目标', characters: [], relationships: [], world_rules: [] };
    await previewFoundation('project/a', { baseRevision: 7, baseAuditSignature: 'audit' }, candidate);
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/projects/project%2Fa/foundation/preview');
    expect(JSON.parse(options.body)).toEqual({ expected_base_revision: 7, expected_base_audit_signature: 'audit', candidate });
  });

  it('apply 只发送 preview ID 与传入的同一个 idempotency key', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ revision: {} }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await applyFoundation('p', 'preview-7', 'stable-key');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ preview_id: 'preview-7', idempotency_key: 'stable-key' });
  });

  it('retry 不提交 candidate 或 preview', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ revision: {} }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    await retryFoundation('p', 'retry-key');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ idempotency_key: 'retry-key' });
  });

  it('保留统一错误 envelope 的 code 与安全 message', () => {
    expect(foundationError({ data: { error: { code: 'foundation_stale', message: 'changed' } } })).toEqual({ code: 'foundation_stale', message: 'changed' });
  });
});
