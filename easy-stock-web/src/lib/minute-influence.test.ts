import { describe, expect, it } from 'vitest';
import { KLine } from './backend';
import { minuteInfluence } from './minute-influence';
const meta = {source: 'tdx', fetched_at: '', latency_ms: 0, stale: false};
function rows(symbol: string, lag = 0): KLine[] {
 let close = 10;
 return Array.from({length: 120}, (_, i) => {
  const j = i - lag;close *= 1 + (j >= 0 ? (Math.sin(j * 1.93) + Math.cos(j * 0.37)) * 0.002 : 0);
  return {symbol, time: new Date(Date.UTC(2026, 9, 9, 1, 31 + i)).toISOString(), open: close, close, high: close, low: close, volume: 100, amount: close * 100, meta};
 });
}
describe('minute peer evidence', () => {
 it('detects timestamp aligned delayed association', () => {
  const result = minuteInfluence('a', {a: rows('a'), b: rows('b', 2), c: rows('c', 2), d: rows('d', 2)}, ['a','b','c','d']);
  expect(result?.peers).toBe(3);expect(result!.samples).toBeGreaterThan(60);expect(result!.score).toBeGreaterThan(30);
 });
 it('rejects stale, missing, cross-day and insufficient samples', () => {
  const histories = {a: rows('a'), b: rows('b'), c: rows('c'), d: rows('d').map(b => ({...b, meta: {...meta, stale: true}}))};
  expect(minuteInfluence('a', histories, ['b','c','d'])).toBeUndefined();
  histories.d = rows('d').map(b => ({...b, time: b.time.replace('2026-10-09', '2026-10-08')}));
  expect(minuteInfluence('a', histories, ['b','c','d'])).toBeUndefined();
  expect(minuteInfluence('a', {a: rows('a').slice(0,20)}, ['b','c','d'])).toBeUndefined();
 });
 it('does not invent influence for flat prices', () => {
  const flat = (s: string) => rows(s).map(b => ({...b, open: 10, close: 10, high: 10, low: 10}));
  expect(minuteInfluence('a', {a: flat('a'), b: flat('b'), c: flat('c'), d: flat('d')}, ['b','c','d'])?.score).toBe(0);
 });
});
