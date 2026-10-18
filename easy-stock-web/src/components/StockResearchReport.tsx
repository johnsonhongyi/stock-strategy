import { useEffect, useState } from 'react';
import './stock-research.css';
import { ArrowUpRight, CheckCircle2, CircleAlert, FileText, History, LoaderCircle, SearchCheck, Square, Trash2, X } from 'lucide-react';
import type { StockAIAnalysis } from '../lib/backend';
import { researchTiming } from '../lib/research-timing';
import { conditionStatusLabel, deduplicateResearchHistoryByModelEffort, evidenceLevelLabel, filterResearchHistory, formatCompactResearchDuration, isResearchRunning, researchConditionValue, researchLevelLabel, researchLevelShortLabel, researchModelEffortConfiguration, researchModelEffortKey, researchModelEffortLabel, researchSummaryLevel, safeResearchURL, type ResearchClaim, type ResearchJob, type ResearchJobSummary, type ResearchLevelFilter, type ResearchReport, type ResearchRequest, type ResearchVerification } from '../lib/stock-research';

export function StockResearchOptions({ purpose, horizon, cost, onPurpose, onHorizon, onCost }: { purpose: ResearchRequest['purpose']; horizon: ResearchRequest['horizon']; cost: string; onPurpose: (value: ResearchRequest['purpose']) => void; onHorizon: (value: ResearchRequest['horizon']) => void; onCost: (value: string) => void }) {
	return <div className="stock-research-options">
		<div role="group" aria-label="研究目的">{([['observe', '观察'], ['new_position', '准备新开仓'], ['holding', '已有持仓']] as const).map(([value, label]) => <button type="button" key={value} aria-pressed={purpose === value} className={purpose === value ? 'active' : ''} onClick={() => onPurpose(value)}>{label}</button>)}</div>
		<label>研究周期<select aria-label="研究周期" value={horizon} onChange={(event) => onHorizon(event.target.value as ResearchRequest['horizon'])}><option value="short">超短观察</option><option value="swing">波段研究</option><option value="medium">中期研究</option></select></label>
		{purpose === 'holding' && <label>持仓成本<input aria-label="持仓成本" type="number" min="0.01" step="0.01" value={cost} onChange={(event) => onCost(event.target.value)} placeholder="选填" /></label>}
	</div>;
}

export function StockResearchProgress({ job, onCancel }: { job: ResearchJob | null; onCancel: () => void }) {
	const [now, setNow] = useState(() => Date.now());
	const running = Boolean(job && isResearchRunning(job));
	useEffect(() => {
		if (!running) return;
		const timer = window.setInterval(() => setNow(Date.now()), 1000);
		return () => window.clearInterval(timer);
	}, [running, job?.id]);
	if (!job) return null;
	const elapsed = formatElapsedTime(job.started_at, running ? now : Date.parse(job.completed_at || job.updated_at));
	return <><div className={`stock-research-progress ${job.status}`} role="status">
		{running ? <LoaderCircle className="spin" size={17} /> : job.status === 'succeeded' ? <CheckCircle2 size={17} /> : <CircleAlert size={17} />}
		<span className="stock-research-progress-message">{job.message}{job.error ? `：${job.error}` : ''}</span>
		<strong className="stock-research-progress-elapsed">目前总耗时 {elapsed}</strong>
		{running && <button type="button" onClick={onCancel} title="停止本次研究" aria-label="停止本次研究"><Square size={15} /></button>}
	</div>{!running && <ResearchTiming job={job} />}</>;
}

function ResearchTiming({ job }: { job: ResearchJob }) {
	const timing = researchTiming(job);
 if (!timing.attempts.length) return null;
	const seconds = (value: number | null) => value === null ? '未采集' : `${(value / 1000).toFixed(2)} 秒`;
	const labels: Record<string, string> = { quick: '快速研判', outline: '问题梳理', core: '核心判断', trade: '条件整理', repair: '结构修复' };
	return <details className="stock-research-timing"><summary>耗时分解 · AI调用 {seconds(timing.calls)} · 资料与校验 {seconds(timing.preparation)}</summary>
		<p>服务端总耗时 {seconds(timing.total)}。AI调用包含排队、桥准备、模型等待与输出、结果回传；不等于纯推理耗时。</p>
		{timing.attempts.map((attempt, index) => <div key={index}><strong>{labels[attempt.stage] || attempt.stage} · {seconds(attempt.call)}</strong>
			{attempt.setup !== null ? <p>排队 {seconds(attempt.queue)} · 桥准备 {seconds(attempt.setup)} · 模型等待首段 {seconds(attempt.waiting)} · 输出 {seconds(attempt.output)} · 其余调用链路 {seconds(attempt.other)}</p> : <p>历史记录未采集桥分段计时。</p>}
			{attempt.tokens && <p>输入 {attempt.tokens.inputTokens ?? '未采集'} token · 缓存命中 {attempt.tokens.cachedInputTokens ?? '未采集'} · 推理 {attempt.tokens.reasoningOutputTokens ?? '未采集'}</p>}
		</div>)}
	</details>;
}

