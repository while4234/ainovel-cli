import { describe, expect, it, vi } from 'vitest';
import { createSerializedAutosave } from './autosave-controller.js';

const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

describe('serialized artwork autosave', () => {
  it('waits 700ms, serializes saves, and preserves an in-flight newer edit', async () => {
    vi.useFakeTimers();
    const first = deferred();
    const second = deferred();
    const save = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const saved = [];
    const controller = createSerializedAutosave({ save, onSaved: (_result, meta) => saved.push(meta), scheduler: globalThis });
    controller.edit({ prompt: 'first' });
    await vi.advanceTimersByTimeAsync(699);
    expect(save).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(save).toHaveBeenCalledTimes(1);
    controller.edit({ prompt: 'newer' });
    await vi.advanceTimersByTimeAsync(700);
    expect(save).toHaveBeenCalledTimes(1);
    first.resolve({ version: 2 });
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(save).toHaveBeenCalledTimes(2);
    expect(controller.getState().dirty).toBe(true);
    second.resolve({ version: 3 });
    await controller.flush();
    expect(save.mock.calls.map(([value]) => value.prompt)).toEqual(['first', 'newer']);
    expect(saved).toEqual([{ revision: 1, current: false }, { revision: 2, current: true }]);
    expect(controller.getState().dirty).toBe(false);
    controller.destroy();
    vi.useRealTimers();
  });

  it('stops after failure and retries only through the manual retry command', async () => {
    vi.useFakeTimers();
    const failure = Object.assign(new Error('save failed'), { code: 'draft_version_conflict' });
    const save = vi.fn().mockRejectedValueOnce(failure).mockResolvedValueOnce({ version: 2 });
    const controller = createSerializedAutosave({ save, scheduler: globalThis });
    controller.edit({ prompt: 'first' });
    await vi.advanceTimersByTimeAsync(700);
    await Promise.resolve();
    expect(controller.getState().failed).toBe(true);
    controller.edit({ prompt: 'second' });
    await vi.advanceTimersByTimeAsync(5000);
    expect(save).toHaveBeenCalledTimes(1);
    await controller.retry();
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0]).toEqual({ prompt: 'second' });
    expect(controller.getState().failed).toBe(false);
    controller.destroy();
    vi.useRealTimers();
  });
});
