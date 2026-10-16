import { afterEach, describe, expect, it, vi } from 'vitest';
import { streamTaskSnapshots } from './task-events';
import { streamDesktopChat } from './desktop-chat';

const config = { backendUrl: 'http://127.0.0.1:20084', token: 'isolated-token' };
afterEach(() => vi.unstubAllGlobals());
function events(chunks: string[]) {
 const encoder = new TextEncoder();
 return new Response(new ReadableStream({ start(controller) { for (const chunk of chunks) controller.enqueue(encoder.encode(chunk)); controller.close(); } }), { headers: { 'Content-Type': 'text/event-stream' } });
}
describe('authenticated task event stream', () => {
 it('parses split CRLF frames and forwards authentication', async () => {
  const fetch = vi.fn().mockResolvedValue(events([': keepalive\r\n\r\nevent: snapshot\r\nda', 'ta: {"data":{"status":"running"}}\r\n\r', '\nevent: snapshot\ndata: {"data":{"status":"completed"}}\n\n']));
  vi.stubGlobal('fetch', fetch); const seen: string[] = [];
  expect(await streamTaskSnapshots<{status: string}>(config, '/events', value => { seen.push(value.status); return value.status === 'completed'; })).toBe(true);
  expect(seen).toEqual(['running', 'completed']);
  expect(fetch.mock.calls[0][1].headers.get('Authorization')).toBe('Bearer isolated-token');
 });
 it('falls back for older servers and releases the response reader', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 404 })));
  expect(await streamTaskSnapshots(config, '/events', () => false)).toBe(false);
 });
 it('does not fall back through an authorization failure', async () => {
  vi.stubGlobal('window', new EventTarget());
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })));
  await expect(streamTaskSnapshots(config, '/events', () => false)).rejects.toMatchObject({ status: 401 });
 });
 it('delivers conversation increments without polling the job again', async () => {
  const fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ data: { id: 'job', session_id: 'logical', status: 'queued', stream_events: true } }), { headers: { 'Content-Type': 'application/json' } }))
   .mockResolvedValueOnce(events(['data: {"data":{"id":"job","session_id":"logical","status":"running","result":{"content":"首段"}}}\n\n', 'data: {"data":{"id":"job","session_id":"logical","status":"completed","result":{"content":"首段完成"}}}\n\n']));
  vi.stubGlobal('fetch', fetch); const onDelta = vi.fn();
  expect(await streamDesktopChat({ config, prompt: 'isolated', onDelta })).toEqual({ content: '首段完成', hermesSessionID: 'logical' });
  expect(onDelta.mock.calls).toEqual([['首段'], ['首段完成']]);
  expect(fetch).toHaveBeenCalledTimes(2);
 });
 it('cancels immediately when abort arrives in the session callback', async () => {
  const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({data:{id:'job',session_id:'logical',status:'queued',stream_events:true}}),{headers:{'Content-Type':'application/json'}}));
  vi.stubGlobal('fetch',fetch);const controller=new AbortController();
  await expect(streamDesktopChat({config,prompt:'isolated',signal:controller.signal,onSession:()=>controller.abort()})).rejects.toMatchObject({name:'AbortError'});
  expect(fetch.mock.calls.some(call=>String(call[0]).endsWith('/events'))).toBe(false);
  expect(fetch.mock.calls.some(call=>call[1]?.method==='DELETE')).toBe(true);
 });
});
