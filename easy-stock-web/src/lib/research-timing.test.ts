import { describe, expect, it } from 'vitest';
import { researchTiming } from './research-timing';
import type { ResearchJob } from './stock-research';
const job = (attempts: unknown[], started = '2026-10-16T19:08:18.762Z', completed = '2026-10-16T19:09:02.014Z') => ({ started_at: started, completed_at: completed, analysis: { research_report: { attempts } } }) as ResearchJob;
describe('researchTiming', () => {
 it('splits the measured 43-second research without counting bridge time twice', () => {
  const result = researchTiming(job([{ stage: 'quick', duration_ms: 38994, bridge_timings: { queue_wait_ms: 3, turn_started_ms: 2110, first_token_ms: 29985, bridge_to_completed_ms: 34547 } }]));
  expect(result.total).toBe(43252); expect(result.preparation).toBe(4258);
  expect(result.attempts[0]).toMatchObject({ setup: 2110, waiting: 27875, output: 4562, other: 4444 });
 });
 it('keeps missing historical bridge measurements unknown', () => {
  const result = researchTiming(job([{ stage: 'quick', duration_ms: 38994 }]));
  expect(result.attempts[0]).toMatchObject({ setup: null, queue: null, waiting: null, output: null, other: null });
 });
 it('shows measured failed calls without requiring an accepted AI report', () => {
  const result = researchTiming({ started_at: '2026-10-16T19:00:00Z', completed_at: '2026-10-16T19:00:20Z', analysis: { research_attempts: [{ stage: 'quick', duration_ms: 15000 }] } } as ResearchJob);
  expect(result.calls).toBe(15000); expect(result.preparation).toBe(5000);
 });
 it('sums multiple calls and rejects invalid dates or durations', () => {
  expect(researchTiming(job([{ duration_ms: 1000 }, { duration_ms: 2000 }])).calls).toBe(3000);
  expect(researchTiming(job([{ duration_ms: -1 }], 'invalid')).preparation).toBeNull();
  expect(researchTiming(job([{ duration_ms: 90000 }])).preparation).toBe(0);
 });
});
