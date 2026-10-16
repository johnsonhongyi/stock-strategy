import type { ResearchJob } from './stock-research';

const ms = (value: unknown): number | null => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null;
const remainder = (total: number | null, ...parts: Array<number | null>) => total === null || parts.some(value => value === null) ? null : Math.max(0, total - parts.reduce<number>((sum, value) => sum + (value as number), 0));
export function researchTiming(job: ResearchJob) {
	const started = Date.parse(job.started_at);
	const ended = Date.parse(job.completed_at || job.updated_at);
	const total = Number.isFinite(started) && Number.isFinite(ended) ? ms(ended - started) : null;
	const attempts = (job.analysis?.research_report?.attempts || []).map(attempt => {
		const timings = attempt.bridge_timings || {};
		const call = ms(attempt.duration_ms), queue = ms(timings.queue_wait_ms), setup = ms(timings.turn_started_ms);
		const completed = ms(timings.bridge_to_completed_ms), first = ms(timings.first_token_ms);
		return { stage: attempt.stage, call, queue, setup, waiting: remainder(first, setup), output: remainder(completed, first), other: remainder(call, queue, completed), tokens: attempt.token_usage };
	});
	const calls = attempts.some(attempt => attempt.call === null) ? null : attempts.reduce((sum, attempt) => sum + (attempt.call as number), 0);
	return { total, calls, preparation: remainder(total, calls), attempts };
}
