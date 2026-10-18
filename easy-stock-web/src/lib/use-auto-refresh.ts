import { useEffect, useRef } from 'react';
import { logRuntimeEvent } from './runtime-log';

type Refresh = (signal: AbortSignal) => unknown | Promise<unknown>;

// Schedule after completion, pause while hidden/offline, and recover on return.
// Callers read existing state only; paid analysis and commands remain explicit.
export function startAutoRefresh(refresh: Refresh, intervalMS: number) {
 let disposed = false;
 let running = false;
 let timer: ReturnType<typeof setTimeout> | undefined;
 let controller: AbortController | undefined;
 let lastStarted = -Infinity;
 const available = () => !document.hidden && navigator.onLine !== false;
 const schedule = () => {
  clearTimeout(timer);
  if (!disposed && available()) timer = setTimeout(() => { void run(); }, intervalMS);
 };
 const run = async () => {
  if (disposed || running || !available()) return;
  clearTimeout(timer);
  running = true; lastStarted = Date.now();
  const current = new AbortController(); controller = current;
  const deadline = setTimeout(() => current.abort(), 45_000);
  try { await refresh(current.signal); }
  catch (error) {
   if (!disposed && !current.signal.aborted) logRuntimeEvent('warn', 'refresh', { event: 'automatic_refresh_failed', error: error instanceof Error ? error.message : String(error) });
  } finally {
   clearTimeout(deadline);
   running = false;
   if (controller === current) controller = undefined;
   schedule();
  }
 };
 const resume = () => {
  if (!available()) { clearTimeout(timer); controller?.abort(); return; }
  if (!running && Date.now() - lastStarted >= 1000) void run();
 };
 document.addEventListener('visibilitychange', resume);
 window.addEventListener('focus', resume);
 window.addEventListener('online', resume);
 window.addEventListener('offline', resume);
 schedule();
 return () => {
  disposed = true; clearTimeout(timer); controller?.abort();
  document.removeEventListener('visibilitychange', resume);
  window.removeEventListener('focus', resume);
  window.removeEventListener('online', resume);
  window.removeEventListener('offline', resume);
 };
}

export function useAutoRefresh(refresh: Refresh, intervalMS: number, enabled = true, resetKey?: unknown) {
 const latest = useRef(refresh); latest.current = refresh;
 useEffect(() => enabled ? startAutoRefresh(signal => latest.current(signal), intervalMS) : undefined, [enabled, intervalMS, resetKey]);
}
