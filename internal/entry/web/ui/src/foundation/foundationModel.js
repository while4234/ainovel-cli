const relationshipTypes = ['ally', 'rival', 'family', 'romantic', 'mentor', 'professional', 'other'];
const relationshipDirections = ['directed', 'bidirectional', 'undirected'];
const relationshipStatuses = ['planned', 'active', 'strained', 'broken', 'resolved'];
const ruleStrengths = ['hard', 'soft'];

export const foundationOptions = {
  relationshipTypes,
  relationshipDirections,
  relationshipStatuses,
  ruleStrengths
};

export function cloneFoundation(value) {
  return JSON.parse(JSON.stringify(value || {}));
}

export function normalizeFoundation(value = {}) {
  return {
    schema_version: Number(value.schema_version || 1),
    revision: Number(value.revision || 0),
    premise: String(value.premise || ''),
    characters: array(value.characters).map(normalizeCharacter),
    relationships: array(value.relationships).map(normalizeRelationship),
    relationships_reviewed: Boolean(value.relationships_reviewed),
    world_rules: array(value.world_rules).map(normalizeWorldRule),
    ...(value.updated_at ? { updated_at: String(value.updated_at) } : {})
  };
}

export function normalizeFoundationResponse(response) {
  const state = response?.foundation || {};
  return {
    project: response?.project || null,
    mode: state.mode === 'adaptation' ? 'adaptation' : 'normal',
    sourceFoundation: state.source_foundation || null,
    targetFoundation: normalizeFoundation(state.target_foundation),
    editable: Boolean(state.editable),
    readonlyReason: String(state.readonly_reason || ''),
    baseRevision: Number(state.base_revision || 0),
    baseAuditSignature: String(state.base_audit_signature || ''),
    coreCastSignature: String(state.core_cast_signature || ''),
    coreCast: state.core_cast || null,
    coreCastCompletion: state.core_cast_completion || null,
    coreCastConfirmed: Boolean(state.core_cast_confirmed),
    modeSpecific: state.mode_specific || null,
    modeSpecificError: String(state.mode_specific_error || ''),
    activeRevision: state.active_revision || null,
    planningReview: state.planning_review || null,
    allowedOperations: array(state.allowed_operations).map(String)
  };
}

export function normalizeCharacter(value = {}) {
  return {
    id: String(value.id || ''),
    name: String(value.name || ''),
    aliases: uniqueStrings(value.aliases),
    role: String(value.role || ''),
    description: String(value.description || ''),
    arc: String(value.arc || ''),
    traits: uniqueStrings(value.traits),
    tier: String(value.tier || ''),
    faction: String(value.faction || ''),
    goal: String(value.goal || ''),
    motivation: String(value.motivation || ''),
    conflict: String(value.conflict || ''),
    voice: String(value.voice || ''),
    constraints: uniqueStrings(value.constraints),
    notes: String(value.notes || '')
  };
}

export function normalizeRelationship(value = {}) {
  return {
    id: String(value.id || ''),
    source_character_id: String(value.source_character_id || ''),
    target_character_id: String(value.target_character_id || ''),
    type: relationshipTypes.includes(value.type) ? value.type : 'other',
    label: String(value.label || ''),
		direction: normalizeRelationshipDirection(value.direction),
    status: relationshipStatuses.includes(value.status) ? value.status : 'planned',
    description: String(value.description || ''),
    since: String(value.since || ''),
    tags: uniqueStrings(value.tags),
    constraints: uniqueStrings(value.constraints)
  };
}

export function normalizeWorldRule(value = {}) {
  return {
    id: String(value.id || ''),
    title: String(value.title || ''),
    category: String(value.category || 'other'),
    rule: String(value.rule || ''),
    boundary: String(value.boundary || ''),
    strength: ruleStrengths.includes(value.strength) ? value.strength : 'hard',
    priority: Number.isFinite(Number(value.priority)) ? Number(value.priority) : 0,
    tags: uniqueStrings(value.tags)
  };
}

export function newFoundationCharacter() {
  return normalizeCharacter({ id: clientID('character') });
}

export function newFoundationRelationship() {
  return normalizeRelationship({ id: clientID('relationship') });
}

export function newFoundationWorldRule() {
  return normalizeWorldRule({ id: clientID('rule') });
}

export function candidateFingerprint(value) {
  const foundation = normalizeFoundation(value);
  delete foundation.updated_at;
  return JSON.stringify(foundation);
}

