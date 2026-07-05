import { describe, expect, it } from 'vitest';
import {
  applyAdaptationProposalSnapshot,
  applyHostEventToSimulationState,
  buildAdaptationProposalKey,
  buildAdaptationRevisionPayload,
  buildBeginCoCreatePayload,
  buildChapterRevisionPayload,
  buildCoCreatePlanningRevisionPayload,
  buildCoCreateIntakeInitial,
  buildExportSuggestedName,
  buildVolumeReviewRevisionPayload,
  canRunAdaptationAnalysis,
  canSaveAnalyzedNovelToLibrary,
  canRunSimulationAnalysis,
  clampCoCreateDecisionPageIndex,
  clearAdaptationProposalSnapshot,
  deriveWorkspaceProgress,
  formatAdaptationSourceCoverageLabel,
  formatAdaptationVolumeSourceLabel,
  getAdaptationProposalReview,
  getCompletedBookChapterRevisionView,
  getCompletedBookSelectedChapterView,
  getCoCreatePlanningReview,
  getVisibleAdaptationProposalReview,
  getSimulationProfileStatus,
  getSnapshotOutlineRows,
  inferCoCreateIntakeFromInitial,
  isCoCreateRequestBusy,
  isSimulationProfileActionBusy,
  isProjectRunning,
  resolveCoCreateStructureChoice,
  resolveCoCreateTargetTotalWords,
  resolveVisibleDefaultModel,
  restoreSimulationProjectState,
  restoreProjectWorkbenchSnapshot,
  simulationFilesFromResponse,
  simulationProfileSummaryText
} from './App.jsx';

