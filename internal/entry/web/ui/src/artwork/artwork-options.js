export const ARTWORK_VIEWS = Object.freeze({ STORY: 'story', CHARACTER: 'character' });
export const ARTWORK_TYPES = Object.freeze({
  COVER: 'cover',
  ILLUSTRATION: 'illustration',
  PORTRAIT: 'character_portrait'
});

const TARGET_ASPECT_RATIO = Object.freeze({
  [ARTWORK_TYPES.COVER]: 2 / 3,
  [ARTWORK_TYPES.PORTRAIT]: 2 / 3,
  [ARTWORK_TYPES.ILLUSTRATION]: 16 / 9
});

export function artworkViewForType(workType) {
  return workType === ARTWORK_TYPES.PORTRAIT ? ARTWORK_VIEWS.CHARACTER : ARTWORK_VIEWS.STORY;
}

export function workTypesForView(view) {
  return view === ARTWORK_VIEWS.CHARACTER
    ? [ARTWORK_TYPES.PORTRAIT]
    : [ARTWORK_TYPES.COVER, ARTWORK_TYPES.ILLUSTRATION];
}

export function scopeKindsForType(workType) {
  if (workType === ARTWORK_TYPES.PORTRAIT) return ['character'];
  if (workType === ARTWORK_TYPES.ILLUSTRATION) return ['project', 'volume', 'chapter'];
  return ['project'];
}

export function normalizeArtworkScope(workType, scope, scopeId, catalog = {}) {
  const allowed = scopeKindsForType(workType);
  const nextScope = allowed.includes(scope) ? scope : allowed[0];
  if (nextScope === 'project') return { scope: 'project', scopeId: '' };
  const entries = nextScope === 'character'
    ? catalog.characters || []
    : nextScope === 'volume'
      ? catalog.volumes || []
      : catalog.chapters || [];
  const nextScopeId = entries.some((entry) => entry.id === scopeId) ? scopeId : entries[0]?.id || '';
  return { scope: nextScope, scopeId: nextScopeId };
}

export function flattenArtworkScopeCatalog(tree = {}, foundation = {}) {
  const volumes = [];
  const chapters = [];
  for (const volume of tree.nodes || []) {
    if (!volume?.stable_id) continue;
    volumes.push({ id: volume.stable_id, label: volume.display_label || volume.title || volume.stable_id });
    for (const arc of volume.children || []) {
      for (const chapter of arc.children || []) {
        if (!chapter?.stable_id) continue;
        chapters.push({
          id: chapter.stable_id,
          label: chapter.display_label || chapter.title || chapter.stable_id,
          volumeId: volume.stable_id
        });
      }
    }
  }
  const target = foundation?.foundation?.target_foundation || foundation?.target_foundation || {};
  const characters = (target.characters || [])
    .filter((character) => character?.id)
    .map((character) => ({ id: character.id, label: character.name || character.id }));
  return { volumes, chapters, characters };
}

export function enabledArtworkModels(registry = {}) {
  return (registry.models || []).filter((model) => model?.id && model.enabled !== false);
}

export function findArtworkModel(registry, modelId) {
  return enabledArtworkModels(registry).find((model) => model.id === modelId) || null;
}

export function chooseDefaultArtworkSize(model, workType) {
  const sizes = model?.supported_sizes || [];
  if (!sizes.length) return '';
  const target = TARGET_ASPECT_RATIO[workType] || TARGET_ASPECT_RATIO[ARTWORK_TYPES.ILLUSTRATION];
  let best = sizes[0];
  let bestDistance = Number.POSITIVE_INFINITY;
  for (const size of sizes) {
    const ratio = ratioForSize(size);
    if (!ratio) continue;
    const distance = Math.abs(Math.log(ratio / target));
    if (distance < bestDistance) {
      best = size;
      bestDistance = distance;
    }
  }
  return best.value || '';
}

export function normalizeArtworkModelSize(registry, modelId, size, workType, fallbackModelId = '') {
  const models = enabledArtworkModels(registry);
  const model = models.find((item) => item.id === modelId)
    || models.find((item) => item.id === fallbackModelId)
    || models[0]
    || null;
  if (!model) return { modelId: '', size: '' };
  const supported = model.supported_sizes || [];
  const nextSize = supported.some((item) => item.value === size)
    ? size
    : chooseDefaultArtworkSize(model, workType);
  return { modelId: model.id, size: nextSize };
}

export function promptModelOptions(modelConfig = {}) {
  return (modelConfig.providers || []).flatMap((provider) => (provider.models || []).map((model) => ({
    provider: provider.name,
    model,
    value: JSON.stringify([provider.name, model]),
    label: `${provider.label || provider.name} / ${model}`
  })));
}

export function selectedPromptModel(modelConfig = {}, runtime = {}) {
  const providers = modelConfig.providers || [];
  const configuredProvider = runtime?.config?.provider || providers[0]?.name || '';
  const configuredModel = runtime?.config?.model || providers.find((provider) => provider.name === configuredProvider)?.models?.[0] || '';
  const route = (modelConfig.roles || []).find((item) => item.role === 'default');
  const provider = route?.provider || configuredProvider;
  const model = route?.model || configuredModel;
  return { provider, model, value: provider && model ? JSON.stringify([provider, model]) : '' };
}

export function parsePromptModelOption(value) {
  try {
    const [provider, model] = JSON.parse(value);
    return { provider: String(provider || '').trim(), model: String(model || '').trim() };
  } catch {
    return { provider: '', model: '' };
  }
}

function ratioForSize(size = {}) {
  const explicit = String(size.aspect_ratio || '').match(/^(\d+(?:\.\d+)?):(\d+(?:\.\d+)?)$/);
  if (explicit) return Number(explicit[1]) / Number(explicit[2]);
  const dimensions = String(size.value || '').match(/^(\d+)x(\d+)$/i);
  if (!dimensions) return 0;
  return Number(dimensions[1]) / Number(dimensions[2]);
}
