export function createWorkbenchState() {
  return {
    lastSeq: 0,
    eventRows: [],
    streamRounds: [{ id: 'round-0', text: '' }],
    snapshot: null
  };
}

export function reduceWebEvent(state, event) {
  if (!event || event.seq <= state.lastSeq) {
    return state;
  }

  const next = {
    ...state,
    lastSeq: event.seq,
    eventRows: state.eventRows,
    streamRounds: state.streamRounds,
    snapshot: state.snapshot
  };

  if (event.type === 'host_event') {
    next.eventRows = mergeEventRows(state.eventRows, event);
  } else if (event.type === 'stream_delta') {
    next.streamRounds = appendStreamDelta(state.streamRounds, event.stream?.text || '');
  } else if (event.type === 'stream_clear') {
    next.streamRounds = startStreamRound(state.streamRounds);
  } else if (event.type === 'snapshot') {
    next.snapshot = event.snapshot || null;
  }

  return next;
}

export function mergeEventRows(rows, event) {
  if (!event?.host_event_id) {
    return [...rows, event];
  }
  const index = rows.findIndex((row) => row.host_event_id === event.host_event_id);
  if (index === -1) {
    return [...rows, event];
  }
  const next = rows.slice();
  next[index] = event;
  return next;
}

export function appendStreamDelta(rounds, text) {
  if (!text) {
    return rounds;
  }
  const next = rounds.length ? rounds.slice() : [{ id: 'round-0', text: '' }];
  const last = next[next.length - 1];
  next[next.length - 1] = {
    ...last,
    text: `${last.text || ''}${text}`
  };
  return next;
}

export function startStreamRound(rounds) {
  const next = rounds.length ? rounds.slice() : [];
  const last = next[next.length - 1];
  if (last && !String(last.text || '').trim()) {
    return next;
  }
  return [...next, { id: `round-${next.length}`, text: '' }];
}

export function visibleStreamRounds(rounds) {
  if (!Array.isArray(rounds) || rounds.length === 0) {
    return [{ id: 'round-0', text: '' }];
  }
  const last = rounds[rounds.length - 1];
  if (String(last?.text || '').trim()) {
    return [last];
  }
  for (let index = rounds.length - 2; index >= 0; index -= 1) {
    const round = rounds[index];
    if (String(round?.text || '').trim()) {
      return [round];
    }
  }
  return [last];
}

export function eventStatus(event) {
  if (event?.event?.running) {
    return 'running';
  }
  if (event?.event?.failed || event?.event?.level === 'error') {
    return 'error';
  }
  if (event?.event?.level === 'warn') {
    return 'warn';
  }
  if (event?.event?.level === 'success') {
    return 'success';
  }
  return 'info';
}
