import { describe, expect, it } from 'vitest';
import { newCoreCastMember, normalizeCoreCast, setCoreCastDisposition, setCoreCastMemberField, setCoreCastMemberSourceID } from './coreCast.js';

describe('core cast reusable state boundary', () => {
  it('normalizes absent backend values without inventing confirmation state', () => {
    expect(normalizeCoreCast(null, 'normal')).toEqual(expect.objectContaining({
      version: 1,
      mode: 'normal',
      revision: 0,
      members: [],
      planned_relationships: [],
      source_dispositions: []
    }));
  });

  it('updates nested character fields immutably', () => {
    const base = normalizeCoreCast({ members: [newCoreCastMember()] });
    const changed = setCoreCastMemberField(base, 0, 'character.arc', 'accept leadership');
    expect(changed.members[0].character.arc).toBe('accept leadership');
    expect(base.members[0].character.arc).toBe('');
  });

  it('keeps adaptation mode and source disposition data explicit', () => {
    const value = normalizeCoreCast({
      members: [{ character: { id: 'target-lin', name: 'Lin' }, origin: 'source' }],
      source_dispositions: [{ source_character_id: 'source-lin', action: 'keep', target_character_ids: ['target-lin'] }]
    }, 'adapt');
    expect(value.mode).toBe('adaptation');
    expect(value.members[0].origin).toBe('source');
    expect(value.source_dispositions[0].action).toBe('keep');
  });

  it('updates source mappings and dispositions through structured helpers', () => {
    const base = normalizeCoreCast({ members: [{ character: { id: 'target-lin', name: 'Lin' }, origin: 'source' }] }, 'adapt');
    const mapped = setCoreCastMemberSourceID(base, 0, 'source-lin', true);
    const disposed = setCoreCastDisposition(mapped, 'source-lin', { action: 'keep', target_character_ids: ['target-lin'] });
    expect(disposed.members[0].source_character_ids).toEqual(['source-lin']);
    expect(disposed.source_dispositions[0]).toEqual(expect.objectContaining({ source_character_id: 'source-lin', action: 'keep', target_character_ids: ['target-lin'] }));
    expect(base.members[0].source_character_ids).toEqual([]);
  });
});
