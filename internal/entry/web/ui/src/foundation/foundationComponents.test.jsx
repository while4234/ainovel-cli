import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { CoreCastEditor } from '../components/CoreCastEditor.jsx';
import { FoundationOverview } from './FoundationOverview.jsx';

describe('foundation components', () => {
  it('adaptation 同时显示 source 只读与 target 数据', () => {
    const markup = renderToStaticMarkup(<FoundationOverview server={{ mode: 'adaptation', baseRevision: 2, baseAuditSignature: 'abcdef012345', editable: true, sourceFoundation: { premise: '原著前提', source_signature: 'source123456', characters: [], world_rules: [] }, modeSpecific: {}, coreCast: { members: [], source_dispositions: [] } }} draft={{ premise: '目标前提', characters: [], relationships: [], world_rules: [] }} onPremiseChange={() => {}} />);
    expect(markup).toContain('SourceFoundation（只读）');
    expect(markup).toContain('不可写入');
    expect(markup).toContain('原著前提');
    expect(markup).toContain('目标前提');
    expect(markup).toContain('目标故事前提');
  });

  it('复用 CoreCastEditor 的只读模式展示 importance/origin/source IDs', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor readOnly mode="adapt" value={{ members: [{ character: { id: 'hero', name: '林舟', traits: [], constraints: [] }, importance: 'protagonist', origin: 'source', source_character_ids: ['source-1'] }] }} confirmed />);
    expect(markup).toContain('protagonist');
    expect(markup).toContain('source-1');
    expect(markup).not.toContain('保存角色契约');
  });
});