function formatElapsedTime(startedAt: string, endAt: number) {
	const started = Date.parse(startedAt);
	if (!Number.isFinite(started) || !Number.isFinite(endAt)) return '计算中';
	const seconds = Math.max(0, Math.floor((endAt - started) / 1000));
	const minutes = Math.floor(seconds / 60);
	return `${minutes}分${String(seconds % 60).padStart(2, '0')}秒`;
}

export function StockResearchHistory({ items, activeID, onOpen, onRemove, onLoadComparison }: { items: ResearchJobSummary[]; activeID: string; onOpen: (id: string) => void; onRemove: (id: string) => void; onLoadComparison?: (ids: string[]) => Promise<ResearchJob[]> }) {
	const [expanded, setExpanded] = useState(false);
	const [levelFilter, setLevelFilter] = useState<ResearchLevelFilter>('all');
	const [modelEffortFilter, setModelEffortFilter] = useState('all');
	const [selectedIDs, setSelectedIDs] = useState<string[]>([]);
	const [comparisonJobs, setComparisonJobs] = useState<ResearchJob[] | null>(null);
	const [comparisonLoading, setComparisonLoading] = useState(false);
	const [comparisonError, setComparisonError] = useState('');
	if (!items.length) return null;
	const selectedItems = items.filter((item) => selectedIDs.includes(item.id));
	const selectedSymbol = selectedItems[0]?.request.symbol.trim().toUpperCase() || '';
	const levelItems = deduplicateResearchHistoryByModelEffort(filterResearchHistory(items, levelFilter));
	const modelEffortCounts = new Map<string, { label: string; title: string; count: number }>();
	for (const item of levelItems) {
		const key = researchModelEffortKey(item) || 'unconfigured';
		const { model, effort } = researchModelEffortConfiguration(item);
		const current = modelEffortCounts.get(key);
		if (current) current.count += 1;
		else modelEffortCounts.set(key, {
			label: key === 'unconfigured' ? '未记录配置' : researchModelEffortLabel(model, effort),
			title: key === 'unconfigured' ? '该记录没有完整的模型与推理强度信息' : `${model} / ${effort}`,
			count: 1,
		});
	}
	const visibleItems = modelEffortFilter === 'all' ? levelItems : levelItems.filter((item) => (researchModelEffortKey(item) || 'unconfigured') === modelEffortFilter);
	const toggleComparisonItem = (item: ResearchJobSummary, checked: boolean) => {
		setSelectedIDs((current) => {
			if (!checked) return current.filter((id) => id !== item.id);
			const symbol = item.request.symbol.trim().toUpperCase();
			const currentSymbol = items.find((candidate) => current.includes(candidate.id))?.request.symbol.trim().toUpperCase() || '';
			if (current.includes(item.id) || current.length >= 4 || (currentSymbol && symbol !== currentSymbol)) return current;
			return [...current, item.id];
		});
	};
	const openComparison = async () => {
		if (!onLoadComparison || selectedItems.length < 2) return;
		setComparisonLoading(true);
		setComparisonError('');
		try {
			const jobs = await onLoadComparison(selectedItems.map((item) => item.id));
			const complete = jobs.filter((job) => job.analysis?.research_report);
			if (complete.length < 2) throw new Error('至少需要两条包含完整研究报告的记录才能对比');
			setComparisonJobs(complete);
			if (complete.length !== jobs.length) setComparisonError(`${jobs.length - complete.length} 条记录缺少完整报告，已从对比中跳过`);
		} catch (reason) {
			setComparisonError(reason instanceof Error ? reason.message : '读取对比报告失败');
		} finally {
			setComparisonLoading(false);
		}
	};
	const filters: Array<{ value: ResearchLevelFilter; label: string }> = [
		{ value: 'all', label: '全部' }, { value: 'quantitative', label: '量化' }, { value: 'quick', label: '快速' },
		{ value: 'standard', label: '标准' }, { value: 'deep', label: '深度' },
	];
	return <section className="stock-research-history" aria-label="研究历史">
		<header><History size={15} /><strong>研究记录</strong><span>{visibleItems.length === items.length ? items.length : visibleItems.length + '/' + items.length}</span><div className="stock-research-history-actions">
			{selectedItems.length > 0 && <button type="button" onClick={() => setSelectedIDs([])}>清空选择</button>}
			<button type="button" disabled={!onLoadComparison || selectedItems.length < 2 || comparisonLoading} onClick={() => void openComparison()}>{comparisonLoading ? '读取中…' : `只看差异 ${selectedItems.length}`}</button>
			<button type="button" onClick={() => setExpanded(!expanded)}>{expanded ? '收起' : '展开'}</button>
		</div></header>
		<div className="stock-research-filters" role="group" aria-label="按分析深度筛选研究记录">{filters.map(({ value, label }) => {
			const count = deduplicateResearchHistoryByModelEffort(filterResearchHistory(items, value)).length;
			return <button type="button" key={value} aria-pressed={levelFilter === value} onClick={() => { setLevelFilter(value); setModelEffortFilter('all'); setExpanded(value !== 'all'); }}>{label}<span>{count}</span></button>;
		})}</div>
		<div className="stock-research-filters stock-research-model-filters" role="group" aria-label="按模型与推理强度筛选研究记录">
			<span className="stock-research-filter-label">模型 / 推理强度</span>
			<button type="button" aria-pressed={modelEffortFilter === 'all'} onClick={() => { setModelEffortFilter('all'); setExpanded(false); }}>全部配置<span>{levelItems.length}</span></button>
			{[...modelEffortCounts].map(([key, option]) => <button type="button" key={key} title={option.title} aria-pressed={modelEffortFilter === key} onClick={() => { setModelEffortFilter(key); setExpanded(true); }}>{option.label}<span>{option.count}</span></button>)}
			<small className="stock-research-dedup-note">同一股票相同配置只显示最新记录</small>
		</div>
		{selectedItems.length > 0 && !comparisonJobs && <div className="stock-research-compare-selection" role="status">
			<span>{selectedItems.length === 1 ? `已选 ${selectedItems[0].name}，再选同一股票的另一条记录` : `已选 ${selectedItems.length} 条 ${selectedItems[0].name} 记录`} · 最多 4 条</span>
			{comparisonError && <strong role="alert">{comparisonError}</strong>}
		</div>}
		{comparisonJobs ? <StockResearchComparisonView jobs={comparisonJobs} summaries={items} note={comparisonError} onClose={() => setComparisonJobs(null)} /> : visibleItems.length ? <div className={`stock-research-history-list${expanded ? ' expanded' : ''}`}>{visibleItems.slice(0, expanded ? 50 : 4).map((item) => {
			const level = researchSummaryLevel(item);
			const duration = formatCompactResearchDuration(item.duration_ms);
			const configuration = researchModelEffortConfiguration(item);
			const selected = selectedIDs.includes(item.id);
			const ticker = item.request.symbol.trim().toUpperCase();
			const selectionDisabled = item.status !== 'succeeded' || (selectedSymbol !== '' && ticker !== selectedSymbol) || (selectedItems.length >= 4 && !selected);
			return <article key={item.id} className={`${item.id === activeID ? 'active ' : ''}${selected ? 'comparison-selected' : ''}`.trim()}>
				<button type="button" onClick={() => onOpen(item.id)}>
					<strong className="stock-research-card-title">{item.name}<span className={'stock-research-tag level-' + level} title={researchLevelLabel(level)} aria-label={'分析深度 ' + researchLevelLabel(level)}>{researchLevelShortLabel(level)}</span>
						{duration && <span className="stock-research-tag stock-research-duration" aria-label={'耗时 ' + duration}>{duration}</span>}
						{configuration.model && <span className="stock-research-tag stock-research-model" title={'模型：' + configuration.model} aria-label={'模型 ' + configuration.model}>{configuration.model}</span>}
						{configuration.effort && <span className="stock-research-tag stock-research-effort" title="推理强度" aria-label={'推理强度 ' + configuration.effort}>{configuration.effort}</span>}
					</strong>
					<time>{formatDate(item.started_at)}</time>
					<span className="stock-research-summary">{isResearchRunning(item) ? item.message : item.headline || item.message}</span>
				</button>
				<label className="stock-research-compare-check" title={item.status !== 'succeeded' ? '仅支持已完成的研究记录' : selectedSymbol && ticker !== selectedSymbol ? '只能对比同一股票' : '选择此记录进行差异对比'}>
					<input type="checkbox" aria-label={`选择${item.name}进行差异对比`} checked={selected} disabled={selectionDisabled} onChange={(event) => toggleComparisonItem(item, event.target.checked)} />
					<span>比对</span>
				</label>
				<button type="button" disabled={isResearchRunning(item)} onClick={() => onRemove(item.id)} title={'删除' + item.name + '本次记录'} aria-label={'删除' + item.name + '本次记录'}><Trash2 size={13} /></button>
			</article>;
		})}</div> : <p className="stock-research-history-empty">{modelEffortFilter === 'all' ? '暂无该分析深度的研究记录' : '暂无该模型与推理强度的研究记录'}</p>}
	</section>;
}

