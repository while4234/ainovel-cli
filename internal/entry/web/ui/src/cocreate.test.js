import { describe, expect, it } from 'vitest';
import {
  applyCoCreateSuggestion,
  appendCoCreateInput,
  coCreateStateFromError,
  coCreateStateFromEvent,
  coCreateStateFromResponse,
  createCoCreateState,
  visibleCoCreateSuggestions
} from './cocreate.js';

describe('co-create UI state', () => {
  it('fills a clicked suggestion into editable input', () => {
    const state = {
      ...createCoCreateState(),
      suggestions: ['加强女主线', '改成双主角']
    };

    const next = applyCoCreateSuggestion(state, state.suggestions[1]);
    const edited = appendCoCreateInput(next, `${next.input}，但保留慢热节奏`);

    expect(next.input).toBe('改成双主角');
    expect(edited.input).toBe('改成双主角，但保留慢热节奏');
  });

  it('tracks whether pending input came from a suggestion', () => {
    const suggested = applyCoCreateSuggestion(createCoCreateState(), 'keep the bittersweet ending');
    const edited = appendCoCreateInput(suggested, 'keep the bittersweet ending, but soften the final scene');

    expect(suggested.input).toBe('keep the bittersweet ending');
    expect(suggested.inputSource).toBe('suggestion');
    expect(edited.inputSource).toBe('custom');
  });

  it('accepts free input without requiring a suggestion', () => {
    const state = appendCoCreateInput(createCoCreateState(), '我想要更强的宿命感');

    expect(state.input).toBe('我想要更强的宿命感');
  });

  it('preserves ready draft and locked adapt mode from backend response', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        draft_prompt: '## 改编 brief',
        ready: true,
        suggestions: [],
        adapt_mode: 'arc',
        rewrite_policy: 'full_rewrite',
        mode_locked: true
      }
    });

    expect(state.status).toBe('ready');
    expect(state.draftPrompt).toBe('## 改编 brief');
    expect(state.adaptMode).toBe('arc');
    expect(state.rewritePolicy).toBe('full_rewrite');
    expect(state.modeLocked).toBe(true);
  });

  it('allows a draft prompt to be started even when the model keeps ready false', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        draft_prompt: '## 改编 brief\n- 已经可以执行',
        ready: false,
        suggestions: []
      }
    });

    expect(state.status).toBe('ready');
    expect(state.ready).toBe(false);
    expect(state.canStart).toBe(true);
    expect(state.draftPrompt).toContain('已经可以执行');
  });

  it('keeps a backend-blocked draft visible but not startable', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        draft_prompt: '## 改编 brief\n- 需要继续合并最新意见',
        ready: true,
        can_start: false,
        suggestions: []
      }
    });

    expect(state.status).toBe('waiting');
    expect(state.ready).toBe(true);
    expect(state.canStart).toBe(false);
    expect(state.draftPrompt).toContain('继续合并');
  });

  it('preserves editable message metadata from backend response', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'normal',
        active: true,
        messages: [
          { id: 'm1', role: 'user', content: '写月城悬疑', editable: true, source: 'custom' },
          { id: 'm2', role: 'assistant', content: '先确认主角。' },
          { id: 'm3', role: 'user', content: '加强女主线', editable: true, source: 'suggestion' }
        ],
        ready: false,
        suggestions: []
      }
    });

    expect(state.messages[0]).toMatchObject({ id: 'm1', editable: true, source: 'custom' });
    expect(state.messages[2]).toMatchObject({ id: 'm3', editable: true, source: 'suggestion' });
  });

  it('shows parsed suggestions while waiting for user input', () => {
    const state = coCreateStateFromResponse({
      cocreate: {
        kind: 'adapt',
        active: true,
        messages: [
          { id: 'm1', role: 'system', content: 'adapt co-create started' },
          { id: 'm2', role: 'assistant', content: '请选择方向。' }
        ],
        ready: false,
        suggestions: [
          '保持黑暗虐心但稍微调整女主心理线',
          '改成双向救赎结局，让女主活下来',
          '削弱性虐尺度，加强情感拉扯'
        ]
      }
    });

    expect(state.status).toBe('waiting');
    expect(state.suggestions).toEqual([
      '保持黑暗虐心但稍微调整女主心理线',
      '改成双向救赎结局，让女主活下来',
      '削弱性虐尺度，加强情感拉扯'
    ]);
  });

  it('extracts fallback suggestions from numbered assistant questions', () => {
    const suggestions = visibleCoCreateSuggestions({
      messages: [
        {
          role: 'assistant',
          content: [
            '1. **感情基调定位**：你希望保持这种黑暗残酷的基调，还是往纯爱方向调整？或者某种中间态（比如虐恋+救赎）？',
            '2. **女主袁可欣的结局**：你希望保留这个悲剧结局，还是打算改变她的命运？'
          ].join('\n')
        }
      ],
      suggestions: null
    });

    expect(suggestions).toEqual([
      '保持这种黑暗残酷的基调',
      '往纯爱方向调整',
      '某种中间态（比如虐恋+救赎）'
    ]);
  });

  it('adds context to short fallback choices from assistant questions', () => {
    const suggestions = visibleCoCreateSuggestions({
      messages: [
        {
          role: 'assistant',
          content: '- **结局改动**：原书结局是袁可欣自杀、安少廷陷入循环。你想保留、反转还是拓展？'
        }
      ],
      suggestions: null
    });

    expect(suggestions).toEqual(['结局改动：保留', '结局改动：反转', '结局改动：拓展']);
  });

  it('extracts fallback suggestions from paragraph choice questions', () => {
    const suggestions = visibleCoCreateSuggestions({
      messages: [
        {
          role: 'assistant',
          content: [
            '当前剧本尚缺一个关键决策：**原著中“梦游”设定如何处理？**',
            '如果保留梦游设定，如何与拟以前被很多人那样对待的 NTR 源头共存？',
            '还是你想把梦游淡化为噩梦或幻觉？'
          ].join('\n')
        }
      ],
      suggestions: null
    });

    expect(suggestions).toEqual([
      '保留梦游设定',
      '把梦游淡化为噩梦或幻觉'
    ]);
  });

  it('merges stream progress without duplicating assistant messages or clearing errors', () => {
    const previous = {
      ...createCoCreateState(),
      active: true,
      error: 'previous error',
      messages: [{ role: 'user', content: '写一个月城悬疑' }]
    };

    const state = coCreateStateFromEvent({
      type: 'cocreate_state',
      cocreate: {
        kind: 'normal',
        active: true,
        messages: previous.messages,
        stream_thinking: 'checking premise',
        stream_reply: '先确认主角目标',
        ready: false,
        suggestions: []
      }
    }, previous);

    expect(state.status).toBe('running');
    expect(state.error).toBe('previous error');
    expect(state.messages).toEqual(previous.messages);
    expect(state.streamThinking).toBe('checking premise');
    expect(state.streamReply).toBe('先确认主角目标');
  });

  it('keeps backend co-create state on begin or send errors', () => {
    const previous = {
      ...createCoCreateState(),
      input: 'retry text'
    };
    const error = new Error('stream failed');
    error.data = {
      cocreate: {
        kind: 'stage',
        active: true,
        messages: [{ role: 'system', content: 'stage paused' }],
        draft_prompt: '',
        ready: false,
        suggestions: []
      }
    };

    const state = coCreateStateFromError(error, previous);

    expect(state.status).toBe('error');
    expect(state.error).toBe('stream failed');
    expect(state.active).toBe(true);
    expect(state.kind).toBe('stage');
    expect(state.input).toBe('retry text');
  });
});
