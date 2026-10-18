import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { connectQuoteStream } from './quote-stream';
vi.mock('./runtime-log', () => ({ logRuntimeEvent: vi.fn() }));
class FakeSocket {
 static CONNECTING = 0; static OPEN = 1; static CLOSED = 3; static instances: FakeSocket[] = [];
 readyState = 0; onopen?: () => void; onclose?: () => void; onerror?: () => void; onmessage?: (event: { data: string }) => void;
 constructor(public url: string) { FakeSocket.instances.push(this); }
 close() { this.readyState = 3; this.onclose?.(); }
 open() { this.readyState = 1; this.onopen?.(); }
}
let documentState: EventTarget & { hidden: boolean }; let stop: () => void;
beforeEach(() => { vi.useFakeTimers(); FakeSocket.instances = []; documentState = Object.assign(new EventTarget(), { hidden: false }); vi.stubGlobal('document', documentState); vi.stubGlobal('navigator', { onLine: true }); vi.stubGlobal('window', new EventTarget()); vi.stubGlobal('WebSocket', FakeSocket); });
afterEach(() => { stop?.(); vi.useRealTimers(); vi.unstubAllGlobals(); });
describe('quote stream recovery', () => {
 it('reconnects after connection loss and accepts fresh quotes on the replacement stream', async () => {
  const quotes = vi.fn(); stop = connectQuoteStream('ws://fixture/stream', quotes, vi.fn());
  FakeSocket.instances[0].open(); FakeSocket.instances[0].close(); await vi.advanceTimersByTimeAsync(1000);
  expect(FakeSocket.instances).toHaveLength(2); FakeSocket.instances[1].open();
  FakeSocket.instances[1].onmessage?.({ data: JSON.stringify({ type: 'quotes', quotes: [{ symbol: '600519.SH', price: 10 }] }) }); expect(quotes).toHaveBeenCalledTimes(1);
 });
 it('handles malformed messages and replaces a connection that never opens', async () => {
  const quotes = vi.fn(); stop = connectQuoteStream('ws://fixture/stream', quotes, vi.fn());
  expect(() => FakeSocket.instances[0].onmessage?.({ data: '<html>gateway error</html>' })).not.toThrow(); expect(quotes).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(21_000); expect(FakeSocket.instances.length).toBeGreaterThan(1);
 });
 it('does not reconnect while hidden and cleans up pending retries', async () => {
  stop = connectQuoteStream('ws://fixture/stream', vi.fn(), vi.fn()); documentState.hidden = true; documentState.dispatchEvent(new Event('visibilitychange'));
  await vi.advanceTimersByTimeAsync(60_000); expect(FakeSocket.instances).toHaveLength(1);
  documentState.hidden = false; documentState.dispatchEvent(new Event('visibilitychange')); expect(FakeSocket.instances).toHaveLength(2);
  stop(); await vi.advanceTimersByTimeAsync(60_000); expect(FakeSocket.instances).toHaveLength(2);
 });
});