type ResearchComparisonColumn = { job: ResearchJob; summary?: ResearchJobSummary; report: ResearchReport; totalMS?: number; aiCallMS?: number; model: string; effort: string; provider: string };

export function StockResearchComparisonView({ jobs, summaries, note, onClose }: { jobs: ResearchJob[]; summaries: ResearchJobSummary[]; note?: string; onClose: () => void }) {
	const summaryByID = new Map(summaries.map((item) => [item.id, item]));
	const columns: ResearchComparisonColumn[] = jobs.flatMap((job) => {
		const report = job.analysis?.research_report;
		if (!report) return [];
		const summary = summaryByID.get(job.id);
		const configuration = researchModelEffortConfiguration({
			request: {
				model: report.request.model || job.request.model || summary?.request.model,
				reasoning_effort: report.request.reasoning_effort || job.request.reasoning_effort || summary?.request.reasoning_effort,
			},
			model: report.model || summary?.model,
			reasoning_effort: summary?.reasoning_effort,
		});
		const model = configuration.model || '未记录模型';
		const effort = configuration.effort;
		const parsedStart = Date.parse(job.started_at);
		const parsedEnd = Date.parse(job.completed_at || job.updated_at);
		const derivedDuration = Number.isFinite(parsedStart) && Number.isFinite(parsedEnd) && parsedEnd >= parsedStart ? parsedEnd - parsedStart : undefined;
		const totalMS = summary?.duration_ms ?? derivedDuration;
		const aiCallMS = report.attempts.length ? report.attempts.reduce((sum, attempt) => sum + attempt.duration_ms, 0) : undefined;
		const providerName: Record<string, string> = { hermes: 'Hermes', codex_desktop: 'Codex Desktop', antigravity_desktop: 'Antigravity Desktop' };
		const provider = providerName[report.request.ai_provider || job.request.ai_provider || ''] || '模型服务';
		return [{ job, summary, report, totalMS, aiCallMS, model, effort, provider }];
	});
	const reports = columns.map((column) => column.report);
	const fields = researchComparisonFields(reports);
	const differences = fields.filter((field) => new Set(field.values.map(normalizeComparisonValue)).size > 1);
	const totalDurations = columns.map((column) => column.totalMS).filter((value): value is number => value !== undefined && Number.isFinite(value));
	const fastestMS = totalDurations.length >= 2 ? Math.min(...totalDurations) : undefined;
	const mismatch = researchComparisonInputMismatches(reports);
	const symbol = reports[0]?.request.symbol || jobs[0]?.analysis?.symbol || '';
	const name = jobs[0]?.analysis?.name || summaryByID.get(jobs[0]?.id || '')?.name || symbol;
	return <section className="stock-research-comparison" aria-label="研究记录差异对比">
		<header><div><strong>{name} · 研究差异</strong><span>{columns.length} 份报告；已隐藏相同项目，仅列 {differences.length} 项差异</span></div><button type="button" onClick={onClose} aria-label="关闭差异对比"><X size={16} />返回记录</button></header>
		{note && <p className="stock-research-comparison-note">{note}</p>}
		{mismatch.length > 0 ? <p className="stock-research-comparison-warning">输入不完全一致：{mismatch.join('、')}。结论差异可能来自输入或时点变化，不能只归因于模型。</p> : <p className="stock-research-comparison-note">研究目的、周期、分析深度及证据快照一致，模型间差异更具可比性。</p>}
		<div className="stock-research-comparison-snapshot" aria-label="模型结论速览">
			<strong>主要结论速览 <span>先看结论与建议；表格保留完整内容</span></strong>
			<div>{columns.map((column, index) => {
				const recommendation = researchComparisonRecommendation(column.report);
				return <article key={column.job.id} data-comparison-color={index % 4}>
					<header><b>{column.provider} · {column.model}</b><small>{column.effort || '推理强度未记录'} · {formatComparisonDuration(column.totalMS)}</small></header>
					<p>{column.report.headline || '未记录结论'}</p>
					<span>{recommendation.label}：{recommendation.text}</span>
				</article>;
			})}</div>
		</div>
		<p className="stock-research-comparison-guide">颜色对应模型；正文只在结论和建议中点亮少量差异短语，长篇依据不着色。首列只是文字参照，不代表优劣。</p>
		{columns.length >= 2 ? <div className="stock-research-comparison-table-wrap"><table><thead><tr><th scope="col">差异项目</th>{columns.map((column, index) => <th scope="col" key={column.job.id} data-comparison-color={index % 4}>
			<strong>{column.provider} · {column.model}</strong><small>推理强度：{column.effort || '未记录'}</small>
			<span className="stock-research-comparison-timing">总耗时 {formatComparisonDuration(column.totalMS)}{column.totalMS !== undefined && column.totalMS === fastestMS ? <em>总耗时最快</em> : null}</span>
			<small>AI调用（含桥接）{formatComparisonDuration(column.aiCallMS)}</small>
			<small>分析时点 {formatDate(column.report.cutoff_at)} · 快照 {(column.report.snapshot_id || '未知快照').slice(0, 8)}</small>
		</th>)}</tr></thead><tbody>{differences.map((field) => {
			const highlight = isPrimaryResearchDifference(field.label);
			const differenceMasks = highlight ? researchComparisonDiffMasks(field.values) : [];
			return <tr key={field.label}><th scope="row">{field.label}</th>{field.values.map((value, index) => <td key={columns[index].job.id} data-comparison-color={index % 4}><span className="stock-research-comparison-value">{highlight ? renderResearchComparisonDiff(value || '未记录', differenceMasks[index], index % 4) : value || '未记录'}</span></td>)}</tr>;
		})}</tbody></table>
		{differences.length === 0 && <p className="stock-research-comparison-empty">所选报告的研判项目一致；模型与耗时信息见上方。</p>}
		</div> : <p className="stock-research-comparison-empty">所选记录中没有两份包含完整研究报告的结果。</p>}
	</section>;
}

