import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { CoreCastEditor } from '../components/CoreCastEditor.jsx';
import { FoundationOverview } from './FoundationOverview.jsx';

describe('foundation components', () => {
  it('adaptation 同时显示 source 只读与 target 数据', () => {
    const markup = renderToStaticMarkup(<FoundationOverview server={{ mode: 'adaptation', baseRevision: 2, baseAuditSignature: 'abcdef012345', editable: true, sourceFoundation: { premise: '原著前提', source_signature: 'source123456', characters: [{ id: 'source-hero', name: '原著主角', role: '主角', description: '推动原著主线' }], world_rules: [] }, modeSpecific: {}, coreCast: { members: [], source_dispositions: [] } }} draft={{ premise: '目标前提', characters: [], relationships: [], world_rules: [] }} onPremiseChange={() => {}} />);
    expect(markup).toContain('SourceFoundation（只读）');
    expect(markup).toContain('不可写入');
    expect(markup).toContain('原著前提');
    expect(markup).toContain('目标前提');
    expect(markup).toContain('目标故事前提');
    expect(markup).toContain('来源角色档案');
    expect(markup).toContain('原著主角');
  });

  it('核心角色只读模式展示中文重要性和来源角色', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor readOnly mode="adapt" value={{ members: [{ character: { id: 'hero', name: '林舟', traits: [], constraints: [] }, importance: 'protagonist', origin: 'source', source_character_ids: ['source-1'] }] }} confirmed />);
    expect(markup).toContain('主角');
    expect(markup).toContain('source-1');
    expect(markup).not.toContain('保存修改');
  });

  it('目标核心角色尚未生成时展示来源分析角色', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor readOnly mode="adapt" value={{ members: [] }} sourceMajorCharacters={[{ id: 'source-hero', name: '原著主角', aliases: ['阿原'] }]} />);
    expect(markup).toContain('目标核心角色尚未生成');
    expect(markup).toContain('原著主角');
    expect(markup).toContain('来源分析角色');
    expect(markup).toContain('阿原');
  });

  it('核心角色编辑器在工作区解释修改流程并隐藏原始 JSON', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor mode="normal" value={{ members: [{ character: { id: 'hero', name: '林舟', role: '主角', traits: ['冷静'], constraints: ['不背叛同伴'] }, importance: 'protagonist', origin: 'original' }], planned_relationships: [] }} completion={{ complete: false, missing: [{ code: 'goal_required', member_id: 'hero', description: 'goal is required' }] }} />);
    expect(markup).toContain('核心角色工作区');
    expect(markup).toContain('先改角色');
    expect(markup).toContain('角色重要性');
    expect(markup).toContain('主角');
    expect(markup).toContain('请填写角色目标');
    expect(markup).not.toContain('核心计划关系（JSON）');
    expect(markup).not.toContain('&gt;protagonist&lt;');
  });

  it('角色较多时一页只渲染一个角色表单并提供直接分页', () => {
    const markup = renderToStaticMarkup(<CoreCastEditor mode="normal" value={{ members: [
      { character: { id: 'hero', name: '林舟', role: '主角', traits: ['冷静'], constraints: ['不背叛同伴'] }, importance: 'protagonist', origin: 'original' },
      { character: { id: 'rival', name: '顾念', role: '对手', traits: ['敏锐'], constraints: ['不轻易认输'] }, importance: 'antagonist', origin: 'original' }
    ], planned_relationships: [] }} completion={{ complete: false, missing: [] }} />);
    expect(markup).toContain('第 1 项，共 2 项');
    expect(markup).toContain('林舟');
    expect(markup).toContain('顾念');
    expect(markup).toContain('角色 1 代号');
    expect(markup).not.toContain('角色 2 代号');
    expect((markup.match(/core-cast-member-card/g) || [])).toHaveLength(1);
  });
});
