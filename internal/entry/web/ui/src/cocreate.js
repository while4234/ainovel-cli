export function createCoCreateState() {
  return {
    kind: 'normal',
    active: false,
    input: '',
    inputSource: '',
    messages: [],
    draftPrompt: '',
    ready: false,
    canStart: false,
    suggestions: [],
    streamThinking: '',
    streamReply: '',
    intakeActive: false,
    intakeInitial: '',
    targetTotalWordsChoice: '',
    customTargetTotalWords: '',
    structureChoice: 'single',
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
  const messages = Array.isArray(data.messages) ? data.messages : [];
  const streamThinking = data.stream_thinking || '';
  const streamReply = data.stream_reply || '';
  const canStart = coCreateCanStartFromBackend(data);
  const suggestions = visibleCoCreateSuggestions({
    messages,
    suggestions: data.suggestions,
    streamReply
  });
  const status = options.status || coCreateStatusFromBackend(data, streamThinking, streamReply, canStart);
  return {
    ...previous,
    kind: data.kind || previous.kind || 'normal',
    active: Boolean(data.active && !data.committed_label),
    messages,
    draftPrompt: data.draft_prompt || '',
    ready: Boolean(data.ready),
    canStart,
    suggestions,
    streamThinking,
    streamReply,
    intakeActive: false,
    intakeInitial: '',
    targetTotalWordsChoice: previous.targetTotalWordsChoice || '',
    customTargetTotalWords: previous.customTargetTotalWords || '',
    structureChoice: previous.structureChoice || 'single',
    status,
    startMessage: data.committed_label || previous.startMessage || '',
    error: options.error ?? (options.preserveError ? previous.error : ''),
    adaptMode: data.adapt_mode || '',
    rewritePolicy: data.rewrite_policy || '',
    modeLocked: Boolean(data.mode_locked),
    input: options.preserveInput ? previous.input : '',
    inputSource: options.preserveInput ? previous.inputSource || '' : ''
  };
}

function coCreateCanStartFromBackend(data) {
  if (Object.prototype.hasOwnProperty.call(data, 'can_start')) {
    return Boolean(data.can_start);
  }
  return Boolean(data.ready || String(data.draft_prompt || '').trim());
}

function coCreateStatusFromBackend(data, streamThinking, streamReply, canStart) {
  if (data.committed_label) {
    return 'started';
  }
  if (canStart) {
    return 'ready';
  }
  if (streamThinking || streamReply) {
    return 'running';
  }
  return data.active ? 'waiting' : 'idle';
}

export function visibleCoCreateSuggestions({ suggestions = [], messages = [], streamReply = '' } = {}) {
  const explicit = normalizeCoCreateSuggestions(suggestions);
  if (explicit.length) {
    return explicit;
  }
  return extractCoCreateSuggestions(streamReply || latestAssistantMessage(messages));
}

function normalizeCoCreateSuggestions(suggestions) {
  if (!Array.isArray(suggestions)) {
    return [];
  }
  return uniqueNonEmpty(suggestions.map(cleanSuggestionText)).slice(0, 3);
}

function latestAssistantMessage(messages) {
  if (!Array.isArray(messages)) {
    return '';
  }
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    if (messages[index]?.role === 'assistant') {
      return messages[index]?.content || '';
    }
  }
  return '';
}

function extractCoCreateSuggestions(text) {
  const candidates = [];
  for (const rawLine of String(text || '').split(/\r?\n/)) {
    const trimmedLine = rawLine.trim();
    if (!trimmedLine) {
      continue;
    }
    const match = rawLine.match(/^\s*(?:[-*•]\s+|\d+[.、)]\s+)(.+)$/);
    const rawCandidate = match ? match[1].trim() : trimmedLine;
    const choices = splitChoiceQuestion(rawCandidate);
    if (choices.length) {
      candidates.push(...choices);
    } else if (match) {
      candidates.push(cleanSuggestionText(rawCandidate));
    }
  }
  return uniqueNonEmpty(candidates)
    .filter((item) => item.length >= 4 && item.length <= 120)
    .slice(0, 3);
}

function splitChoiceQuestion(line) {
  const markdownHeadingMatch = line.match(/^\*\*([^*]+?)\*\*[：:]\s*(.*)$/);
  const plainHeadingMatch = markdownHeadingMatch ? null : line.match(/^([^：:]{2,24})[：:]\s*(.*)$/);
  const headingMatch = markdownHeadingMatch || plainHeadingMatch;
  const heading = headingMatch ? cleanSuggestionText(headingMatch[1]) : '';
  const body = headingMatch ? headingMatch[2] : line;
  if (!/(你希望|你想|你打算|还是|或者|或是|如果|若|要是)/.test(body)) {
    return [];
  }
  const candidates = [];
  const conditionalChoice = extractConditionalChoice(body);
  if (conditionalChoice) {
    candidates.push(withChoiceContext(conditionalChoice, heading));
  }
  const promptMatch = body.match(/(?:你希望|你想|你打算)(.+)$/);
  if (promptMatch) {
    candidates.push(...splitChoiceText(promptMatch[1]).map((item) => withChoiceContext(item, heading)));
  }
  return uniqueNonEmpty(candidates)
    .filter((item) => item.length >= 4 && item.length <= 80);
}

function extractConditionalChoice(text) {
  const match = text.match(/(?:如果|若|要是)([^，,？?。；;]{2,40})/);
  return match ? cleanSuggestionText(match[1]) : '';
}

function splitChoiceText(text) {
  return String(text || '')
    .replace(/[？?。；;]+/g, '，')
    .split(/(?:，)?(?:还是|或者|或是)|[、,，]/)
    .map((item) => cleanChoiceText(item))
    .filter(Boolean);
}

function withChoiceContext(choice, heading) {
  if (!choice) {
    return '';
  }
  return heading && Array.from(heading).length <= 10 && Array.from(choice).length <= 6
    ? `${heading}：${choice}`
    : choice;
}

function cleanChoiceText(text) {
  return cleanSuggestionText(text)
    .replace(/^(?:你希望|你想|你打算|希望|想|打算)/, '')
    .trim();
}

function cleanSuggestionText(text) {
  return String(text || '')
    .replace(/\*\*/g, '')
    .replace(/^[：:，,、\s]+/g, '')
    .replace(/[？?。；;：:，,、]+$/g, '')
    .trim();
}

function uniqueNonEmpty(values) {
  const seen = new Set();
  const out = [];
  for (const value of values) {
    const text = cleanSuggestionText(value);
    if (!text || seen.has(text)) {
      continue;
    }
    seen.add(text);
    out.push(text);
  }
  return out;
}

export function applyCoCreateSuggestion(state, suggestion) {
  return {
    ...state,
    input: String(suggestion || ''),
    inputSource: 'suggestion'
  };
}

export function appendCoCreateInput(state, text) {
  return {
    ...state,
    input: text,
    inputSource: 'custom'
  };
}
