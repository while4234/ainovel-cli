export const coreCastImportanceOptions = [
  'protagonist', 'co_protagonist', 'major_pov', 'antagonist',
  'love_interest', 'major_support', 'user_important'
];

export const coreCastOriginOptions = ['original', 'source'];
export const sourceDispositionActions = ['keep', 'rename', 'merge', 'split', 'exclude'];

export function normalizeCoreCast(value, mode = 'normal') {
  const input = value && typeof value === 'object' ? value : {};
  return {
    version: 1,
    mode: mode === 'adapt' ? 'adaptation' : (input.mode || 'normal'),
    draft_revision: Number(input.draft_revision || 0),
    draft_hash: String(input.draft_hash || ''),
    source_signature: String(input.source_signature || ''),
    adaptation_intent_hash: String(input.adaptation_intent_hash || ''),
    members: Array.isArray(input.members) ? input.members.map(normalizeCoreCastMember) : [],
    planned_relationships: Array.isArray(input.planned_relationships) ? input.planned_relationships.map((item) => ({ ...item, tags: arrayOfStrings(item?.tags), constraints: arrayOfStrings(item?.constraints) })) : [],
    source_dispositions: Array.isArray(input.source_dispositions) ? input.source_dispositions.map((item) => ({ ...item, source_character_id: String(item?.source_character_id || ''), target_character_ids: arrayOfStrings(item?.target_character_ids), rationale: String(item?.rationale || '') })) : [],
    revision: Number(input.revision || 0)
  };
}

export function normalizeCoreCastMember(value = {}) {
  const character = value.character && typeof value.character === 'object' ? value.character : {};
  return {
    character: {
      id: String(character.id || ''), name: String(character.name || ''), aliases: arrayOfStrings(character.aliases),
      role: String(character.role || ''), description: String(character.description || ''), arc: String(character.arc || ''),
      traits: arrayOfStrings(character.traits), tier: String(character.tier || ''), faction: String(character.faction || ''),
      goal: String(character.goal || ''), motivation: String(character.motivation || ''), conflict: String(character.conflict || ''),
      voice: String(character.voice || ''), constraints: arrayOfStrings(character.constraints), notes: String(character.notes || '')
    },
    importance: coreCastImportanceOptions.includes(value.importance) ? value.importance : 'major_support',
    origin: coreCastOriginOptions.includes(value.origin) ? value.origin : 'original',
    mainline_function: String(value.mainline_function || ''),
    source_character_ids: arrayOfStrings(value.source_character_ids),
    inclusion_rationale: String(value.inclusion_rationale || ''),
    no_core_relationships: Boolean(value.no_core_relationships)
  };
}

export function newCoreCastMember() {
  return normalizeCoreCastMember({});
}

export function setCoreCastMemberField(contract, index, path, value) {
  const next = normalizeCoreCast(contract, contract?.mode === 'adaptation' ? 'adapt' : 'normal');
  const member = next.members[index];
  if (!member) return next;
  if (path.startsWith('character.')) member.character[path.slice('character.'.length)] = value;
  else member[path] = value;
  return next;
}

export function setCoreCastMemberSourceID(contract, index, sourceID, selected) {
  const next = normalizeCoreCast(contract, contract?.mode === 'adaptation' ? 'adapt' : 'normal');
  const member = next.members[index];
  if (!member) return next;
  const ids = new Set(member.source_character_ids);
  if (selected) ids.add(String(sourceID));
  else ids.delete(String(sourceID));
  member.source_character_ids = [...ids].filter(Boolean).sort();
  return next;
}

export function setCoreCastDisposition(contract, sourceID, change) {
  const next = normalizeCoreCast(contract, 'adapt');
  const id = String(sourceID || '').trim();
  let disposition = next.source_dispositions.find((item) => item.source_character_id === id);
  if (!disposition) {
    disposition = { source_character_id: id, action: 'keep', target_character_ids: [], rationale: '' };
    next.source_dispositions.push(disposition);
  }
  Object.assign(disposition, change);
  disposition.action = sourceDispositionActions.includes(disposition.action) ? disposition.action : 'keep';
  disposition.target_character_ids = arrayOfStrings(disposition.target_character_ids);
  if (disposition.action === 'exclude') disposition.target_character_ids = [];
  next.source_dispositions.sort((left, right) => left.source_character_id.localeCompare(right.source_character_id));
  return next;
}

function arrayOfStrings(value) {
  return Array.isArray(value) ? value.map((item) => String(item || '').trim()).filter(Boolean) : [];
}
