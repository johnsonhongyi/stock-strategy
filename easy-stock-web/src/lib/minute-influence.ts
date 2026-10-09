import { KLine } from './backend';
export type MinuteInfluence = { score: number; peers: number; samples: number; coverage: number; tradeDate: string; lag: number };
type History = Record<string, KLine[]>;
function correlation(pairs: [number, number][]): number {
 const n = pairs.length;
 const sx = pairs.reduce((s, p) => s + p[0], 0), sy = pairs.reduce((s, p) => s + p[1], 0);
 const xy = pairs.reduce((s, p) => s + p[0] * p[1], 0) - sx * sy / n;
 const xx = pairs.reduce((s, p) => s + p[0] ** 2, 0) - sx ** 2 / n;
 const yy = pairs.reduce((s, p) => s + p[1] ** 2, 0) - sy ** 2 / n;
 return xx > 1e-16 && yy > 1e-16 ? xy / Math.sqrt(xx * yy) : 0;
}
// Observational lead/lag association, not causal attribution. Never mix sessions,
// stale providers, lunch breaks or absent peer minutes into zero-filled returns.
export function minuteInfluence(symbol: string, histories: History, eligiblePeers: string[], expectedDate?: string): MinuteInfluence | undefined {
 const target = histories[symbol];
 if (!target?.length || target.at(-1)?.meta.stale) return;
 const date = target.at(-1)!.time.slice(0, 10);
 if (expectedDate && date !== expectedDate) return;
 const series = (rows: KLine[]) => new Map(rows.filter(b => b.time.slice(0, 10) === date && !b.meta.stale && Number.isFinite(b.close) && b.close > 0).map(b => [Date.parse(b.time), b.close]));
 const own = series(target);const peers = [...new Set(eligiblePeers)].filter(s => s !== symbol);
 if (own.size < 90 || peers.length < 3) return;
 const observed: { value: number; lag: number; samples: number }[] = [];
 for (const peer of peers) {
  const rows = histories[peer];if (!rows?.length || rows.at(-1)?.meta.stale || rows.at(-1)!.time.slice(0, 10) !== date) continue;
  const other = series(rows);let best: typeof observed[number] | undefined;
  for (let lag = 1; lag <= 5; lag++) {
   const pairs: [number, number][] = [];
   for (const [time, close] of own) {
    const previous = own.get(time - 60000), start = other.get(time), end = other.get(time + lag * 60000);
    // Every minute must exist: do not bridge lunch or sparse/truncated pages.
    if (!previous || !start || !end || !Array.from({length: lag}, (_, i) => other.has(time + (i + 1) * 60000)).every(Boolean)) continue;
    pairs.push([close / previous - 1, end / start - 1]);
   }
   if (pairs.length < 60) continue;
   const value = correlation(pairs);if (!best || value > best.value) best = { value, lag, samples: pairs.length };
  }
  if (best) observed.push(best);
 }
 const coverage = observed.length / peers.length;
 if (observed.length < 3 || coverage < 0.6) return;
 return { score: Math.round(observed.reduce((s, p) => s + Math.max(0, p.value), 0) / observed.length * 100), peers: observed.length, samples: Math.min(...observed.map(p => p.samples)), coverage, tradeDate: date, lag: Math.round(observed.reduce((s, p) => s + p.lag, 0) / observed.length) };
}
