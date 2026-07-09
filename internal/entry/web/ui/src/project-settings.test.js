import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  buildProjectStyleSaveRequest,
  canSubmitProjectStyle,
  normalizeProjectStyleCatalog,
  projectStyleLabel,
  resolveProjectStyleID,
  snapshotHasStartedWritingContent
} from './App.jsx';

const appSource = readFileSync(new URL('./App.jsx', import.meta.url), 'utf8');

function extractFunctionBody(source, name) {
  const signature = `const ${name} = async`;
  const signatureStart = source.indexOf(signature);
  expect(signatureStart).toBeGreaterThanOrEqual(0);

  const bodyStart = source.indexOf('{', signatureStart);
  expect(bodyStart).toBeGreaterThanOrEqual(0);

  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index];
    if (char === '{') {
      depth += 1;
    }
    if (char === '}') {
      depth -= 1;
      if (depth === 0) {
        return source.slice(bodyStart + 1, index);
      }
    }
  }

  throw new Error(`Could not find body for ${name}`);
}

describe('project settings panel', () => {
  it('adds the settings tab alongside project side tools', () => {
    expect(appSource).toContain("sideView === 'settings'");
    expect(appSource).toContain("setSideView('settings')");
    expect(appSource).toContain('>设定<');
  });

  it('uses style labels for display while preserving ids for values', () => {
    const catalog = normalizeProjectStyleCatalog({
      default_style: 'default',
      styles: [
        { id: 'default', label: '通用写作风格' },
        { id: 'fantasy', label: '奇幻冒险风格' }
      ]
    });

    expect(catalog.defaultStyle).toBe('default');
    expect(projectStyleLabel(catalog.styles, 'default')).toBe('通用写作风格');
    expect(projectStyleLabel(catalog.styles, 'fantasy')).toBe('奇幻冒险风格');
    expect(projectStyleLabel(catalog.styles, 'missing')).toBe('missing');
  });

  it('resolves current style from snapshot before runtime defaults', () => {
    const catalog = normalizeProjectStyleCatalog({
      default_style: 'default',
      styles: [{ id: 'romance', label: '言情风格' }]
    });

    expect(resolveProjectStyleID(
      { Style: 'romance' },
      { config: { style: 'fantasy' } },
      catalog
    )).toBe('romance');
  });

  it('builds save requests with the selected style id', () => {
    expect(buildProjectStyleSaveRequest(
      { id: 'project-1' },
      { selectedStyle: 'fantasy' }
    )).toEqual({
      ok: true,
      projectId: 'project-1',
      style: 'fantasy'
    });
  });

  it('allows style saves after proposal generation but before writing starts', () => {
    const projectSettings = {
      styles: [{ id: 'default', label: '通用写作风格' }, { id: 'romance', label: '言情风格' }],
      selectedStyle: 'romance',
      loadStatus: 'done',
      saveStatus: 'idle'
    };
    const snapshot = {
      NovelName: '半熟恋人',
      Phase: 'ready',
      TotalChapters: 36,
      CompletedCount: 0,
      TotalWordCount: 0,
      RuntimeState: 'idle',
      IsRunning: false
    };

    expect(snapshotHasStartedWritingContent(snapshot)).toBe(false);
    expect(canSubmitProjectStyle({
      activeProject: { id: 'project-1' },
      busy: false,
      currentStyle: 'default',
      projectSettings,
      snapshot
    })).toBe(true);
  });

  it('disables style saves after writing has started', () => {
    const projectSettings = {
      styles: [{ id: 'default', label: '通用写作风格' }, { id: 'romance', label: '言情风格' }],
      selectedStyle: 'romance',
      loadStatus: 'done',
      saveStatus: 'idle'
    };
    const snapshot = {
      TotalChapters: 12,
      CompletedCount: 1,
      TotalWordCount: 3200
    };

    expect(snapshotHasStartedWritingContent(snapshot)).toBe(true);
    expect(canSubmitProjectStyle({
      activeProject: { id: 'project-1' },
      busy: false,
      currentStyle: 'default',
      projectSettings,
      snapshot
    })).toBe(false);
  });

  it('saves through the project style endpoint and updates the active snapshot', () => {
    const body = extractFunctionBody(appSource, 'saveProjectStyle');

    expect(body).toContain('setProjectStyle(request.projectId, request.style)');
    expect(body).toContain('setActiveProject(data.project || activeProject)');
    expect(body).toContain('snapshot: data.snapshot || previous.snapshot');
  });
});
