import { describe, expect, it } from 'vitest';
import {
  buildAdaptationProposalKey,
  buildBeginCoCreatePayload,
  buildCoCreateIntakeInitial,
  clearAdaptationProposalSnapshot,
  deriveWorkspaceProgress,
  getAdaptationProposalReview,
  getVisibleAdaptationProposalReview,
  getSimulationProfileStatus,
  getSnapshotOutlineRows,
  isProjectRunning,
  resolveCoCreateStructureChoice,
  resolveCoCreateTargetTotalWords,
  simulationProfileSummaryText
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

  it('normalizes full outline rows with camel, Pascal, and snake fallback fields', () => {
    const rows = getSnapshotOutlineRows({
      outline: [
        {
          chapter: 13,
          title: '雨巷录音',
          coreEvent: '主角确认录音来自未来',
          hook: '门外脚步声同步响起',
          scenes: ['事务所', '雨巷'],
          writtenWordCount: 4200,
          wordBudget: { targetRunes: 4500, minRunes: 3900, maxRunes: 5100 },
          sourceCoverage: { chapters: [4, 5], from: 4, to: 5, runes: 3800 }
        },
        {
          Chapter: 14,
          Title: '旧门牌',
          CoreEvent: '门牌指向失踪者家属',
          Scenes: ['老楼'],
          WrittenWordCount: 0,
          WordBudget: { TargetWords: 4000, MinWords: 3500, MaxWords: 4500 }
        }
      ]
    });

    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      chapter: 13,
      title: '雨巷录音',
      coreEvent: '主角确认录音来自未来',
      hook: '门外脚步声同步响起',
      writtenWordCount: 4200
    });
    expect(rows[0].wordBudget.targetRunes).toBe(4500);
    expect(rows[0].sourceCoverage.from).toBe(4);
    expect(rows[1].wordBudget.targetWords).toBe(4000);
  });

  it('falls back to adaptation proposal chapters for old snapshots without Outline', () => {
    const review = getAdaptationProposalReview({
      adaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: '改成雨城悬疑',
        chapters: [
          {
            chapter: 1,
            title: '旧录音',
            core_event: '发现时间错位',
            source_chapters: [1, 2],
            target_runes: 3000,
            target_min_runes: 2600,
            target_max_runes: 3400
          }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.chapterCount).toBe(1);
    expect(review.chapters[0].sourceCoverage.chapters).toEqual([1, 2]);
    expect(review.chapters[0].wordBudget.targetRunes).toBe(3000);
  });

  it('hides stale adaptation proposals after source upload or proposal input changes', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Make it a mystery',
        ChapterCount: 1
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Make it a mystery',
        chapters: [{ chapter: 1, title: 'Old plan' }]
      }
    };
    const currentAdaptation = {
      sourceFile: { relative_path: 'old.txt' },
      mode: 'free',
      brief: 'Make it a mystery'
    };
    currentAdaptation.proposalKey = buildAdaptationProposalKey(currentAdaptation);

    expect(getVisibleAdaptationProposalReview(snapshot, currentAdaptation).proposalReady).toBe(true);

    const uploadedSnapshot = clearAdaptationProposalSnapshot(snapshot);
    const afterUpload = {
      sourceFile: { relative_path: 'new.txt' },
      mode: 'free',
      brief: 'Make it a mystery',
      proposalKey: ''
    };
    const afterUploadReview = getVisibleAdaptationProposalReview(uploadedSnapshot, afterUpload);

    expect(getAdaptationProposalReview(uploadedSnapshot).loaded).toBe(false);
    expect(afterUploadReview.loaded).toBe(false);
    expect(afterUploadReview.proposalReady).toBe(false);

    const changedBriefReview = getVisibleAdaptationProposalReview(snapshot, {
      ...currentAdaptation,
      brief: 'Make it a romance'
    });

    expect(changedBriefReview.loaded).toBe(false);
    expect(changedBriefReview.proposalReady).toBe(false);
    expect(changedBriefReview.stale).toBe(true);
  });

  it('reports imported simulation profiles as loaded after refresh', () => {
    const profile = getSimulationProfileStatus({
      SimulationSummary: {
        Loaded: true,
        SourceCount: 2,
        SourceFiles: ['a.txt', 'b.txt'],
        StyleSignals: ['近景冷调'],
        HookSignals: ['章尾反转']
      }
    });

    expect(profile.loaded).toBe(true);
    expect(profile.sourceCount).toBe(2);
    expect(profile.sourceFiles).toEqual(['a.txt', 'b.txt']);
    expect(profile.signals).toContain('章尾反转');
  });

  it('summarizes simulation profiles without timestamps or source filenames', () => {
    expect(simulationProfileSummaryText({
      loaded: true,
      sourceCount: 143,
      updatedAt: '2026-07-02T15:28:13+08:00',
      sourceFiles: ['001_第一章_新的科目.txt']
    })).toBe('143 篇语料');

    expect(simulationProfileSummaryText({ loaded: true, sourceCount: 0 })).toBe('画像已加载');
    expect(simulationProfileSummaryText({ loaded: false })).toBe('上传或导入画像后会出现在这里');
  });
});