function researchComparisonFields(reports: ResearchReport[]) {
	const purposeLabel: Record<string, string> = { observe: '观察', new_position: '准备新开仓', holding: '已有持仓' };
	const horizonLabel: Record<string, string> = { short: '超短观察', swing: '波段研究', medium: '中期研究' };
	const fields: Array<{ label: string; values: string[] }> = [
		{ label: '结论', values: reports.map((report) => report.headline || '未记录') },
		{ label: '核心判断', values: reports.map((report) => report.thesis?.text || '未记录') },
		{ label: '新仓建议', values: reports.map((report) => report.decision.new_position || '未记录') },
		{ label: '持仓建议', values: reports.map((report) => report.decision.existing_position || '未记录') },
		{ label: '建议理由', values: reports.map((report) => report.decision.reason || '未记录') },
		{ label: '价格条件', values: reports.map(researchComparisonPlan) },
		{ label: '核心分歧', values: reports.map((report) => report.main_conflict || '未记录') },
		{ label: '量化基线', values: reports.map((report) => `${report.baseline_relation || '未评估'}：${report.baseline_reason || '未记录'}`) },
		{ label: '支持依据', values: reports.map((report) => formatComparisonClaims(report.support)) },
		{ label: '反对依据', values: reports.map((report) => formatComparisonClaims(report.counter)) },
		{ label: '替代解释', values: reports.map((report) => formatComparisonClaims(report.alternatives)) },
		{ label: '证据方向与结构', values: reports.map((report) => {
			const direction = report.support.length && report.counter.length ? '双向论据' : report.support.length ? '仅列支持依据' : report.counter.length ? '仅列反对依据' : '未列支持或反对依据';
			return `证据${evidenceLevelLabel(report.evidence_level)}；${direction}；支持 ${report.support.length} / 反对 ${report.counter.length} / 替代 ${report.alternatives.length}`;
		}) },
		{ label: '来源引用覆盖', values: reports.map((report) => {
			const claims = [report.thesis, ...report.support, ...report.counter, ...report.alternatives];
			return `${claims.filter((claim) => claim?.source_ids?.length > 0).length}/${claims.length} 项主张有来源引用`;
		}) },
		{ label: '资料来源', values: reports.map((report) => {
			const dated = report.sources.filter((source) => source.time_status === 'dated').length;
			const titles = report.sources.slice(0, 8).map((source) => source.title).filter(Boolean);
			return `${report.sources.length} 条；有时间标注 ${dated} 条${titles.length ? `\n${titles.join('\n')}` : ''}`;
		}) },
		{ label: '观察与失效条件', values: reports.map((report) => report.conditions.map((condition) => `${report.invalidation_ids.includes(condition.id) ? '失效' : '观察'}：${condition.text}（${researchConditionValue(condition)}）`).join('\n') || '未形成条件') },
		{ label: '信息限制', values: reports.map((report) => report.limitations.join('\n') || '未记录') },
		{ label: '证据快照', values: reports.map((report) => report.snapshot_id || '未记录') },
		{ label: '研究输入', values: reports.map((report) => `${purposeLabel[report.request.purpose] || report.request.purpose} / ${horizonLabel[report.request.horizon] || report.request.horizon} / ${researchLevelLabel(report.analysis_level || report.request.analysis_level)}${report.request.cost_price == null ? '' : ` / 成本 ${report.request.cost_price.toFixed(2)} 元`}`) },
		{ label: '行情与资料截止', values: reports.map((report) => report.cutoff_at || '未记录') },
	];
	return fields;
}

