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
 let interrupted = false, recoveryPending = false;
 const available = () => !document.hidden && navigator.onLine !== false;
 const schedule = () => {
  clearTimeout(timer);
  if (!disposed && available()) timer = setTimeout(() => { void run(); }, intervalMS);
 };
 const run = async () => {
  if (disposed || running || !available()) return;
  clearTimeout(timer);
  running = true; recoveryPending = false; lastStarted = Date.now();
  const current = new AbortController(); controller = current;
  const deadline = setTimeout(() => current.abort(), 45_000);
  try { await refresh(current.signal); }
  catch (error) {
   if (!disposed && !current.signal.aborted) logRuntimeEvent('warn', 'refresh', { event: 'automatic_refresh_failed', error: error instanceof Error ? error.message : String(error) });
  } finally {
   clearTimeout(deadline);
   running = false;
   if (controller === current) controller = undefined;
   if (!disposed && recoveryPending && available()) queueRecovery(); else schedule();
  }
 };
 const queueRecovery = () => {
  recoveryPending = false; clearTimeout(timer);
  const delay = Math.max(0, 1000 - (Date.now() - lastStarted));
  if (delay) timer = setTimeout(() => { void run(); }, delay); else void run();
 };
 const resume = () => {
  if (!available()) { interrupted = true; clearTimeout(timer); controller?.abort(); return; }
  const recover = interrupted || Date.now() - lastStarted >= 1000;
  interrupted = false;
  if (running) { if (recover) recoveryPending = true; return; }
  if (recover) queueRecovery();
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
