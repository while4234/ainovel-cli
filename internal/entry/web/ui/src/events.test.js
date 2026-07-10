import { describe, expect, it } from 'vitest';
import {
  appendStreamDelta,
  compactStreamRounds,
  createWorkbenchState,
  mergeWorkflowProgress,
  mergeEventRows,
  reduceWebEvent,
  reduceWebEvents,
  startStreamRound
} from './events.js';

describe('web event reducer', () => {
  it('updates running host events with the same id in place', () => {
    const started = {
      seq: 1,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafting', running: true }
    };
    const finished = {
      seq: 2,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafted', running: false }
    };

    const rows = mergeEventRows(mergeEventRows([], started), finished);

    expect(rows).toHaveLength(1);
    expect(rows[0].event.summary).toBe('drafted');
    expect(rows[0].event.running).toBe(false);
  });

  it('ignores duplicate seq events after reconnect', () => {
    const initial = createWorkbenchState();
    const first = reduceWebEvent(initial, {
      seq: 4,
      type: 'stream_delta',
      stream: { text: 'alpha' }
    });
    const duplicate = reduceWebEvent(first, {
      seq: 4,
      type: 'stream_delta',
      stream: { text: 'alpha' }
    });

    expect(duplicate.streamRounds[0].text).toBe('alpha');
  });

  it('keeps top-level SSE workflow progress inside the current snapshot', () => {
    const workflowProgress = {
      workflow: 'continuation',
      status: 'running',
      steps: [{ id: 'writing', label: '续写正文', status: 'running' }]
    };
    const next = reduceWebEvent(createWorkbenchState(), {
      seq: 1,
      type: 'snapshot',
      snapshot: { runtime_state: 'running' },
      workflow_progress: workflowProgress
    });

    expect(next.snapshot.runtime_state).toBe('running');
    expect(next.snapshot.workflow_progress).toBe(workflowProgress);
    expect(mergeWorkflowProgress(null, workflowProgress)).toEqual({ workflow_progress: workflowProgress });
  });

  it('replays event history without duplicating stale events', () => {
    const initial = reduceWebEvent(createWorkbenchState(), {
      seq: 4,
      type: 'host_event',
      host_event_id: 'tool-1',
      event: { id: 'tool-1', summary: 'drafting', running: true }
    });
    const restored = reduceWebEvents(initial, [
      {
        seq: 4,
        type: 'host_event',
        host_event_id: 'tool-1',
        event: { id: 'tool-1', summary: 'stale duplicate', running: true }
      },
      {
        seq: 5,
        type: 'host_event',
        host_event_id: 'tool-1',
        event: { id: 'tool-1', summary: 'drafted', running: false }
      },
      {
        seq: 6,
        type: 'host_event',
        host_event_id: 'tool-2',
        event: { id: 'tool-2', summary: 'reviewing', running: true }
      }
    ]);

    expect(restored.lastSeq).toBe(6);
    expect(restored.eventRows).toHaveLength(2);
    expect(restored.eventRows[0].event.summary).toBe('drafted');
    expect(restored.eventRows[1].event.summary).toBe('reviewing');
  });

  it('keeps stream clear and delta rows stable', () => {
    const one = appendStreamDelta([{ id: 'round-0', text: '' }], '第一段');
    const cleared = startStreamRound(one);
    const two = appendStreamDelta(cleared, '第二段');

    expect(two).toEqual([
      { id: 'round-0', text: '第一段' },
      { id: 'round-1', text: '第二段' }
    ]);
  });

  it('collapses repeated draft stream rounds after refresh', () => {
    const first = '雨水敲打通风管，霓虹在远处闪烁。'.repeat(12);
    const second = `${first}他终于拔出接口线，继续向节点深处移动。`;
    const rounds = compactStreamRounds([
      { id: 'round-0', text: first },
      { id: 'round-1', text: second }
    ]);

    expect(rounds).toHaveLength(1);
    expect(rounds[0].text).toBe(second);
  });
});
