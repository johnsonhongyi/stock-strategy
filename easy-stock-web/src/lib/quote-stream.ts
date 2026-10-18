import type { Quote, StreamMessage } from './backend';
import { logRuntimeEvent } from './runtime-log';

export function connectQuoteStream(url: string, onQuotes: (quotes: Quote[]) => void, onStatus: (status: string) => void) {
 let disposed = false;
 let socket: WebSocket | undefined;
 let retry: ReturnType<typeof setTimeout> | undefined;
 let delay = 1000;
 let lastMessage = Date.now();
 const available = () => !document.hidden && navigator.onLine !== false;
 const reconnect = () => {
  clearTimeout(retry);
  if (disposed || !available()) return;
  retry = setTimeout(connect, delay); delay = Math.min(delay * 2, 30_000);
 };
 const connect = () => {
  if (disposed || !available() || socket && socket.readyState <= WebSocket.OPEN) return;
  let current: WebSocket;
  try { current = new WebSocket(url); }
  catch { onStatus('实时行情连接失败，正在重试'); reconnect(); return; }
  socket = current; lastMessage = Date.now();
  current.onopen = () => { if (!disposed && socket === current) { delay = 1000; lastMessage = Date.now(); onStatus('实时行情已连接'); } };
  current.onmessage = event => {
   if (disposed || socket !== current) return;
   lastMessage = Date.now();
   try {
    const message = JSON.parse(event.data) as StreamMessage;
    if (message.type === 'quotes' && Array.isArray(message.quotes)) { onQuotes(message.quotes); onStatus('实时行情更新中'); }
    if (message.type === 'error') onStatus(message.error || '实时行情暂不可用');
   } catch { onStatus('行情消息格式异常，等待重试'); logRuntimeEvent('warn', 'quotes', { event: 'websocket_invalid_message' }); }
  };
  current.onclose = () => { if (!disposed && socket === current) { socket = undefined; onStatus('实时行情已断开，正在重连'); reconnect(); } };
  current.onerror = () => { if (!disposed && socket === current) { onStatus('实时行情连接异常，正在重试'); current.close(); } };
 };
 const resume = () => {
  if (!available()) { clearTimeout(retry); socket?.close(); return; }
  clearTimeout(retry); connect();
 };
 const watchdog = setInterval(() => {
  if (socket && Date.now() - lastMessage > (socket.readyState === WebSocket.CONNECTING ? 15_000 : 45_000)) socket.close();
 }, 10_000);
 document.addEventListener('visibilitychange', resume);
 window.addEventListener('online', resume); window.addEventListener('offline', resume); window.addEventListener('focus', resume);
 connect();
 return () => {
  disposed = true; clearTimeout(retry); clearInterval(watchdog); socket?.close();
  document.removeEventListener('visibilitychange', resume);
  window.removeEventListener('online', resume); window.removeEventListener('offline', resume); window.removeEventListener('focus', resume);
 };
}