export function validateFoundationDraft(value) {
  const foundation = normalizeFoundation(value);
  const fields = {};
  const summary = [];
  if (!foundation.premise.trim()) addError(fields, summary, 'premise', '故事前提不能为空');

  const characterIDs = new Set();
  const identities = new Map();
  foundation.characters.forEach((character, index) => {
    const prefix = `characters.${index}`;
    if (!character.id.trim()) addError(fields, summary, `${prefix}.id`, `角色 ${index + 1} 缺少稳定 ID`);
    if (!character.name.trim()) addError(fields, summary, `${prefix}.name`, `角色 ${index + 1} 姓名不能为空`);
    if (characterIDs.has(character.id)) addError(fields, summary, `${prefix}.id`, `角色 ID ${character.id} 重复`);
    characterIDs.add(character.id);
    for (const label of [character.name, ...character.aliases]) {
      const identity = label.trim().toLocaleLowerCase();
      if (!identity) continue;
      const owner = identities.get(identity);
      if (owner && owner !== character.id) addError(fields, summary, `${prefix}.aliases`, `姓名或别名 ${label} 与其他角色冲突`);
      identities.set(identity, character.id);
    }
  });
  if (!foundation.characters.length) addError(fields, summary, 'characters', '至少需要一个角色');

  const relationIDs = new Set();
  foundation.relationships.forEach((relationship, index) => {
    const prefix = `relationships.${index}`;
    if (relationIDs.has(relationship.id)) addError(fields, summary, `${prefix}.id`, `关系 ID ${relationship.id} 重复`);
    relationIDs.add(relationship.id);
    if (!characterIDs.has(relationship.source_character_id)) addError(fields, summary, `${prefix}.source_character_id`, `关系 ${index + 1} 的起点角色已悬空`);
    if (!characterIDs.has(relationship.target_character_id)) addError(fields, summary, `${prefix}.target_character_id`, `关系 ${index + 1} 的终点角色已悬空`);
    if (relationship.source_character_id && relationship.source_character_id === relationship.target_character_id) addError(fields, summary, `${prefix}.target_character_id`, `关系 ${index + 1} 不能指向同一角色`);
  });

  const ruleIDs = new Set();
  foundation.world_rules.forEach((rule, index) => {
    const prefix = `world_rules.${index}`;
    if (!rule.rule.trim()) addError(fields, summary, `${prefix}.rule`, `世界规则 ${index + 1} 的规则正文不能为空`);
    if (ruleIDs.has(rule.id)) addError(fields, summary, `${prefix}.id`, `世界规则 ID ${rule.id} 重复`);
    ruleIDs.add(rule.id);
  });
  if (!foundation.world_rules.length) addError(fields, summary, 'world_rules', '至少需要一条世界规则');
  return { valid: summary.length === 0, fields, summary };
}

export function sourceMajorCharacters(sourceFoundation) {
  return array(sourceFoundation?.characters).map((character) => ({
    id: String(character?.id || ''),
    name: String(character?.name || ''),
    aliases: uniqueStrings(character?.aliases),
    role: String(character?.role || ''),
    description: String(character?.description || ''),
    arc: String(character?.arc || ''),
    traits: uniqueStrings(character?.traits),
    goal: String(character?.goal || ''),
    motivation: String(character?.motivation || ''),
    conflict: String(character?.conflict || ''),
    voice: String(character?.voice || ''),
    constraints: uniqueStrings(character?.constraints),
    faction: String(character?.faction || ''),
    notes: String(character?.notes || '')
  })).filter((character) => character.id || character.name);
}

export function shortSignature(value) {
  const text = String(value || '');
  return text ? text.slice(0, 10) : '—';
}

export function isCoreCharacter(character, coreCast) {
  const coreIDs = new Set(array(coreCast?.members).map((member) => String(member?.character?.id || '')));
  return coreIDs.has(String(character?.id || ''));
}

function uniqueStrings(value) {
  const seen = new Set();
  return array(value).map((item) => String(item || '').trim()).filter((item) => {
    const key = item.toLocaleLowerCase();
    if (!item || seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function addError(fields, summary, path, message) {
  if (!fields[path]) fields[path] = message;
  if (!summary.includes(message)) summary.push(message);
}

function clientID(kind) {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const prefix = { character: 'char', relationship: 'rel', rule: 'rule' }[kind] || kind;
  return `${prefix}-${random}`;
}

function array(value) {
	return Array.isArray(value) ? value : [];
}

function normalizeRelationshipDirection(value) {
	if (value === 'mutual' || !value) return 'bidirectional';
	return relationshipDirections.includes(value) ? value : 'bidirectional';
}
