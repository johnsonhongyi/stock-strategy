import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { startAutoRefresh } from './use-auto-refresh';
vi.mock('./runtime-log', () => ({ logRuntimeEvent: vi.fn() }));
let documentState: EventTarget & { hidden: boolean };
let network: { onLine: boolean };
let browser: EventTarget;
const stops: Array<() => void> = [];
beforeEach(() => {
 vi.useFakeTimers();
 documentState = Object.assign(new EventTarget(), { hidden: false });
 network = { onLine: true }; browser = new EventTarget();
 vi.stubGlobal('document', documentState); vi.stubGlobal('navigator', network); vi.stubGlobal('window', browser);
});
afterEach(() => { stops.splice(0).forEach(stop => stop()); vi.useRealTimers(); vi.unstubAllGlobals(); });
describe('automatic data refresh lifecycle', () => {
 it('waits for a slow read to settle before scheduling another read', async () => {
  let finish: () => void = () => {};
  const read = vi.fn(() => new Promise<void>(resolve => { finish = resolve; }));
  stops.push(startAutoRefresh(read, 1000));
  await vi.advanceTimersByTimeAsync(6000); expect(read).toHaveBeenCalledTimes(1);
  finish(); await Promise.resolve(); await vi.advanceTimersByTimeAsync(999); expect(read).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1); expect(read).toHaveBeenCalledTimes(2);
 });
 it('pauses in a hidden tab and reloads once when visibility and focus return together', async () => {
  const read = vi.fn(async () => {}); stops.push(startAutoRefresh(read, 1000));
  documentState.hidden = true; documentState.dispatchEvent(new Event('visibilitychange'));
  await vi.advanceTimersByTimeAsync(60_000); expect(read).not.toHaveBeenCalled();
  documentState.hidden = false; documentState.dispatchEvent(new Event('visibilitychange')); browser.dispatchEvent(new Event('focus'));
  await Promise.resolve(); expect(read).toHaveBeenCalledTimes(1);
 });
 it('recovers from a failed read and an offline interval without user refresh', async () => {
  const read = vi.fn().mockRejectedValueOnce(new Error('temporary')).mockResolvedValue(undefined);
  stops.push(startAutoRefresh(read, 1000)); await vi.advanceTimersByTimeAsync(1000); expect(read).toHaveBeenCalledTimes(1);
  network.onLine = false; browser.dispatchEvent(new Event('offline')); await vi.advanceTimersByTimeAsync(5000); expect(read).toHaveBeenCalledTimes(1);
  network.onLine = true; browser.dispatchEvent(new Event('online')); await Promise.resolve(); expect(read).toHaveBeenCalledTimes(2);
 });
 it('aborts the active read and removes all future work on cleanup', async () => {
  let signal: AbortSignal | undefined;
  const read = vi.fn((value: AbortSignal) => { signal = value; return new Promise<void>(resolve => value.addEventListener('abort', () => resolve())); });
  const stop = startAutoRefresh(read, 1000); stops.push(stop); await vi.advanceTimersByTimeAsync(1000);
  stop(); expect(signal?.aborted).toBe(true); browser.dispatchEvent(new Event('focus')); await vi.advanceTimersByTimeAsync(60_000); expect(read).toHaveBeenCalledTimes(1);
 });
});
