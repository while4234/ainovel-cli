// @vitest-environment jsdom
import React from 'react';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ArtworkWorkspace } from './ArtworkWorkspace.jsx';
import * as api from './artwork-api.js';

vi.mock('./artwork-api.js');

const registry = {
  version: 'test/v1',
  models: [
    { id: 'a2e', label: 'A2E', enabled: true, verified: true, supported_sizes: [{ value: '1080x1620', label: '2:3', aspect_ratio: '2:3' }, { value: '1920x1080', label: '16:9', aspect_ratio: '16:9' }] },
    { id: 'unverified', label: '候选模型', enabled: true, verified: false, supported_sizes: [{ value: '1280x720', label: '16:9', aspect_ratio: '16:9' }] }
  ]
};
const gateway = { config: { base_url: 'http://127.0.0.1:4180/v1', default_model: 'a2e', request_timeout_seconds: 60, has_api_key: true } };
const catalog = { volumes: [{ id: 'v1', label: '第一卷' }], chapters: [{ id: 'c1', label: '第一章', volumeId: 'v1' }], characters: [{ id: 'hero', label: '林舟' }] };
const draft = (patch = {}) => ({
  id: 'd1', version: 1, work_type: 'cover', scope: 'project', scope_id: '', prompt: '雾港中的孤灯', prompt_source: 'manual',
  model_id: 'a2e', size: '1080x1620', source_status: 'not_applicable', is_stale: false, ...patch
});
const portrait = draft({ id: 'd2', work_type: 'character_portrait', scope: 'character', scope_id: 'hero', prompt: '林舟肖像' });
const asset = (patch = {}) => ({
  id: 'a1', work_type: 'cover', prompt: '雾港中的孤灯', prompt_source: 'manual', file_name: 'cover.png', content_url: '/api/projects/p1/artwork/assets/a1/content', download_url: '/api/projects/p1/artwork/assets/a1/download', request: { model_id: 'a2e', size: '1080x1620' }, applied: false, ...patch
});
const workspace = (patch = {}) => ({
  drafts: { items: [draft(), portrait], next_cursor: 'draft-next' },
  prompt_jobs: { items: [] },
  jobs: { items: [] },
  assets: { items: [asset(), asset({ id: 'a2', applied: true, content_url: '/api/projects/p1/artwork/assets/a2/content' })], next_cursor: 'asset-next' },
  applied: [{ target: 'cover:project:', asset_id: 'a2' }],
  ...patch
});

let root;
let container;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  window.history.replaceState({}, '', '/?projectId=p1&view=story&draft=d1');
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  api.loadArtworkRegistry.mockResolvedValue(registry);
  api.loadArtworkGatewayConfig.mockResolvedValue(gateway);
  api.loadArtworkWorkspace.mockResolvedValue(workspace());
  api.loadArtworkScopeCatalog.mockResolvedValue(catalog);
  api.getArtworkDraft.mockImplementation(async (_project, id) => ({ draft: id === 'd2' ? portrait : draft() }));
  api.updateArtworkDraft.mockImplementation(async (_project, _id, payload) => ({ draft: draft({ ...payload, version: payload.expected_version + 1 }) }));
  api.confirmArtworkStalePrompt.mockResolvedValue({ draft: draft({ version: 2, prompt_source: 'ai', source_status: 'current' }) });
  api.generateArtworkImage.mockResolvedValue({ job: { id: 'j-new', status: 'queued' } });
  api.generateArtworkPrompt.mockResolvedValue({ draft: draft({ version: 2, prompt_source: 'ai', prompt: 'AI 雾港提示词' }), job: { id: 'p1', status: 'succeeded' } });
  api.listArtworkDrafts.mockResolvedValue({ items: [draft({ id: 'd3', prompt: '第二页' })], next_cursor: '' });
  api.listArtworkAssets.mockResolvedValue({ items: [asset({ id: 'a3' })], next_cursor: '' });
  api.downloadArtworkAsset.mockResolvedValue({ fileName: 'cover.png' });
  api.reuseArtworkAsset.mockResolvedValue({ draft: draft({ id: 'd-reuse', version: 1, prompt_source: 'reuse' }), reused: false });
  api.applyArtworkAsset.mockImplementation(async (_project, id) => ({ asset: asset({ id, applied: true }), applied_to: 'book-cover' }));
  api.deleteArtworkAsset.mockResolvedValue(null);
  api.deleteArtworkDraft.mockResolvedValue(null);
  api.saveArtworkGatewayConfig.mockResolvedValue(gateway);
  api.verifyArtworkGatewayConfig.mockResolvedValue({ verified: true, model_count: 2, config: gateway.config });
  api.createArtworkDraft.mockResolvedValue({ draft: draft({ id: 'd-new' }) });
  api.newArtworkIdempotencyKey.mockImplementation((scope) => `${scope}-key`);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.clearAllMocks();
  vi.useRealTimers();
});

