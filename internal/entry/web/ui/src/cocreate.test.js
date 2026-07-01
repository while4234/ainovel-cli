import { describe, expect, it } from 'vitest';
import {
  applyCoCreateSuggestion,
  appendCoCreateInput,
  coCreateStateFromError,
  coCreateStateFromEvent,
  coCreateStateFromResponse,
  createCoCreateState
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
