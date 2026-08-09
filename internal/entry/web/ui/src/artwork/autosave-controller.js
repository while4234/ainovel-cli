export function createSerializedAutosave({
  delay = 700,
  save,
  onChange = () => {},
  onSaved = () => {},
  onError = () => {},
  scheduler = globalThis
}) {
  let timer = null;
  let disposed = false;
  let revision = 0;
  let savedRevision = 0;
  let value;
  let inFlight = null;
  let timerMaturedWhileSaving = false;
  let failed = false;
  let lastError = null;

  function snapshot() {
    return {
      revision,
      savedRevision,
      dirty: revision > savedRevision,
      saving: Boolean(inFlight),
      failed,
      error: lastError
    };
  }

  function publish() {
    if (!disposed) onChange(snapshot());
  }

  function clearTimer() {
    if (timer !== null) scheduler.clearTimeout(timer);
    timer = null;
  }

  function schedule() {
    clearTimer();
    if (disposed || failed || revision <= savedRevision) return;
    timer = scheduler.setTimeout(() => {
      timer = null;
      if (inFlight) {
        timerMaturedWhileSaving = true;
        return;
      }
      void persistCurrent().catch(() => {});
    }, delay);
  }

  async function persistCurrent() {
    if (disposed || failed || revision <= savedRevision) return null;
    if (inFlight) return inFlight;
    clearTimer();
    const targetRevision = revision;
    const targetValue = value;
    timerMaturedWhileSaving = false;
    const operation = Promise.resolve().then(() => save(targetValue, targetRevision));
    inFlight = operation;
    publish();
    try {
      const result = await operation;
      if (disposed) return result;
      savedRevision = Math.max(savedRevision, targetRevision);
      lastError = null;
      failed = false;
      const current = revision === targetRevision;
      onSaved(result, { revision: targetRevision, current });
      return result;
    } catch (error) {
      if (!disposed) {
        failed = true;
        lastError = error;
        timerMaturedWhileSaving = false;
        clearTimer();
        onError(error, { revision: targetRevision });
      }
      throw error;
    } finally {
      if (inFlight === operation) inFlight = null;
      if (!disposed) {
        publish();
        if (!failed && revision > savedRevision) {
          if (timerMaturedWhileSaving) void persistCurrent().catch(() => {});
          else if (timer === null) schedule();
        }
      }
    }
  }

  function edit(nextValue) {
    if (disposed) return snapshot();
    value = nextValue;
    revision += 1;
    if (!failed) schedule();
    publish();
    return snapshot();
  }

  async function flush() {
    clearTimer();
    if (failed) throw lastError || new Error('autosave retry required');
    while (!disposed && revision > savedRevision) {
      if (inFlight) await inFlight;
      else await persistCurrent();
      if (failed) throw lastError;
    }
    return snapshot();
  }

  async function retry() {
    if (disposed) return snapshot();
    failed = false;
    lastError = null;
    publish();
    await flush();
    return snapshot();
  }

  function destroy() {
    disposed = true;
    clearTimer();
  }

  publish();
  return { edit, flush, retry, destroy, getState: snapshot };
}
