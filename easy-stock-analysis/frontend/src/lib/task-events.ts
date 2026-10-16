import { BackendRequestError, type BackendConfig } from './backend';

/** Authenticated SSE snapshots; false means reconnect/read through the existing GET API. */
export async function streamTaskSnapshots<T>(config: BackendConfig, path: string, receive: (value: T) => boolean, signal?: AbortSignal): Promise<boolean> {
 const headers = new Headers({ Accept: 'text/event-stream' });
 if (config.token) headers.set('Authorization', `Bearer ${config.token}`);
 const controller = new AbortController();
 const stop = () => controller.abort();
 if (signal?.aborted) controller.abort(); else signal?.addEventListener('abort', stop, { once: true });
 let idle: ReturnType<typeof setTimeout>;
 const touch = () => { clearTimeout(idle); idle = setTimeout(() => controller.abort(), 45000); };
 touch();
 let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
 try {
  const response = await fetch(new URL(path, config.backendUrl), { headers, signal: controller.signal });
  if (response.status === 404 || response.status === 405) return false;
  if (!response.ok) {
   if (response.status === 401) window.dispatchEvent(new Event('stock-auth-required'));
   throw new BackendRequestError('任务事件连接失败', response.status);
  }
  if (!response.body || !response.headers?.get('content-type')?.includes('text/event-stream')) return false;
  reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '', lines: string[] = [], frameSize = 0;
  while (true) {
   const chunk = await reader.read();
   if (chunk.done) return false;
   touch(); buffer += decoder.decode(chunk.value, { stream: true });
   if (buffer.length > 2 * 1024 * 1024) throw new Error('任务事件超过大小限制');
   let newline: number;
   while ((newline = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, newline).replace(/\r$/, ''); buffer = buffer.slice(newline + 1);
    if (line.startsWith('data:')) { const value = line.slice(5).trimStart(); lines.push(value); frameSize += value.length; }
    if (frameSize > 2 * 1024 * 1024) throw new Error('任务事件超过大小限制');
    if (!line && lines.length) {
     const payload = JSON.parse(lines.join('\n')) as { data: T }; lines = []; frameSize = 0;
     if (receive(payload.data)) return true;
    }
   }
  }
 } finally {
  clearTimeout(idle!); signal?.removeEventListener('abort', stop);
  await reader?.cancel().catch(() => undefined);
  controller.abort();
 }
}
