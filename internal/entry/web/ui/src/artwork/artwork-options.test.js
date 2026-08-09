import { describe, expect, it } from 'vitest';
import {
  ARTWORK_TYPES,
  ARTWORK_VIEWS,
  chooseDefaultArtworkSize,
  flattenArtworkScopeCatalog,
  normalizeArtworkModelSize,
  normalizeArtworkScope,
  parsePromptModelOption,
  promptModelOptions,
  scopeKindsForType,
  selectedPromptModel
} from './artwork-options.js';

const registry = {
  models: [
    { id: 'a2e', enabled: true, supported_sizes: [
      { value: '1024x1024', aspect_ratio: '1:1' },
      { value: '1080x1620', aspect_ratio: '2:3' },
      { value: '1920x1080', aspect_ratio: '16:9' }
    ] },
    { id: 'limited', enabled: true, supported_sizes: [{ value: '768x1024' }, { value: '1280x720' }] },
    { id: 'disabled', enabled: false, supported_sizes: [{ value: '1x1' }] }
  ]
};

describe('artwork model and scope options', () => {
  it('uses portrait-like 2:3 and illustration-like 16:9 defaults', () => {
    expect(chooseDefaultArtworkSize(registry.models[0], ARTWORK_TYPES.COVER)).toBe('1080x1620');
    expect(chooseDefaultArtworkSize(registry.models[0], ARTWORK_TYPES.PORTRAIT)).toBe('1080x1620');
    expect(chooseDefaultArtworkSize(registry.models[0], ARTWORK_TYPES.ILLUSTRATION)).toBe('1920x1080');
    expect(chooseDefaultArtworkSize(registry.models[1], ARTWORK_TYPES.COVER)).toBe('768x1024');
  });

  it('normalizes an invalid size whenever the model changes', () => {
    expect(normalizeArtworkModelSize(registry, 'limited', '1080x1620', ARTWORK_TYPES.ILLUSTRATION)).toEqual({
      modelId: 'limited', size: '1280x720'
    });
    expect(normalizeArtworkModelSize(registry, 'missing', '', ARTWORK_TYPES.COVER, 'a2e')).toEqual({
      modelId: 'a2e', size: '1080x1620'
    });
  });

  it('enforces story and character scope rules without exposing scene scope', () => {
    const catalog = { characters: [{ id: 'hero' }], volumes: [{ id: 'v1' }], chapters: [{ id: 'c1' }] };
    expect(scopeKindsForType(ARTWORK_TYPES.COVER)).toEqual(['project']);
    expect(scopeKindsForType(ARTWORK_TYPES.ILLUSTRATION)).toEqual(['project', 'volume', 'chapter']);
    expect(scopeKindsForType(ARTWORK_TYPES.PORTRAIT)).toEqual(['character']);
    expect(normalizeArtworkScope(ARTWORK_TYPES.COVER, 'chapter', 'c1', catalog)).toEqual({ scope: 'project', scopeId: '' });
    expect(normalizeArtworkScope(ARTWORK_TYPES.PORTRAIT, 'character', 'missing', catalog)).toEqual({ scope: 'character', scopeId: 'hero' });
  });

  it('flattens project-native volume, chapter, and character choices', () => {
    const catalog = flattenArtworkScopeCatalog({ nodes: [{ stable_id: 'v1', display_label: '第一卷', children: [{ children: [{ stable_id: 'c1', display_label: '第一章' }] }] }] }, {
      foundation: { target_foundation: { characters: [{ id: 'hero', name: '林舟' }] } }
    });
    expect(catalog).toEqual({
      volumes: [{ id: 'v1', label: '第一卷' }],
      chapters: [{ id: 'c1', label: '第一章', volumeId: 'v1' }],
      characters: [{ id: 'hero', label: '林舟' }]
    });
  });

  it('builds a project-default prompt model selector', () => {
    const config = { providers: [{ name: 'p1', label: '主模型', models: ['m1', 'm2'] }], roles: [{ role: 'default', provider: 'p1', model: 'm2' }] };
    expect(promptModelOptions(config)).toHaveLength(2);
    expect(selectedPromptModel(config, {}).value).toBe('["p1","m2"]');
    expect(parsePromptModelOption('["p1","m1"]')).toEqual({ provider: 'p1', model: 'm1' });
    expect(parsePromptModelOption('bad')).toEqual({ provider: '', model: '' });
    expect(ARTWORK_VIEWS.STORY).toBe('story');
  });
});