function researchComparisonPlan(report: ResearchReport) {
	const plan = report.decision.price_plan;
	if (!plan) return '未形成价格条件';
	const anchors = [
		{ label: '介入', id: plan.entry_anchor }, { label: '失效', id: plan.stop_anchor },
		...(plan.target_anchor ? [{ label: '目标', id: plan.target_anchor }] : []),
	];
	return anchors.map(({ label, id }) => {
		const anchor = report.anchors.find((item) => item.id === id);
		return anchor ? `${label} ${anchor.price.toFixed(2)} 元（${anchor.label}）` : `${label} ${id}`;
	}).join('；') + `\n${plan.reason}`;
}

function formatComparisonClaims(claims: ResearchClaim[]) {
	return claims.length ? claims.map((claim) => `• ${claim.text}`).join('\n') : '未列出';
}

function normalizeComparisonValue(value: string) {
	return value.trim().replace(/\s+/g, ' ').toLowerCase();
}

function researchComparisonInputMismatches(reports: ResearchReport[]) {
	const mismatch = (values: string[]) => new Set(values).size > 1;
	const result: string[] = [];
	if (mismatch(reports.map((report) => `${report.request.purpose}/${report.request.horizon}/${report.analysis_level || report.request.analysis_level || ''}/${report.request.cost_price ?? ''}`))) result.push('研究目的、周期、深度或成本');
	if (mismatch(reports.map((report) => report.snapshot_id))) result.push('证据快照');
	if (mismatch(reports.map((report) => report.cutoff_at))) result.push('分析时点');
	return result;
}

