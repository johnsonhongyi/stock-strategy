import { useCallback, useEffect, useState } from 'react';
import { BackendConfig, KLine, requestJSON } from './backend';
import { useAutoRefresh } from './use-auto-refresh';

// Bounded current-page sample. All persistence is owned by the external Go cache.
export function useThemeMinutes(config: BackendConfig | null, active: boolean, symbols: string[], selected: string, refreshKey: number) {
 const key = [...new Set([selected, ...symbols].filter(Boolean))].slice(0, 16).sort().join(',');
 const [snapshot, setSnapshot] = useState<{ key: string; config: BackendConfig; data: Record<string, KLine[]> }>();
 const load = useCallback(async (signal: AbortSignal) => {
  if (!config || !active || !key) return;
  try {
   const result = await requestJSON<{ data: Record<string, KLine[]> }>(config, `/api/v1/quotes/kline/batch?symbols=${encodeURIComponent(key)}&period=1&limit=240`, { signal });
   if (!signal.aborted) setSnapshot({ key, config, data: result.data });
  } catch {
   if (signal.aborted) return;
   setSnapshot(current => current?.key === key && current.config === config ? { ...current, data: Object.fromEntries(Object.entries(current.data).map(([symbol, lines]) => [symbol, lines.map(line => ({ ...line, meta: { ...line.meta, stale: true, fallback_reason: '分时更新失败，保留上次快照' } }))])) } : undefined);
  }
 }, [config, active, key]);
 useEffect(() => { const controller = new AbortController(); void load(controller.signal); return () => controller.abort(); }, [load, refreshKey]);
 useAutoRefresh(load, 60_000, !!config && active && !!key, load);
 return active && snapshot?.key === key && snapshot.config === config ? snapshot.data : {};
}
