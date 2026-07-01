import { describe, expect, it } from 'vitest';
import {
  appendStreamDelta,
  createWorkbenchState,
  mergeEventRows,
  reduceWebEvent,
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

  it('keeps stream clear and delta rows stable', () => {
    const one = appendStreamDelta([{ id: 'round-0', text: '' }], '第一段');
    const cleared = startStreamRound(one);
    const two = appendStreamDelta(cleared, '第二段');

    expect(two).toEqual([
      { id: 'round-0', text: '第一段' },
      { id: 'round-1', text: '第二段' }
    ]);
  });
});