function formatComparisonDuration(value?: number) {
	if (value == null || !Number.isFinite(value)) return '未采集';
	const seconds = value / 1000;
	return seconds < 60 ? `${seconds.toFixed(1)} 秒` : `${Math.floor(seconds / 60)} 分 ${Math.round(seconds % 60)} 秒`;
}

function isPrimaryResearchDifference(label: string) {
	return ['结论', '核心判断', '新仓建议', '持仓建议', '价格条件'].includes(label);
}

function researchComparisonRecommendation(report: ResearchReport) {
	if (report.request.purpose === 'holding') return { label: '持仓建议', text: report.decision.existing_position || '未形成建议' };
	if (report.request.purpose === 'new_position') return { label: '新仓建议', text: report.decision.new_position || '未形成建议' };
	return { label: '观察重点', text: report.decision.reason || '未形成建议' };
}

function researchComparisonDiffMasks(values: string[]) {
	const masks = values.map((value) => new Array(researchDiffTokens(value).length).fill(false));
	for (let index = 1; index < values.length; index += 1) {
		if (normalizeComparisonValue(values[0]) === normalizeComparisonValue(values[index])) continue;
		const difference = researchTokenDiff(researchDiffTokens(values[0]), researchDiffTokens(values[index]));
		for (let token = 0; token < difference.left.length; token += 1) masks[0][token] ||= difference.left[token];
		masks[index] = difference.right;
	}
	return masks;
}

function researchDiffTokens(value: string) {
	return value.match(/[\u3400-\u9fff]|[A-Za-z0-9]+|\s+|[^\s]/g) || [];
}

function researchTokenDiff(left: string[], right: string[]) {
	const leftChanged = new Array(left.length).fill(false);
	const rightChanged = new Array(right.length).fill(false);
	const cells = (left.length + 1) * (right.length + 1);
	if (cells > 300_000) {
		let prefix = 0;
		while (prefix < left.length && prefix < right.length && left[prefix] === right[prefix]) prefix += 1;
		let leftEnd = left.length;
		let rightEnd = right.length;
		while (leftEnd > prefix && rightEnd > prefix && left[leftEnd - 1] === right[rightEnd - 1]) { leftEnd -= 1; rightEnd -= 1; }
		for (let index = prefix; index < Math.min(leftEnd, prefix + 80); index += 1) leftChanged[index] = true;
		for (let index = prefix; index < Math.min(rightEnd, prefix + 80); index += 1) rightChanged[index] = true;
		return { left: leftChanged, right: rightChanged };
	}
	const width = right.length + 1;
	const table = new Uint16Array(cells);
	for (let row = 1; row <= left.length; row += 1) {
		const rowOffset = row * width;
		const previousOffset = (row - 1) * width;
		for (let column = 1; column <= right.length; column += 1) {
			table[rowOffset + column] = left[row - 1] === right[column - 1]
				? table[previousOffset + column - 1] + 1
				: Math.max(table[previousOffset + column], table[rowOffset + column - 1]);
		}
	}
	let row = left.length;
	let column = right.length;
	while (row > 0 && column > 0) {
		if (left[row - 1] === right[column - 1]) { row -= 1; column -= 1; }
		else if (table[(row - 1) * width + column] >= table[row * width + column - 1]) { leftChanged[row - 1] = true; row -= 1; }
		else { rightChanged[column - 1] = true; column -= 1; }
	}
	while (row > 0) { leftChanged[--row] = true; }
	while (column > 0) { rightChanged[--column] = true; }
	return { left: leftChanged, right: rightChanged };
}

