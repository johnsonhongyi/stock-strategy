import { afterEach, describe, expect, it, vi } from 'vitest';
import { requestJSON } from './backend';
vi.mock('./runtime-log', () => ({ logRuntimeEvent: vi.fn(), runtimeErrorDetails: vi.fn(), runtimeFeatureForPath: vi.fn() }));
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
const config = { backendUrl: 'http://fixture', token: '' };
describe('bounded data reads', () => {
 it('ends a stalled read so automatic refresh can retry', async () => {
  vi.useFakeTimers();
  vi.stubGlobal('fetch', vi.fn((_url, init) => new Promise((_resolve, reject) => init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))))));
  const result = expect(requestJSON(config, '/api/v1/market/indexes')).rejects.toMatchObject({ code: 'read_timeout' });
  await vi.advanceTimersByTimeAsync(30_000); await result;
 });
 it('preserves explicit page cancellation without relabeling it a timeout', async () => {
  vi.stubGlobal('fetch', vi.fn((_url, init) => new Promise((_resolve, reject) => init.signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))))));
  const controller = new AbortController(); const result = expect(requestJSON(config, '/api/v1/market/indexes', { signal: controller.signal })).rejects.toMatchObject({ name: 'AbortError' });
  controller.abort(); await result;
 });
});
