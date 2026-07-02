import { describe, expect, it } from 'vitest';
import {
  buildBeginCoCreatePayload,
  buildCoCreateIntakeInitial,
  deriveWorkspaceProgress,
  isProjectRunning,
  resolveCoCreateStructureChoice,
  resolveCoCreateTargetTotalWords
} from './App.jsx';

describe('co-create begin payload helpers', () => {
  it('sends target_total_words for normal co-create only', () => {
    expect(buildBeginCoCreatePayload({
      kind: 'normal',
      initial: '  moon city mystery  ',
      targetTotalWords: 10000
    })).toEqual({
      kind: 'normal',
      initial: 'moon city mystery',
      target_total_words: 10000
    });

    expect(buildBeginCoCreatePayload({
      kind: 'adapt',
      initial: '',
      sourceFile: 'source.txt',
      mode: 'arc',
      targetTotalWords: 30000
    })).toEqual({
      kind: 'adapt',
      initial: '',
      source_file: 'source.txt',
      mode: 'arc'
    });
  });

  it('resolves preset and custom total word choices', () => {
    expect(resolveCoCreateTargetTotalWords({})).toBe(0);
    expect(resolveCoCreateTargetTotalWords({ targetTotalWordsChoice: '30000' })).toBe(30000);
    expect(resolveCoCreateTargetTotalWords({
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '12000'
    })).toBe(12000);
    expect(resolveCoCreateTargetTotalWords({
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '12.5'
    })).toBe(0);
  });

  it('builds intake prompt with total-word and short-story structure rules', () => {
    const prompt = buildCoCreateIntakeInitial('写未来地球赛博悬疑', {
      targetTotalWords: 5000,
      structureChoice: 'single'
    });

    expect(resolveCoCreateStructureChoice({ structureChoice: 'unknown' })).toBe('single');
    expect(prompt).toContain('target_total_words=5000');
    expect(prompt).toContain('全书总字数');
    expect(prompt).toContain('3000-5000');
    expect(prompt).toContain('不要拆成多个章节');
  });
});

describe('workspace progress derivation', () => {
  it('derives progress, budget, and running tool from snapshot agents first', () => {
    const progress = deriveWorkspaceProgress({
      runtime_state: 'running',
      completed_count: 3,
      total_chapters: 12,
      current_chapter: 4,
      total_word_count: 12000,
      word_budget: 30000,
      agents: [
        { name: 'writer', state: 'tool', tool: 'draft_chapter', summary: 'drafting chapter' }
      ]
    }, []);

    expect(progress.statusLabel).toBe('running');
    expect(progress.chapterLabel).toBe('3/12');
    expect(progress.currentChapter).toBe(4);
    expect(progress.wordCount).toBe(12000);
    expect(progress.targetWords).toBe(30000);
    expect(progress.wordLabel).toContain('/');
    expect(progress.runningLabel).toBe('writer / draft_chapter');
  });

  it('falls back to running event rows when no agent is active', () => {
    const progress = deriveWorkspaceProgress({
      RuntimeState: 'paused',
      CompletedCount: 1,
      TotalChapters: 5,
      TotalWordCount: 5000,
      Agents: [{ Name: 'writer', State: 'idle' }]
    }, [
      { seq: 1, event: { running: false, agent: 'editor', summary: 'reviewed' } },
      { seq: 2, event: { running: true, agent: 'architect', summary: 'planning volume' } }
    ]);

    expect(progress.runningLabel).toBe('architect / planning volume');
  });

  it('detects running snapshots for pause controls', () => {
    expect(isProjectRunning({ IsRunning: true, RuntimeState: 'paused' })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'running', Agents: [] })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'paused', Agents: [{ Name: 'writer', State: 'idle' }] })).toBe(false);
    expect(isProjectRunning({ RuntimeState: '', Agents: [{ Name: 'writer', State: 'working' }] })).toBe(true);
  });
});
