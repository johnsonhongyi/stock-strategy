import { useEffect, useState } from 'react';
import { BackendConfig, KLine, requestJSON } from './backend';

// Bounded current-page sample. All persistence is owned by the external Go cache.
export function useThemeMinutes(config: BackendConfig | null, active: boolean, symbols: string[], selected: string, refreshKey: number) {
 const key = [...new Set([selected, ...symbols].filter(Boolean))].slice(0, 16).sort().join(',');
 const [snapshot, setSnapshot] = useState<{ key: string; config: BackendConfig; data: Record<string, KLine[]> }>();
 useEffect(() => {
  if (!config || !active || !key) return;
  let controller = new AbortController();let stopped = false;let timer: ReturnType<typeof setTimeout>;
  async function load() {
   if(stopped)return;
   controller=new AbortController();
   const timeout=setTimeout(()=>controller.abort(),40_000);
   try {
    const result = await requestJSON<{data: Record<string, KLine[]>}>(config!, `/api/v1/quotes/kline/batch?symbols=${encodeURIComponent(key)}&period=1&limit=240`, {signal: controller.signal});
    if (!stopped) setSnapshot({key, config: config!, data: result.data});
   } catch { if (!stopped) setSnapshot(undefined); }
   clearTimeout(timeout);
   if (!stopped) timer = setTimeout(load, 60_000);
  }
  void load();return () => { stopped = true;controller.abort();clearTimeout(timer); };
 }, [config, active, key, refreshKey]);
 return active && snapshot?.key === key && snapshot.config === config ? snapshot.data : {};
}
