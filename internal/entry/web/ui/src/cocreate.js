export function createCoCreateState() {
  return {
    kind: 'normal',
    active: false,
    input: '',
    messages: [],
    draftPrompt: '',
    ready: false,
    suggestions: [],
    streamThinking: '',
    streamReply: '',
    status: 'idle',
    startMessage: '',
    error: '',
    adaptMode: '',
    rewritePolicy: '',
    modeLocked: false
  };
}

export function coCreateStateFromResponse(response, previous = createCoCreateState(), options = {}) {
  return coCreateStateFromBackend(response?.cocreate || {}, previous, options);
}

export function coCreateStateFromEvent(event, previous = createCoCreateState()) {
  return coCreateStateFromBackend(event?.cocreate || {}, previous, {
    preserveError: true,
    preserveInput: true
  });
}

export function coCreateStateFromError(error, previous = createCoCreateState()) {
  return coCreateStateFromBackend(error?.data?.cocreate || {}, previous, {
    preserveInput: true,
    status: 'error',
    error: error?.message || 'co-create failed'
  });
}

export function coCreateStateFromBackend(data, previous = createCoCreateState(), options = {}) {
  const hasBackendState = Boolean(data && Object.keys(data).length);
  if (!hasBackendState) {
    return {
      ...previous,
      status: options.status || previous.status,
      error: options.error ?? previous.error
    };
  }
  const status = options.status || (data.committed_label ? 'started' : data.ready ? 'ready' : data.active ? 'running' : 'idle');
  return {
    ...previous,
    kind: data.kind || previous.kind || 'normal',
    active: Boolean(data.active && !data.committed_label),
    messages: data.messages || [],
    draftPrompt: data.draft_prompt || '',
    ready: Boolean(data.ready),
    suggestions: data.suggestions || [],
    streamThinking: data.stream_thinking || '',
    streamReply: data.stream_reply || '',
    status,
    startMessage: data.committed_label || previous.startMessage || '',
    error: options.error ?? (options.preserveError ? previous.error : ''),
    adaptMode: data.adapt_mode || '',
    rewritePolicy: data.rewrite_policy || '',
    modeLocked: Boolean(data.mode_locked),
    input: options.preserveInput ? previous.input : ''
  };
}

export function applyCoCreateSuggestion(state, suggestion) {
  return {
    ...state,
    input: String(suggestion || '')
  };
}

export function appendCoCreateInput(state, text) {
  return {
    ...state,
    input: text
  };
}