describe('co-create begin payload helpers', () => {
  it('clamps co-create decision pagination to visible pending decisions', () => {
    expect(clampCoCreateDecisionPageIndex(2, 4)).toBe(2);
    expect(clampCoCreateDecisionPageIndex(-1, 4)).toBe(0);
    expect(clampCoCreateDecisionPageIndex(9, 4)).toBe(3);
    expect(clampCoCreateDecisionPageIndex('bad', 4)).toBe(0);
    expect(clampCoCreateDecisionPageIndex(2, 0)).toBe(0);
  });

  it('builds safe export suggested filenames', () => {
    expect(buildExportSuggestedName({ path: '', format: 'txt' }, { name: '梦中的女孩改_v2' })).toBe('梦中的女孩改_v2.txt');
    expect(buildExportSuggestedName({ path: 'draft.epub', format: 'txt' }, { name: 'Book' })).toBe('draft.txt');
    expect(buildExportSuggestedName({ path: 'bad/name:*?', format: 'epub' }, { name: 'Book' })).toBe('name___.epub');
  });

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

    expect(buildBeginCoCreatePayload({
      kind: 'adapt',
      initial: '',
      fallbackInitial: '  保留主线但加强女主调查线  ',
      sourceFile: 'source.txt',
      mode: 'free'
    })).toEqual({
      kind: 'adapt',
      initial: '保留主线但加强女主调查线',
      source_file: 'source.txt',
      mode: 'free'
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

  it('infers explicit total words from the initial idea', () => {
    expect(inferCoCreateIntakeFromInitial('创作一篇5000字的短篇小说')).toMatchObject({
      targetTotalWords: 5000,
      targetTotalWordsChoice: '5000',
      structureChoice: 'single'
    });
    expect(inferCoCreateIntakeFromInitial('写十万字长篇悬疑')).toMatchObject({
      targetTotalWords: 100000,
      targetTotalWordsChoice: '100000',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写三万字都市奇幻')).toMatchObject({
      targetTotalWords: 30000,
      targetTotalWordsChoice: '30000',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写30w字赛博故事')).toMatchObject({
      targetTotalWords: 300000,
      targetTotalWordsChoice: 'custom',
      customTargetTotalWords: '300000'
    });
  });

  it('requires confirmation for vague length labels and per-chapter counts', () => {
    expect(inferCoCreateIntakeFromInitial('写一部长篇悬疑小说')).toMatchObject({
      targetTotalWords: 0,
      targetTotalWordsChoice: '',
      structureChoice: 'auto'
    });
    expect(inferCoCreateIntakeFromInitial('写一个故事，每章5000字')).toMatchObject({
      targetTotalWords: 0,
      targetTotalWordsChoice: ''
    });
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
      RuntimeState: 'running',
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

  it('preserves replayed event rows when project snapshot resolves after event replay', () => {
    const previous = {
      lastSeq: 2,
      eventRows: [
        { seq: 1, event: { running: false, agent: 'editor', summary: 'reviewed' } },
        { seq: 2, event: { running: true, agent: 'architect', summary: 'planning volume' } }
      ],
      streamRounds: [{ id: 'round-0', text: 'draft preview' }],
      snapshot: null
    };

    const next = restoreProjectWorkbenchSnapshot(previous, {
      RuntimeState: 'running',
      CompletedCount: 1
    });

    expect(next).toMatchObject({
      lastSeq: 2,
      eventRows: previous.eventRows,
      streamRounds: previous.streamRounds,
      snapshot: { RuntimeState: 'running', CompletedCount: 1 }
    });
  });

  it('restores event rows from project history when opening a running project', () => {
    const next = restoreProjectWorkbenchSnapshot({
      lastSeq: 0,
      eventRows: [],
      streamRounds: [{ id: 'round-0', text: '' }],
      snapshot: null
    }, {
      RuntimeState: 'running',
      CompletedCount: 1
    }, [
      {
        seq: 11,
        type: 'host_event',
        host_event_id: 'analysis-1',
        event: { id: 'analysis-1', running: true, agent: 'web', summary: 'analyzing source' }
      }
    ]);

    expect(next.lastSeq).toBe(11);
    expect(next.eventRows).toHaveLength(1);
    expect(next.eventRows[0].event.summary).toBe('analyzing source');
    expect(next.snapshot).toMatchObject({ RuntimeState: 'running' });
  });

  it('ignores stale running event rows when the latest snapshot is idle', () => {
    const progress = deriveWorkspaceProgress({
      RuntimeState: 'idle',
      StatusLabel: 'READY',
      CompletedCount: 1,
      TotalChapters: 5,
      TotalWordCount: 5000,
      Agents: []
    }, [
      { seq: 1, event: { running: true, agent: 'web', summary: 'generating proposal' } }
    ]);

    expect(progress.runningLabel).toBe('idle');
  });

  it('detects running snapshots for pause controls', () => {
    expect(isProjectRunning({ IsRunning: true, RuntimeState: 'paused' })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'running', Agents: [] })).toBe(true);
    expect(isProjectRunning({ RuntimeState: 'paused', Agents: [{ Name: 'writer', State: 'idle' }] })).toBe(false);
    expect(isProjectRunning({ RuntimeState: '', Agents: [{ Name: 'writer', State: 'working' }] })).toBe(true);
  });

  it('keeps simulation analysis available during unrelated adaptation work', () => {
    const activeProject = { id: 'project-1' };

    expect(canRunSimulationAnalysis({
      activeProject,
      busy: false,
      simulation: { analysisStatus: 'idle', importStatus: 'idle' }
    })).toBe(true);
    expect(canRunSimulationAnalysis({
      activeProject,
      busy: false,
      simulation: { analysisStatus: 'running', importStatus: 'idle' }
    })).toBe(false);
    expect(canRunSimulationAnalysis({
      activeProject,
      busy: true,
      simulation: { analysisStatus: 'idle', importStatus: 'idle' }
    })).toBe(true);
  });

  it('keeps adaptation analysis available while simulation preparation is running', () => {
    const activeProject = { id: 'project-1' };
    const adaptation = {
      sourceFile: { relative_path: 'source.txt' },
      analysisStatus: 'idle'
    };

    expect(isSimulationProfileActionBusy({ analysisStatus: 'running' })).toBe(true);
    expect(canRunAdaptationAnalysis({ activeProject, busy: false, adaptation })).toBe(true);
    expect(canRunAdaptationAnalysis({ activeProject, busy: true, adaptation })).toBe(true);
    expect(canRunAdaptationAnalysis({
      activeProject,
      busy: false,
      adaptation: { ...adaptation, analysisStatus: 'running' }
    })).toBe(false);
  });

  it('lets analyzed adaptation sources accept a new novel library name before saving', () => {
    const activeProject = { id: 'project-1' };
    const adaptation = {
      sourceFile: { relative_path: 'sources/source.txt' },
      analysisStatus: 'done',
      librarySaveName: '',
      libraryLoadedName: ''
    };

    expect(canSaveAnalyzedNovelToLibrary({ activeProject, busy: false, adaptation })).toBe(true);
    expect(canSaveAnalyzedNovelToLibrary({
      activeProject,
      busy: false,
      adaptation: { ...adaptation, analysisStatus: 'running' }
    })).toBe(false);
    expect(canSaveAnalyzedNovelToLibrary({ activeProject, busy: true, adaptation })).toBe(false);
  });

  it('treats running co-create as current-project busy without requiring global busy', () => {
    expect(isCoCreateRequestBusy({ status: 'running' })).toBe(true);
    expect(isCoCreateRequestBusy({ status: 'waiting' })).toBe(false);
    expect(isCoCreateRequestBusy({ status: 'started' })).toBe(false);
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

  it('shows completed-book chapter revision only after complete phase with outline rows', () => {
    expect(getCompletedBookChapterRevisionView({
      phase: 'complete',
      outline: [{ chapter: 1, title: 'Opening' }]
    })).toMatchObject({
      visible: true,
      phase: 'complete'
    });

    expect(getCompletedBookChapterRevisionView({
      phase: 'writing',
      outline: [{ chapter: 1, title: 'Opening' }]
    }).visible).toBe(false);

    expect(getCompletedBookChapterRevisionView({
      phase: 'complete',
      outline: []
    }).visible).toBe(false);
  });

  it('selects completed-book revision workspace chapter content', () => {
    const snapshot = {
      phase: 'complete',
      outline: [
        { chapter: 1, title: 'Opening', content: 'Chapter one body' },
        { Chapter: 3, Title: 'Reveal', Text: 'Chapter three body' }
      ]
    };

    expect(getCompletedBookSelectedChapterView(snapshot, { chapter: '3' })).toMatchObject({
      visible: true,
      chapter: 3,
      title: 'Reveal',
      content: 'Chapter three body'
    });

    expect(getCompletedBookSelectedChapterView(snapshot, { chapter: '99' })).toMatchObject({
      chapter: 1,
      title: 'Opening',
      content: 'Chapter one body'
    });
  });

  it('hides selected chapter workspace view outside completed books', () => {
    expect(getCompletedBookSelectedChapterView({
      phase: 'writing',
      outline: [{ chapter: 1, title: 'Opening', body: 'Draft body' }]
    }, { chapter: '1' })).toMatchObject({
      visible: false,
      chapter: 0,
      content: ''
    });
  });

  it('builds completed-book single chapter revision payloads', () => {
    const snapshot = {
      phase: 'complete',
      outline: [
        { chapter: 1, title: 'Opening' },
        { chapter: 3, title: 'Reveal' }
      ]
    };

    expect(buildChapterRevisionPayload({
      chapter: '3',
      mode: 'polish',
      instruction: '  keep the plot but smooth the prose  '
    }, snapshot)).toEqual({
      ok: true,
      body: {
        chapter: 3,
        mode: 'polish',
        instruction: 'keep the plot but smooth the prose'
      }
    });

    expect(buildChapterRevisionPayload({
      chapter: '1',
      mode: 'unknown',
      instruction: 'rewrite the opening'
    }, snapshot).body.mode).toBe('rewrite');
  });

  it('validates completed-book chapter revision payloads', () => {
    const snapshot = {
      phase: 'complete',
      outline: [{ chapter: 1, title: 'Opening' }]
    };

    expect(buildChapterRevisionPayload({
      chapter: '1',
      instruction: ''
    }, snapshot)).toMatchObject({ ok: false });

    expect(buildChapterRevisionPayload({
      chapter: '2',
      instruction: 'rewrite'
    }, snapshot)).toMatchObject({ ok: false });

    expect(buildChapterRevisionPayload({
      chapter: '1',
      instruction: 'rewrite'
    }, { ...snapshot, phase: 'writing' })).toMatchObject({ ok: false });
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

  it('deduplicates proposal volumes mirrored in summary and plan', () => {
    const review = getAdaptationProposalReview({
      ProposalSummary: {
        Status: 'proposal',
        ChapterCount: 4,
        Volumes: [
          { Index: 1, Title: 'Opening summary', TargetFrom: 1, TargetTo: 2 },
          { Index: 2, Title: 'Ending summary', TargetFrom: 3, TargetTo: 4 }
        ]
      },
      AdaptationProposal: {
        status: 'proposal',
        chapters: [
          { chapter: 1, title: 'One' },
          { chapter: 2, title: 'Two' },
          { chapter: 3, title: 'Three' },
          { chapter: 4, title: 'Four' }
        ],
        volumes: [
          { index: 1, title: 'Opening plan', target_from: 1, target_to: 2, source_from: 1, source_to: 1 },
          { index: 2, title: 'Ending plan', target_from: 3, target_to: 4, source_from: 2, source_to: 2 }
        ]
      }
    });

    expect(review.volumes).toHaveLength(2);
    expect(review.volumes.map((volume) => volume.index)).toEqual([1, 2]);
    expect(review.volumes.map((volume) => volume.title)).toEqual(['Opening plan', 'Ending plan']);
    expect(review.volumes.map((volume) => [volume.targetFrom, volume.targetTo])).toEqual([[1, 2], [3, 4]]);
  });

  it('surfaces staged volume review before detailed chapter proposal', () => {
    const review = getAdaptationProposalReview({
      proposal_summary: {
        status: 'proposal',
        granularity: 'chapter',
        rewrite_policy: 'preserve_mainline',
        brief: 'slow-burn mystery'
      },
      volume_review: {
        volumes: [
          {
            index: 1,
            title: 'Opening arc',
            target_from: 1,
            target_to: 6,
            source_from: 1,
            source_to: 20,
            plot: 'Move the discovery earlier.',
            key_beats: ['discovery', 'choice']
          }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(true);
    expect(review.chapterCount).toBe(6);
    expect(review.volumeReview.volumes[0]).toMatchObject({
      index: 1,
      title: 'Opening arc',
      targetFrom: 1,
      targetTo: 6,
      plot: 'Move the discovery earlier.',
      beats: ['discovery', 'choice']
    });
  });

  it('restores staged volume review from backend snapshot fields', () => {
    const review = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'restore staged plan',
        TargetChapterCount: 20,
        Volumes: [
          { Index: 1, Title: 'Opening volume', TargetFrom: 1, TargetTo: 8 }
        ]
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'restore staged plan',
        target_chapter_count: 20,
        volumes: [
          { index: 1, title: 'Opening volume', target_from: 1, target_to: 8 }
        ]
      }
    });

    expect(review.loaded).toBe(true);
    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(true);
    expect(review.granularity).toBe('free');
    expect(review.chapterCount).toBe(20);
    expect(review.volumeReview.volumes[0].title).toBe('Opening volume');
  });

  it('hides free-mode source anchors from adaptation proposal labels', () => {
    const chapterReview = getAdaptationProposalReview({
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        chapters: [
          {
            chapter: 1,
            title: 'Opening',
            source_chapters: [17],
            source_range: { from: 17, to: 17 }
          }
        ]
      }
    });
    const volumeReview = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        Granularity: 'free',
        TargetChapterCount: 8
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        granularity: 'free',
        volumes: [
          {
            index: 1,
            title: 'Opening volume',
            target_from: 1,
            target_to: 8,
            source_from: 17,
            source_to: 17
          }
        ]
      }
    });

    expect(chapterReview.chapters[0].sourceCoverage).toMatchObject({ from: 17, to: 17, chapters: [17] });
    expect(formatAdaptationSourceCoverageLabel(chapterReview.chapters[0].sourceCoverage, chapterReview.granularity)).toBe('');
    expect(formatAdaptationSourceCoverageLabel({ isAdded: true }, chapterReview.granularity, { addedLabel: '\u65b0\u589e\u6865\u6bb5' })).toBe('\u65b0\u589e\u6865\u6bb5');
    expect(volumeReview.volumeReviewReady).toBe(true);
    expect(volumeReview.volumeReview.volumes[0]).toMatchObject({ sourceFrom: 17, sourceTo: 17 });
    expect(formatAdaptationVolumeSourceLabel(volumeReview.volumeReview.volumes[0], volumeReview.granularity)).toBe('');
  });

  it('keeps source mapping labels for chapter and arc adaptation proposal modes', () => {
    expect(formatAdaptationSourceCoverageLabel({ from: 2, to: 4 }, 'chapter')).toBe('\u539f 2-4');
    expect(formatAdaptationSourceCoverageLabel({ chapters: [2, 4] }, 'arc')).toBe('\u539f 2,4');
    expect(formatAdaptationVolumeSourceLabel({ sourceFrom: 1, sourceTo: 20 }, 'arc')).toBe('\u539f 1-20');
    expect(formatAdaptationVolumeSourceLabel({ sourceLabel: '\u539f 3-9' }, 'chapter')).toBe('\u539f 3-9');
  });

  it('deduplicates staged volume review mirrored in summary and review payload', () => {
    const review = getAdaptationProposalReview({
      VolumeReviewSummary: {
        Status: 'volume_review',
        TargetChapterCount: 10,
        Volumes: [
          { Index: 1, Title: 'Dreamer', TargetFrom: 1, TargetTo: 2 },
          { Index: 2, Title: 'Shards', TargetFrom: 3, TargetTo: 4 }
        ]
      },
      AdaptationVolumeReview: {
        status: 'volume_review',
        volumes: [
          { index: 1, title: 'Dreamer', target_from: 1, target_to: 2, plot: 'Keep the opening intact.' },
          { index: 2, title: 'Shards', target_from: 3, target_to: 4, plot: 'Escalate the middle.' }
        ]
      }
    });

    expect(review.volumeReview.volumes).toHaveLength(2);
    expect(review.volumeReview.volumes.map((volume) => volume.index)).toEqual([1, 2]);
    expect(review.volumeReview.volumes.map((volume) => volume.title)).toEqual(['Dreamer', 'Shards']);
    expect(review.volumeReview.volumes[0].plot).toBe('Keep the opening intact.');
  });

  it('uses detailed chapters instead of staged volume review once details exist', () => {
    const review = getAdaptationProposalReview({
      volume_review: {
        volumes: [{ index: 1, title: 'Opening arc', target_from: 1, target_to: 2 }]
      },
      adaptation_proposal: {
        status: 'proposal',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    });

    expect(review.proposalReady).toBe(true);
    expect(review.volumeReviewReady).toBe(false);
    expect(review.chapters).toHaveLength(2);
  });

  it('builds staged volume-review revision payloads', () => {
    expect(buildVolumeReviewRevisionPayload({
      revisionVolume: '2',
      revisionInstruction: 'raise the midpoint stakes'
    }, {
      volumes: [{ index: 1 }, { index: 2 }]
    })).toEqual({
      ok: true,
      body: {
        volume_index: 2,
        instruction: 'raise the midpoint stakes'
      }
    });

    expect(buildVolumeReviewRevisionPayload({
      revisionVolume: '99',
      revisionInstruction: 'fallback to first visible volume'
    }, {
      volumes: [{ index: 4 }]
    }).body.volume_index).toBe(4);
  });

  it('builds normal co-create planning revision payloads', () => {
    expect(buildCoCreatePlanningRevisionPayload({
      feedback: '  make the heroine proactive  '
    })).toEqual({
      ok: true,
      body: {
        feedback: 'make the heroine proactive'
      }
    });

    expect(buildCoCreatePlanningRevisionPayload({
      instruction: 'tighten the opening'
    }).body.feedback).toBe('tighten the opening');
    expect(buildCoCreatePlanningRevisionPayload({ feedback: '   ' })).toEqual({
      ok: false,
      error: '请输入审核意见'
    });
  });

  it('keeps normal co-create planning review visible while pending or regenerating', () => {
    const pending = getCoCreatePlanningReview({
      planning_review: {
        status: 'pending',
        kind: 'chapter_outline',
        brief: 'review me',
        target_total_words: 5000
      },
      outline: [{ chapter: 1, title: 'Opening' }]
    });
    expect(pending.active).toBe(true);
    expect(pending.pending).toBe(true);
    expect(pending.collecting).toBe(false);

    const collecting = getCoCreatePlanningReview({
      PlanningReview: {
        Status: 'collecting',
        Kind: 'volume_split',
        Brief: 'regenerating',
        TargetTotalWords: 8000
      },
      Outline: [{ Chapter: 1, Title: 'Opening' }]
    });
    expect(collecting.active).toBe(true);
    expect(collecting.pending).toBe(false);
    expect(collecting.collecting).toBe(true);
    expect(collecting.revising).toBe(true);
  });

  it('restores visible adaptation proposal state from a co-create commit snapshot', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Make the mystery structure richer',
        ChapterCount: 2
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Make the mystery structure richer',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    };
    const previous = {
      sourceFile: { relative_path: 'source.txt' },
      mode: 'chapter',
      brief: '',
      proposalKey: '',
      startStatus: 'idle',
      startMessage: '',
      error: 'old error'
    };

    const next = applyAdaptationProposalSnapshot(previous, snapshot);

    expect(next.mode).toBe('free');
    expect(next.brief).toBe('Make the mystery structure richer');
    expect(next.error).toBe('');
    expect(next.proposalKey).toBe(buildAdaptationProposalKey(next));
    expect(getVisibleAdaptationProposalReview(snapshot, next).proposalReady).toBe(true);
  });

  it('restores a saved proposal even when the uploaded source file cannot be reconstructed', () => {
    const snapshot = {
      ProposalSummary: {
        Status: 'proposal',
        Granularity: 'free',
        RewritePolicy: 'full_rewrite',
        Brief: 'Keep the mystery safe and long-form',
        ChapterCount: 2
      },
      AdaptationProposal: {
        status: 'proposal',
        granularity: 'free',
        rewrite_policy: 'full_rewrite',
        brief: 'Keep the mystery safe and long-form',
        chapters: [
          { chapter: 1, title: 'Opening' },
          { chapter: 2, title: 'Reveal' }
        ]
      }
    };

    const next = applyAdaptationProposalSnapshot({
      sourceFile: null,
      mode: 'chapter',
      brief: '',
      proposalKey: '',
      startStatus: 'idle',
      startMessage: '',
      error: ''
    }, snapshot);

    expect(next.sourceFile).toBeNull();
    expect(next.mode).toBe('free');
    expect(next.proposalKey).toBe(buildAdaptationProposalKey(next));
    expect(getVisibleAdaptationProposalReview(snapshot, next).proposalReady).toBe(true);
    expect(getVisibleAdaptationProposalReview(snapshot, { ...next, brief: 'changed' }).stale).toBe(true);
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

  it('builds revision payloads for chapter, range, and volume targets', () => {
    const proposal = { chapterCount: 12, volumes: [{ index: 1 }, { index: 2 }] };

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'chapter',
      revisionChapter: '3',
      revisionInstruction: 'strengthen the hook'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第3章',
        from_chapter: 3,
        to_chapter: 3,
        instruction: 'strengthen the hook'
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'range',
      revisionFromChapter: '8',
      revisionToChapter: '5',
      revisionInstruction: 'smooth the arc'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第5-8章',
        from_chapter: 5,
        to_chapter: 8
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'volume',
      revisionVolume: 'all',
      revisionInstruction: 'rebalance all volumes'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '全卷',
        volume_index: -1
      }
    });

    expect(buildAdaptationRevisionPayload({
      revisionMode: 'volume',
      revisionVolume: '2',
      revisionInstruction: 'raise the midpoint stakes'
    }, proposal)).toMatchObject({
      ok: true,
      body: {
        target: '第2卷',
        volume_index: 2
      }
    });
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

  it('does not start the simulation library save flow automatically when analysis completes', () => {
    const next = applyHostEventToSimulationState({
      files: [],
      uploadMessage: '',
      analysisStatus: 'running',
      analysisEvents: [],
      importStatus: 'idle',
      importEvents: [],
      importMessage: '',
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: '',
      saveName: '',
      saveStatus: 'running',
      saveError: 'old error',
      error: ''
    }, {
      type: 'host_event',
      event: {
        category: 'SIMULATE',
        kind: 'done',
        level: 'success',
        summary: '仿写画像已完成'
      }
    });

    expect(next.analysisStatus).toBe('done');
    expect(next.saveStatus).toBe('idle');
    expect(next.saveError).toBe('');
  });

  it('keeps import merge progress in the load-profile workflow', () => {
    const next = applyHostEventToSimulationState({
      files: [],
      uploadMessage: '',
      analysisStatus: 'idle',
      analysisEvents: [],
      importStatus: 'running',
      importEvents: [],
      importMessage: '',
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: '',
      saveName: '',
      saveStatus: 'idle',
      saveError: '',
      error: ''
    }, {
      type: 'host_event',
      event: {
        category: 'SIMULATE',
        kind: 'merge',
        level: 'info',
        summary: '分批重合成仿写画像 24/49'
      }
    });

    expect(next.importStatus).toBe('running');
    expect(next.importEvents).toHaveLength(1);
    expect(next.importEvents[0].message).toContain('分批重合成');
    expect(next.analysisStatus).toBe('idle');
    expect(next.analysisEvents).toHaveLength(0);
  });

  it('restores uploaded simulation source files from refresh responses', () => {
    expect(simulationFilesFromResponse({
      files: [
        { name: 'a-source.txt', size: 12, relative_path: 'a-source.txt' },
        { Name: 'b-source.md', Size: 34, RelativePath: 'b-source.md' }
      ]
    })).toEqual([
      {
        name: 'a-source.txt',
        original_name: 'a-source.txt',
        size: 12,
        relative_path: 'a-source.txt'
      },
      {
        name: 'b-source.md',
        original_name: 'b-source.md',
        size: 34,
        relative_path: 'b-source.md'
      }
    ]);

    expect(simulationFilesFromResponse({ source_files: ['nested/c-source.txt'] })).toEqual([
      {
        name: 'c-source.txt',
        original_name: 'c-source.txt',
        size: 0,
        relative_path: 'nested/c-source.txt'
      }
    ]);
  });

  it('restores running simulation analysis state from project snapshots', () => {
    const next = restoreSimulationProjectState({
      libraryQuery: '',
      libraryStatus: 'idle',
      libraryItems: [],
      libraryMessage: '',
      libraryError: ''
    }, {
      analysis_status: 'running',
      analysis_events: [{ stage: 'analyze', message: '分析仿写语料 1/10' }],
      import_status: 'idle',
      files: [{ name: 'part_001.txt', size: 42, relative_path: 'part_001.txt' }]
    });

    expect(next.analysisStatus).toBe('running');
    expect(next.analysisEvents).toHaveLength(1);
    expect(next.files[0].name).toBe('part_001.txt');
  });

  it('shows project default model instead of the global runtime default when a project is active', () => {
    const visible = resolveVisibleDefaultModel(
      { id: 'project-1' },
      { config: { provider: 'custom-openai', model: 'deepseek-v4-pro' } },
      {
        providers: [
          { name: 'custom-openai', models: ['deepseek-v4-pro'] },
          { name: 'deepseek', models: ['deepseek-v4-pro'] }
        ],
        roles: [
          { role: 'default', provider: 'deepseek', model: 'deepseek-v4-pro', explicit: true }
        ]
      }
    );

    expect(visible.provider).toBe('deepseek');
    expect(visible.model).toBe('deepseek-v4-pro');
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
