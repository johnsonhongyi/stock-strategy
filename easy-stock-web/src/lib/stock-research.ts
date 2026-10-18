import type { StockAIAnalysis } from './backend';

export type ResearchAnalysisLevel = 'quantitative' | 'quick' | 'standard' | 'deep';
export type ResearchRequest = { symbol: string; purpose: 'observe' | 'new_position' | 'holding'; horizon: 'short' | 'swing' | 'medium'; cost_price?: number; analysis_level?: ResearchAnalysisLevel; ai_provider?: 'hermes' | 'codex_desktop' | 'antigravity_desktop'; model?: string; reasoning_effort?: string; desktop_node_id?: string; desktop_workspace_id?: string };
export type ResearchSource = { id: string; kind: string; title: string; content: string; provider: string; url?: string; published_at?: string; captured_at: string; report_date?: string; time_status: string };
export type ResearchClaim = { text: string; kind: string; source_ids: string[]; quote?: string };
export type ResearchCondition = { id: string; text: string; metric: string; operator: string; anchor_id?: string; threshold?: number; window: string; source_ids: string[]; status: string };
export type ResearchScenario = { key: string; name: string; description: string; condition_ids: string[]; response: string };
export type ResearchReport = {
	headline: string; thesis: ResearchClaim; support: ResearchClaim[]; counter: ResearchClaim[]; alternatives: ResearchClaim[];
	main_conflict: string; evidence_level: string; limitations: string[]; conditions: ResearchCondition[]; invalidation_ids: string[];
	scenarios: ResearchScenario[];
	decision: { status: string; mode: string; horizon: string; new_position: string; existing_position: string; reason: string; price_plan?: { entry_anchor: string; stop_anchor: string; target_anchor?: string; reason: string; source_ids: string[] } | null };
	baseline_relation: string; baseline_reason: string; snapshot_id: string; snapshot_version: number; prompt_version: string;
	request: ResearchRequest; model: string; generated_at: string; cutoff_at: string; sources: ResearchSource[];
	analysis_level?: ResearchAnalysisLevel;
	anchors: Array<{ id: string; label: string; price: number; source_id: string; as_of: string }>;
	questions: Array<{ question: string; why: string; tool: string; query: string; source_id?: string; status: string; outcome?: string }>;
	attempts: Array<{ stage: string; duration_ms: number; prompt_bytes: number; response_bytes: number; error?: string; bridge_timings?: Record<string, number>; token_usage?: Record<string, number> }>;
	compression?: { version: string; original_source_count: number; selected_source_count: number; original_content_bytes: number; selected_content_bytes: number };
	validation: string; validation_notes: string[];
};
export type ResearchVerification = { checked_at: string; baseline_at: string; source: string; summary: string; checks: Array<{ condition_id: string; status: string; observed?: number; as_of?: string; detail: string }> };
export type ResearchJob = { id: string; request: ResearchRequest; status: string; stage: string; message: string; error?: string; started_at: string; updated_at: string; completed_at?: string; analysis?: StockAIAnalysis; verification?: ResearchVerification };
export type ResearchJobSummary = Pick<ResearchJob, 'id' | 'request' | 'status' | 'stage' | 'message' | 'started_at' | 'updated_at'> & { name: string; headline: string; score: number; completed_at?: string; duration_ms?: number; analysis_level?: ResearchAnalysisLevel; model?: string; reasoning_effort?: string };