async function renderWorkspace(props = {}) {
  await act(async () => root.render(<ArtworkWorkspace
    modelConfig={{ providers: [{ name: 'text', label: '文本', models: ['writer'] }], roles: [{ role: 'default', provider: 'text', model: 'writer' }] }}
    onDirtyChange={vi.fn()}
    onPromptModelChange={vi.fn()}
    projectId="p1"
    projectName="雾港纪事"
    runtime={{ config: { provider: 'text', model: 'writer' } }}
    {...props}
  />));
  await settle();
}

async function settle() {
  await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
}

function button(label) {
  return [...container.querySelectorAll('button')].find((item) => item.textContent.includes(label));
}

function inputValue(input, value) {
  const setter = Object.getOwnPropertyDescriptor(globalThis.HTMLInputElement.prototype, 'value').set;
  setter.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

describe('ArtworkWorkspace', () => {
  it('restores URL state, exposes only valid scopes, and switches story/character drafts', async () => {
    await renderWorkspace();
    expect(container.textContent).toContain('绘境 · Visual Studio');
    expect(container.querySelector('[aria-label="作品类型"]').value).toBe('cover');
    expect(container.querySelector('[aria-label="封面范围"]').value).toBe('整本书');
    expect(container.textContent).not.toContain('分镜');
    await act(async () => button('角色肖像').click());
    await settle();
    expect(container.querySelector('[aria-label="作品类型"]').value).toBe('character_portrait');
    expect(container.querySelector('[aria-label="肖像角色"]').value).toBe('hero');
    expect(new URLSearchParams(window.location.search).get('draft')).toBe('d2');
    expect(new URLSearchParams(window.location.search).get('view')).toBe('character');
  });

  it('requires separate stale and potentially-paid confirmations without hidden generation', async () => {
    api.getArtworkDraft.mockResolvedValue({ draft: draft({ prompt_source: 'ai', is_stale: true, source_status: 'stale', current_source_signature: 'current-digest' }) });
    api.loadArtworkWorkspace.mockResolvedValue(workspace({ drafts: { items: [draft({ prompt_source: 'ai', is_stale: true })] } }));
    await renderWorkspace();
    expect(container.textContent).toContain('来源内容已变化');
    await act(async () => button('查看并确认').click());
    expect(api.confirmArtworkStalePrompt).not.toHaveBeenCalled();
    const staleDialog = container.querySelector('[role="alertdialog"]');
    await act(async () => [...staleDialog.querySelectorAll('button')].find((item) => item.textContent.includes('确认使用当前来源')).click());
    await settle();
    expect(api.confirmArtworkStalePrompt).toHaveBeenCalledWith('p1', 'd1', { expected_version: 1, source_signature: 'current-digest' });
    expect(api.generateArtworkImage).not.toHaveBeenCalled();

    await act(async () => button('生成 1 张图片').click());
    expect(container.textContent).toContain('可能产生费用');
    expect(api.generateArtworkImage).not.toHaveBeenCalled();
    await act(async () => button('确认并生成 1 张').click());
    await settle();
    expect(api.generateArtworkImage).toHaveBeenCalledTimes(1);
    expect(api.generateArtworkImage.mock.calls[0][2]).toMatchObject({ expected_version: 2, idempotency_key: 'image-key' });
  });

  it('supports cursor loading, Blob download, immutable reuse, apply, and protected delete', async () => {
    await renderWorkspace();
    await act(async () => button('加载更多草稿').click()); await settle();
    await act(async () => button('加载更多图片').click()); await settle();
    expect(api.listArtworkDrafts).toHaveBeenCalledWith('p1', { cursor: 'draft-next' });
    expect(api.listArtworkAssets).toHaveBeenCalledWith('p1', { cursor: 'asset-next' });

    const firstCard = container.querySelector('.artwork-gallery-grid article');
    await act(async () => firstCard.querySelector('[aria-label="下载图片"]').click()); await settle();
    expect(api.downloadArtworkAsset).toHaveBeenCalledWith(expect.objectContaining({ id: 'a1' }));
    await act(async () => firstCard.querySelector('[aria-label="复用图片参数"]').click()); await settle();
    expect(api.reuseArtworkAsset).toHaveBeenCalledWith('p1', 'a1', { idempotency_key: 'reuse-key' });

    const reusedFirstCard = container.querySelector('.artwork-gallery-grid article');
    await act(async () => reusedFirstCard.querySelector('[aria-label="应用图片"]').click()); await settle();
    expect(api.applyArtworkAsset).toHaveBeenCalledWith('p1', 'a1');
    expect(reusedFirstCard.querySelector('[aria-label="删除图片"]').disabled).toBe(true);
    expect(container.querySelectorAll('[aria-label="删除图片"]')[1].disabled).toBe(false);
  });

  it('deletes only a non-applied asset after explicit confirmation', async () => {
    await renderWorkspace();
    const deleteButton = container.querySelector('.artwork-gallery-grid article [aria-label="删除图片"]');
    await act(async () => deleteButton.click());
    expect(api.deleteArtworkAsset).not.toHaveBeenCalled();
    await act(async () => button('确认删除图片').click()); await settle();
    expect(api.deleteArtworkAsset).toHaveBeenCalledWith('p1', 'a1');
    expect(container.querySelector('.artwork-gallery-grid article [aria-label="删除图片"]')).not.toBe(deleteButton);
  });

  it('polls the aggregate workspace only while active and refreshes again on a new terminal state', async () => {
    vi.useFakeTimers();
    const running = workspace({ jobs: { items: [{ id: 'j1', status: 'running', work_type: 'cover' }] } });
    const terminal = workspace({ jobs: { items: [{ id: 'j1', status: 'succeeded', work_type: 'cover' }] } });
    api.loadArtworkWorkspace.mockReset().mockResolvedValueOnce(running).mockResolvedValueOnce(terminal).mockResolvedValueOnce(terminal);
    await renderWorkspace();
    expect(api.loadArtworkWorkspace).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    await settle();
    expect(api.loadArtworkWorkspace).toHaveBeenCalledTimes(3);
    expect(container.textContent).toContain('图片 · 已完成');
    await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
    expect(api.loadArtworkWorkspace).toHaveBeenCalledTimes(3);
  });

  it('keeps gateway secrets write-only and makes verification explicitly non-generating', async () => {
    await renderWorkspace();
    const key = container.querySelector('[aria-label="AI2API API Key"]');
    expect(key.value).toBe('');
    expect(key.placeholder).toContain('留空将保持不变');
    await act(async () => inputValue(key, 'fixture-key'));
    await act(async () => button('验证并发现').click()); await settle();
    expect(api.verifyArtworkGatewayConfig).toHaveBeenCalledWith(expect.objectContaining({ api_key: 'fixture-key' }));
    expect(api.generateArtworkImage).not.toHaveBeenCalled();
    expect(container.textContent).toContain('未生成图片');
    expect(container.textContent).not.toContain('fixture-key');
  });
});
