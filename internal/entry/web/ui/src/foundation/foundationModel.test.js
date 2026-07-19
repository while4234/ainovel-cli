import { describe, expect, it } from 'vitest';
import { candidateFingerprint, newFoundationCharacter, normalizeFoundationResponse, validateFoundationDraft } from './foundationModel.js';

const complete = {
  schema_version: 1, revision: 4, premise: '目标故事',
  characters: [{ id: 'hero', name: '林舟', aliases: ['阿舟', '阿舟'], role: '主角', traits: [], constraints: [] }],
  relationships: [], relationships_reviewed: true,
  world_rules: [{ id: 'rule-1', category: 'magic', rule: '能力有代价', boundary: '不可复活', strength: 'hard' }]
};

describe('foundation model', () => {
  it('映射 normal/adaptation DTO 且保留 source 只读数据', () => {
    const normal = normalizeFoundationResponse({ foundation: { mode: 'normal', target_foundation: complete, editable: true, allowed_operations: ['get', 'preview'] } });
    expect(normal.mode).toBe('normal');
    expect(normal.sourceFoundation).toBeNull();
    const adaptation = normalizeFoundationResponse({ foundation: { mode: 'adaptation', source_foundation: { premise: '原著' }, target_foundation: complete, editable: true } });
    expect(adaptation.mode).toBe('adaptation');
    expect(adaptation.sourceFoundation.premise).toBe('原著');
    expect(adaptation.targetFoundation.premise).toBe('目标故事');
  });

  it('对别名去重，并让 UI 状态不进入 candidate fingerprint', () => {
    const response = normalizeFoundationResponse({ foundation: { target_foundation: complete } });
    expect(response.targetFoundation.characters[0].aliases).toEqual(['阿舟']);
    expect(candidateFingerprint({ ...complete, updated_at: 'before' })).toBe(candidateFingerprint({ ...complete, updated_at: 'after' }));
  });

  it('定位悬空关系与缺失规则字段', () => {
    const validation = validateFoundationDraft({ ...complete, relationships: [{ id: 'rel', source_character_id: 'hero', target_character_id: 'missing', type: 'ally' }], world_rules: [{ id: 'rule-1', rule: '' }] });
    expect(validation.valid).toBe(false);
    expect(validation.fields['relationships.0.target_character_id']).toMatch(/悬空/);
    expect(validation.fields['world_rules.0.rule']).toMatch(/不能为空/);
  });

  it('新建角色直接使用不会与服务端稳定前缀冲突的 UUID ID', () => {
    expect(newFoundationCharacter().id).toMatch(/^char-/);
  });
});