export const isResearchRunning = (job?: Pick<ResearchJob, 'status'> | null) => !!job && (job.status === 'queued' || job.status === 'running');
export const evidenceLevelLabel = (level: string) => ({ sufficient: '较充分', limited: '有限', insufficient: '不足' }[level] || '未评估');
export const researchLevelLabel = (level?: ResearchAnalysisLevel) => ({ quantitative: '量化速览', quick: 'AI快速研判', standard: 'AI标准研判', deep: 'AI深度研究' }[level || 'deep'] || 'AI深度研究');
export type ResearchLevelFilter = 'all' | ResearchAnalysisLevel;
export function researchSummaryLevel(item: Pick<ResearchJobSummary, 'analysis_level' | 'request'>): ResearchAnalysisLevel {
	return item.analysis_level || item.request.analysis_level || 'deep';
}
export function filterResearchHistory(items: ResearchJobSummary[], level: ResearchLevelFilter = 'all') {
	return level === 'all' ? items : items.filter((item) => researchSummaryLevel(item) === level);
}
export function researchLevelShortLabel(level?: ResearchAnalysisLevel) {
	return ({ quantitative: '量', quick: '快', standard: '标', deep: '深' } as const)[level || 'deep'] || '深';
}
export function formatCompactResearchDuration(durationMS?: number) {
	if (durationMS == null || !Number.isFinite(durationMS) || durationMS < 0) return '';
	const seconds = Math.round(durationMS / 1000);
	return seconds < 60 ? seconds + '秒' : Math.round(seconds / 60) + '分';
}
export function researchModelEffortLabel(model?: string, effort?: string) {
	const id = (model || '').trim().toLowerCase();
	if (!id) return '';
	const tokens = id.split(/[^a-z0-9]+/).filter(Boolean);
	const family = ['luna', 'sol', 'astra', 'terra'].find((value) => tokens.includes(value)) || tokens[tokens.length - 1] || id;
	const normalizedEffort = (effort || '').trim().toLowerCase();
	const effortCode: Record<string, string> = { none: '', minimal: 'minimal', low: 'low', medium: 'medium', high: 'high', xhigh: 'xhigh', max: 'max' };
	const suffix = effortCode[normalizedEffort] ?? normalizedEffort;
	return suffix ? family + '-' + suffix : family;
}
export const conditionStatusLabel = (status: string) => ({ pending: '待观察', met: '已满足', not_met: '窗口结束未满足', not_yet: '尚未满足', unavailable: '数据不足', manual_review: '需人工核实' }[status] || '待观察');
export function safeResearchURL(value?: string) {
	try { const url = new URL(value || ''); return ['https:', 'http:'].includes(url.protocol) ? url.href : undefined; } catch { return undefined; }
}
export function researchConditionValue(condition: ResearchCondition) {
	if (condition.threshold == null) return condition.text;
	return `${condition.metric === 'close' ? '收盘价' : '5/20日量比'} ${condition.operator === 'gte' ? '≥' : '≤'} ${condition.threshold.toFixed(2)}${condition.metric === 'close' ? ' 元' : ''}`;
}
export function researchPlanText(analysis: StockAIAnalysis) {
	const report = analysis.research_report;
	if (!report) return '';
	return [
		`${analysis.name}（${analysis.symbol}）研究报告`, `报告编号：${analysis.analysis_id || '--'}；快照：${report.snapshot_id} v${report.snapshot_version}`,
		`分析时点：${report.cutoff_at}；研究周期：${report.request.horizon}`,
		`判断：${report.thesis.text}`, `证据充分度：${evidenceLevelLabel(report.evidence_level)}`, `核心分歧：${report.main_conflict}`,
		...report.support.map((claim) => `支持：${claim.text} [${claim.source_ids.join(', ')}]`),
		...report.counter.map((claim) => `反证：${claim.text} [${claim.source_ids.join(', ')}]`),
		...report.alternatives.map((claim) => `替代解释：${claim.text} [${claim.source_ids.join(', ')}]`),
		`新仓：${report.decision.new_position}`, `已有仓位：${report.decision.existing_position}`, `计划依据：${report.decision.reason}`,
		...report.conditions.map((condition) => `${report.invalidation_ids.includes(condition.id) ? '失效' : '观察'}条件 ${condition.id}：${condition.text}；${researchConditionValue(condition)}`),
		...(report.decision.price_plan ? [['介入参考',report.decision.price_plan.entry_anchor],['失效参考',report.decision.price_plan.stop_anchor],['压力参考',report.decision.price_plan.target_anchor]].map(([label,id]) => { const anchor=report.anchors.find((item)=>item.id===id);return `${label}：${anchor ? `${anchor.price.toFixed(2)} 元，${anchor.label} [${anchor.source_id}]` : '没有充分依据'}`; }) : []),
		...report.scenarios.map((scenario) => `${scenario.name}：${scenario.description}；条件 ${scenario.condition_ids.join(', ')}；${scenario.response}`),
		...report.limitations.map((item) => `信息限制：${item}`),
		...report.sources.map((item) => `[${item.id}] ${item.title} ${item.report_date || item.published_at || '发布时间未知'} ${safeResearchURL(item.url) || ''}`),
		`模型：${report.model}；生成于：${report.generated_at}；语义解释与未来情景仍需验证。`,
	].join('\n');
}
