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
    next.snapshot = mergeSnapshotUpdate(state.snapshot, event.snapshot, event.workflow_progress);
  } else if (event.workflow_progress) {
    next.snapshot = mergeWorkflowProgress(state.snapshot, event.workflow_progress);
  }

  return next;
}

export function mergeWorkflowProgress(snapshot, workflowProgress) {
  if (!snapshot) {
    return workflowProgress ? { workflow_progress: workflowProgress } : null;
  }
  if (!workflowProgress || snapshot.workflow_progress === workflowProgress) {
    return snapshot;
  }
  return {
    ...snapshot,
    workflow_progress: workflowProgress
  };
}

export function mergeSnapshotUpdate(previousSnapshot, incomingSnapshot, workflowProgress) {
  if (!incomingSnapshot) {
    return mergeWorkflowProgress(previousSnapshot, workflowProgress);
  }
  const progress = workflowProgress ||
    incomingSnapshot.workflow_progress ||
    incomingSnapshot.WorkflowProgress ||
    previousSnapshot?.workflow_progress ||
    previousSnapshot?.WorkflowProgress;
  return mergeWorkflowProgress(incomingSnapshot, progress);
}

export function reduceWebEvents(state, events = []) {
  if (!Array.isArray(events) || events.length === 0) {
    return state;
  }
  return events.reduce((next, event) => reduceWebEvent(next, event), state);
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
  return compactStreamRounds(next);
}

export function startStreamRound(rounds) {
  const next = rounds.length ? rounds.slice() : [];
  const last = next[next.length - 1];
  if (last && !String(last.text || '').trim()) {
    return next;
  }
  return compactStreamRounds([...next, { id: `round-${next.length}`, text: '' }]);
}

export function compactStreamRounds(rounds, limit = 12) {
  const compacted = [];
  for (const round of rounds || []) {
    const text = String(round?.text || '');
    const isEmpty = !text.trim();
    if (isEmpty) {
      if (round === rounds[rounds.length - 1] || compacted.length === 0) {
        compacted.push({ ...round, text: '' });
      }
      continue;
    }
    const previous = compacted[compacted.length - 1];
    if (previous && streamRoundsOverlap(previous.text, text)) {
      compacted[compacted.length - 1] =
        text.length >= String(previous.text || '').length ? { ...round, text } : previous;
      continue;
    }
    compacted.push({ ...round, text });
  }
  const last = compacted[compacted.length - 1];
  const nonEmpty = compacted.filter((round) => String(round.text || '').trim());
  const tail = nonEmpty.slice(-limit);
  if (last && !String(last.text || '').trim()) {
    return [...tail, last];
  }
  return tail.length ? tail : [{ id: 'round-0', text: '' }];
}

function streamRoundsOverlap(a, b) {
  const left = streamSignature(a);
  const right = streamSignature(b);
  if (left.length < 120 || right.length < 120) {
    return false;
  }
  const prefix = left.slice(0, Math.min(240, left.length));
  const otherPrefix = right.slice(0, Math.min(240, right.length));
  return right.startsWith(prefix) || left.startsWith(otherPrefix);
}

function streamSignature(text) {
  return String(text || '').replace(/\s+/g, '');
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
