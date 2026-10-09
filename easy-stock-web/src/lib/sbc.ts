import { useEffect, useMemo, useState } from 'react';
import { requestJSON, type BackendConfig, type KLine } from './backend';

export type SBCPoint = { time: string } & Record<string, number | string | boolean | null>;
export type SBCAnalysis = {
  implementation?: 'go-native';
  schema_version: 1; source: string; symbol: string; period: string; as_of: string;
  trade_date: string; market_meta?: KLine['meta']; points: SBCPoint[];
  levels: {ratio: number; price: number}[];
  activity: null | { avg_amp: number; max_amp: number; rating: string; days: [string, number][] };
  strategy: { is_matched: boolean; score?: number; reason?: string; entry_price?: number; stop_loss?: number; target_price_1?: number; target_price_2?: number; breakout_bar_idx?: number; pioneer_sig_indices?: number[] };
};
export type SBCProvider = (config: BackendConfig, input: {symbol: string; period: string; bars: KLine[]}, signal: AbortSignal) => Promise<SBCAnalysis>;
export const httpSBCProvider: SBCProvider = async (config, input, signal) => {
  const result = await requestJSON<{data: SBCAnalysis}>(config, '/api/v1/quotes/sbc/v1/analyze', {
    method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(input), signal,
  });
  if (result.data.schema_version !== 1 || result.data.symbol !== input.symbol || result.data.period !== input.period ||
      result.data.points.length !== input.bars.length || result.data.points.some((point, i) => Date.parse(point.time) !== Date.parse(input.bars[i].time))) {
    throw new Error('SBC 结果与当前行情不一致');
  }
  return result.data;
};

export function useSBC(config: BackendConfig | null | undefined, bars: KLine[], symbol: string, period: string, enabled: boolean, provider = httpSBCProvider) {
  const key = useMemo(() => JSON.stringify({symbol, period, bars}), [symbol, period, bars]);
  const [snapshot, setSnapshot] = useState<{key: string; data?: SBCAnalysis; error?: string} | null>(null);
  useEffect(() => {
    if (!enabled || !config || bars.length < 10) return;
    const controller = new AbortController();
    provider(config, {symbol, period, bars}, controller.signal).then(
      data => { if (!controller.signal.aborted) setSnapshot({key, data}); },
      error => { if (!controller.signal.aborted) setSnapshot({key, error: error instanceof Error ? error.message : 'SBC 测算失败'}); },
    );
    return () => controller.abort();
  }, [key, config?.backendUrl, config?.token, enabled, provider]);
  if (!enabled) return {data: undefined, status: ''};
  if (bars.length < 10) return {data: undefined, status: 'SBC：至少需要 10 根 K 线'};
  if (!config) return {data: undefined, status: 'SBC：后端未连接'};
  const current = snapshot?.key === key ? snapshot : null;
  return {data: current?.data, status: current?.error ? 'SBC：' + current.error : current?.data ? '' : 'SBC 测算中…'};
}