function renderResearchComparisonDiff(value: string, mask: boolean[], color: number) {
	const tokens = researchDiffTokens(value);
	if (tokens.length !== mask.length) return value;
	const runs: Array<{ text: string; changed: boolean }> = [];
	for (let index = 0; index < tokens.length; index += 1) {
		const changed = mask[index] && /[\u3400-\u9fffA-Za-z0-9]/.test(tokens[index]);
		const previous = runs[runs.length - 1];
		if (previous && previous.changed === changed) previous.text += tokens[index];
		else runs.push({ text: tokens[index], changed });
	}
	let markedCharacters = 0;
	let markedRuns = 0;
	return runs.map((run, index) => {
		if (!run.changed || markedCharacters >= 48 || markedRuns >= 3) return <span key={index}>{run.text}</span>;
		const remaining = 48 - markedCharacters;
		const markedText = [...run.text].slice(0, remaining).join('');
		const rest = [...run.text].slice(remaining).join('');
		markedCharacters += [...markedText].length;
		markedRuns += 1;
		return <span key={index}><mark className="stock-research-diff-mark" data-comparison-color={color}>{markedText}</mark>{rest}</span>;
	});
}

export function StockResearchReportView({ analysis, view = 'research', verification, verifying, onVerify }: { analysis: StockAIAnalysis; view?: 'research' | 'expectation' | 'risk'; verification?: ResearchVerification; verifying?: boolean; onVerify?: () => void }) {
	const report = analysis.research_report;
	const [tab, setTab] = useState<'research' | 'evidence'>('research');
	const [sourceID, setSourceID] = useState('');
	if (!report) return null;
	const openSource = (id: string) => { setTab('evidence'); setSourceID(id); };
	return <div className="stock-research-report">
		<header className="stock-research-report-header"><div><strong>{view === 'risk' ? '条件与执行边界' : view === 'expectation' ? '情景与后续核验' : 'AI研究判断'}</strong><span>{researchLevelLabel(report.analysis_level || report.request.analysis_level)} · 证据{evidenceLevelLabel(report.evidence_level)} · {report.model || '当前模型'}</span></div><div role="tablist" aria-label="研究内容"><button role="tab" aria-selected={tab === 'research'} onClick={() => setTab('research')}>研判</button><button role="tab" aria-selected={tab === 'evidence'} onClick={() => setTab('evidence')}>证据 {report.sources.length}</button></div></header>
		{tab === 'evidence' ? <ResearchEvidence report={report} selectedID={sourceID} /> : <>
			{view === 'research' && <><div className="stock-research-thesis"><h3>{report.headline}</h3><Claim claim={report.thesis} onSource={openSource} /><p><strong>核心分歧</strong>{report.main_conflict || '暂未形成明确的分歧判断'}</p></div>
			<div className="stock-research-arguments"><section><h4>支持依据</h4>{report.support.length ? report.support.map((claim, i) => <Claim key={i} claim={claim} onSource={openSource} />) : <p>尚未取得足够支持依据</p>}</section><section><h4>反对依据</h4>{report.counter.length ? report.counter.map((claim, i) => <Claim key={i} claim={claim} onSource={openSource} />) : <p>未取得直接反证，不代表不存在风险</p>}</section></div>
			{report.alternatives.length > 0 && <section className="stock-research-band"><h4>替代解释</h4>{report.alternatives.map((claim, i) => <Claim key={i} claim={claim} onSource={openSource} />)}</section>}
			<section className="stock-research-baseline"><strong>{report.baseline_relation === 'disagree' ? '与量化基线存在分歧' : report.baseline_relation === 'agree' ? '与量化基线方向一致' : '尚不足以与量化基线比较'}</strong><p>{report.baseline_reason}</p><span>量化基线 {analysis.scorecard.overall} · AI未改写基线分数</span></section></>}
			<ResearchDecision analysis={analysis} onSource={openSource} />
			<section className="stock-research-band"><header><h4>确认与失效条件</h4>{onVerify && <button type="button" onClick={onVerify} disabled={verifying}>{verifying ? <LoaderCircle size={14} className="spin" /> : <SearchCheck size={14} />}核对后续行情</button>}</header>
				{report.conditions.length ? report.conditions.map((condition) => { const check = verification?.checks.find((item) => item.condition_id === condition.id); return <div className="stock-research-condition" key={condition.id}><span className={report.invalidation_ids.includes(condition.id) ? 'invalidation' : ''}>{report.invalidation_ids.includes(condition.id) ? '失效条件' : '观察条件'}</span><div><strong>{condition.text}</strong><p>{researchConditionValue(condition)}</p><small>{condition.window === 'next_close' ? '下一交易日收盘' : condition.window === 'next_disclosure' ? '后续披露' : '后续5个交易日'}{check ? ` · ${check.detail}${check.as_of ? ` · ${check.as_of}` : ''}` : ''}</small></div><em>{conditionStatusLabel(check?.status || 'pending')}</em></div>; }) : <p>尚未形成可核验条件</p>}
				{verification && <p className="stock-research-muted">{verification.summary} · {formatDate(verification.checked_at)}</p>}
			</section>
			{view !== 'risk' && report.scenarios.length > 0 && <section className="stock-research-band"><h4>条件情景</h4><div className="stock-research-scenarios">{report.scenarios.map((scenario) => <article key={scenario.key}><strong>{scenario.name}</strong><p>{scenario.description}</p><small>{scenario.condition_ids.map((id) => report.conditions.find((item) => item.id === id)?.text).filter(Boolean).join('；')}</small><p>{scenario.response}</p></article>)}</div></section>}
			<section className="stock-research-band"><h4>信息缺口与限制</h4><ul>{report.limitations.map((item, i) => <li key={i}>{item}</li>)}</ul></section>
		</>}
		<footer className="stock-research-footer"><span>分析时点 {formatDate(report.cutoff_at)} · 快照 {report.snapshot_id.slice(0, 8)} · v{report.snapshot_version}</span><span>{report.attempts.length} 次模型调用 · {Math.round(report.attempts.reduce((sum, item) => sum + item.duration_ms, 0) / 1000)} 秒AI调用耗时（含桥接） · {report.prompt_version}{report.compression ? ` · 证据${report.compression.original_content_bytes > report.compression.selected_content_bytes ? '已压缩' : '未压缩'}` : ''}</span></footer>
	</div>;
}

