import fs from 'node:fs/promises';
import path from 'node:path';
import { expect, test } from '@playwright/test';

const screenshotDir = path.resolve(process.cwd(), '../../../../output/playwright/artwork-pr05');

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop', 'Exact PR-05 viewports are exercised from one isolated Chrome project.');
  await page.request.post('/api/test/reset');
  await page.goto('/browser-fixture.html?surface=artwork&projectId=artwork-browser-project&view=story&draft=art-draft-cover');
  await expect(page.getByRole('heading', { name: '绘境 · Visual Studio' })).toBeVisible();
  await expect(page.getByLabel('图片提示词')).toHaveValue(/雾港黄昏/);
  await expect(page.locator('.artwork-gallery-grid img').first()).toBeVisible();
});

test('explicit fake-only prompt, verify, one-image, download, reuse, and apply flows', async ({ page }) => {
  const externalRequests = [];
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (!['127.0.0.1', 'localhost'].includes(url.hostname)) externalRequests.push(request.url());
  });

  await expect(page.locator('body')).not.toContainText('fixture-secret');
  const verifyRequest = page.waitForRequest((request) => request.url().endsWith('/api/artwork/config/verify'));
  await page.getByRole('button', { name: '验证并发现' }).click();
  expect(new URL((await verifyRequest).url()).hostname).toBe('127.0.0.1');
  await expect(page.getByText(/验证通过.*未生成图片/)).toBeVisible();

  const promptRequest = page.waitForRequest((request) => request.url().endsWith('/generate-prompt'));
  await page.getByRole('button', { name: /AI 草拟提示词/ }).click();
  expect(new URL((await promptRequest).url()).hostname).toBe('127.0.0.1');
  await expect(page.getByLabel('图片提示词')).toHaveValue(/电影感小说封面/);

  await page.getByRole('button', { name: '生成 1 张图片' }).click();
  await expect(page.getByRole('alertdialog', { name: '确认一次可能付费的图片生成' })).toBeVisible();
  expect((await page.request.get('/api/test/artwork/last-request')).json()).resolves.toMatchObject({ action: 'generate-prompt' });
  const imageRequest = page.waitForRequest((request) => request.url().endsWith('/generate-image'));
  await page.getByRole('button', { name: '确认并生成 1 张' }).click();
  expect(new URL((await imageRequest).url()).hostname).toBe('127.0.0.1');
  const last = await (await page.request.get('/api/test/artwork/last-request')).json();
  expect(last).toEqual(expect.objectContaining({ action: 'generate-image', count: 1, body: expect.objectContaining({ idempotency_key: '<present>' }) }));

  const firstCard = page.locator('.artwork-gallery-grid article').filter({ hasText: '书籍封面' }).first();
  const downloadEvent = page.waitForEvent('download');
  await firstCard.getByRole('button', { name: '下载图片' }).click();
  expect((await downloadEvent).suggestedFilename()).toContain('fog-harbor-cover');
  await firstCard.getByRole('button', { name: '复用图片参数' }).click();
  await expect(page.getByText('已按图片生成时的不可变参数创建新草稿。')).toBeVisible();
  await expect(page.getByText('复用', { exact: true }).first()).toBeVisible();

  const illustrationCard = page.locator('.artwork-gallery-grid article').filter({ hasText: '章节插图' }).first();
  await illustrationCard.getByRole('button', { name: '应用图片' }).click();
  await expect(illustrationCard.getByText('正在使用')).toBeVisible();
  await expect(illustrationCard.getByRole('button', { name: '删除图片' })).toBeDisabled();
  await illustrationCard.getByRole('button', { name: '取消应用图片' }).click();
  await expect(illustrationCard.getByText('正在使用')).toHaveCount(0);
  await expect(illustrationCard.getByRole('button', { name: '删除图片' })).toBeEnabled();
  expect(externalRequests).toEqual([]);
});

test('URL restore and exact desktop/mobile viewport evidence keep primary actions reachable', async ({ page }) => {
  await page.getByRole('tab', { name: '角色肖像' }).click();
  await expect(page.getByLabel('作品类型')).toHaveValue('character_portrait');
  await expect(page).toHaveURL(/projectId=artwork-browser-project.*view=character.*draft=art-draft-portrait/);
  await page.reload();
  await expect(page.getByLabel('肖像角色')).toHaveValue('hero');

  await fs.mkdir(screenshotDir, { recursive: true });
  for (const viewport of [
    { width: 1920, height: 1200, name: '1920x1200' },
    { width: 1440, height: 900, name: '1440x900' },
    { width: 390, height: 844, name: '390x844' }
  ]) {
    await page.setViewportSize(viewport);
    await page.goto('/browser-fixture.html?surface=artwork&projectId=artwork-browser-project&view=story&draft=art-draft-cover');
    await expect(page.getByLabel('图片提示词')).toBeVisible();
    const generate = page.getByRole('button', { name: '生成 1 张图片' });
    await expect(generate).toBeVisible();
    const generateBox = await generate.boundingBox();
    expect(generateBox.y + generateBox.height).toBeLessThanOrEqual(viewport.height + 1);
    expect(generateBox.x + generateBox.width).toBeLessThanOrEqual(viewport.width + 1);
    await page.getByRole('heading', { name: '任务状态' }).scrollIntoViewIfNeeded();
    await expect(page.getByRole('heading', { name: '任务状态' })).toBeVisible();
    await page.getByRole('heading', { name: '图片图库' }).scrollIntoViewIfNeeded();
    await expect(page.getByRole('button', { name: /预览图片/ }).first()).toBeVisible();
    const geometry = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth }));
    expect(geometry.scrollWidth).toBeLessThanOrEqual(geometry.width + 1);
    await page.evaluate(() => {
      window.scrollTo(0, 0);
      for (const element of document.querySelectorAll('.artwork-workspace, .artwork-body, .artwork-main-column')) {
        element.scrollTop = 0;
        element.scrollLeft = 0;
      }
    });
    await page.screenshot({ path: path.join(screenshotDir, `visual-studio-${viewport.name}.png`), fullPage: true });
  }
});