function Claim({ claim, onSource }: { claim: ResearchClaim; onSource: (id: string) => void }) {
	return <div className="stock-research-claim"><span>{claim.kind === 'fact' ? '原文陈述' : claim.kind === 'opinion' ? '第三方观点' : '研究推断'}</span><p>{claim.text}</p><div>{claim.source_ids.map((id) => <button type="button" key={id} onClick={() => onSource(id)} title={`查看证据 ${id}`}><FileText size={12} />{id}</button>)}</div></div>;
}

function ResearchDecision({ analysis, onSource }: { analysis: StockAIAnalysis; onSource: (id: string) => void }) {
	const report = analysis.research_report!;
	const plan = report.decision.price_plan;
	return <section className="stock-research-band"><h4>{report.decision.status === 'no_plan' ? '暂不形成交易计划' : report.decision.status === 'observe' ? '观察与核实' : '条件化计划'}</h4><p>{report.decision.reason}</p><div className="stock-research-positions"><div><strong>准备新开仓</strong><p>{report.decision.new_position}</p></div><div><strong>已有仓位{report.request.cost_price ? ` · 成本 ${report.request.cost_price.toFixed(2)} 元` : ''}</strong><p>{report.decision.existing_position}</p></div></div>
		{plan && <div className="stock-research-prices">{[['介入参考', plan.entry_anchor], ['失效参考', plan.stop_anchor], ['压力参考', plan.target_anchor]].map(([label, id]) => { const anchor = report.anchors.find((item) => item.id === id); return <div key={label}><span>{label}</span><strong>{anchor ? `${anchor.price.toFixed(2)} 元` : '没有充分依据'}</strong>{anchor && <button type="button" onClick={() => onSource(anchor.source_id)}>{anchor.label}<ArrowUpRight size={12} /></button>}</div>; })}</div>}
	</section>;
}

function ResearchEvidence({ report, selectedID }: { report: ResearchReport; selectedID: string }) {
	const ordered = selectedID ? [...report.sources].sort((left, right) => Number(right.id === selectedID) - Number(left.id === selectedID)) : report.sources;
	return <div className="stock-research-evidence"><section className="stock-research-band"><h4>补证记录</h4>{report.questions.length ? report.questions.map((item, i) => <article key={i}><strong>{item.question}</strong><p>{item.why}</p><small>{item.outcome || '未进一步查询'}</small></article>) : <p>本次未提出额外补证请求</p>}</section>
		{ordered.map((source) => <details key={`${source.id}-${selectedID}`} open={source.id === selectedID || undefined} className={source.id === selectedID ? 'selected' : ''}><summary><FileText size={15} /><strong>{source.title}</strong><span>{source.id}</span></summary><div><p className="stock-research-source-meta">{source.provider || '来源未标注'} · {source.kind} · {source.time_status === 'publication_unknown' ? '发布时间未知' : source.report_date || formatDate(source.published_at || '')}</p><pre>{source.content}</pre>{safeResearchURL(source.url) && <a href={safeResearchURL(source.url)} target="_blank" rel="noreferrer">查看原始来源<ArrowUpRight size={13} /></a>}</div></details>)}
		<section className="stock-research-band"><h4>校验记录</h4><ul>{report.validation_notes.map((note, i) => <li key={i}>{note}</li>)}</ul></section>
	</div>;
}

function formatDate(value: string) { const date = new Date(value); return Number.isNaN(date.getTime()) || date.getFullYear() < 2000 ? '时间未知' : date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }); }
