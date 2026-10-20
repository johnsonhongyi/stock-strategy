import {
	Activity,
	CheckCircle2,
	ChevronRight,
	CircleAlert,
	Clock3,
	ExternalLink,
	History,
	LoaderCircle,
	Plus,
	RefreshCw,
	ShieldCheck,
	Trash2,
	WalletCards,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
	AppSettings,
	BackendConfig,
	PortfolioInspectionJob,
	PortfolioInspectionReport,
	PortfolioTraderProfile,
	StockAIAnalysis,
	StockDirectoryData,
	StockDirectoryEntry,
	requestJSON,
} from '../lib/backend';
import { desktopModelGroups, resolveAvailableDesktopModelSelection, selectedDesktopModelGroup, type DesktopModelGroup } from '../lib/desktop-model-groups';
import { desktopChatProviderLabel, isDesktopChatProvider, loadAvailableDesktopChatTargets, loadChatRuntime, saveChatRuntime, targetsForDesktopProvider, type AvailableDesktopChatTarget, type DesktopChatProvider } from '../lib/desktop-chat';
import { bridgeTargetLabel } from '../lib/linkage-display';
import { portfolioDraftToHoldings, portfolioProfiles, readPortfolioDraft, writePortfolioDraft } from '../lib/portfolio-draft';
import { PortfolioSetupForm } from './PortfolioSetupForm';

type Props = {
	config: BackendConfig | null;
	refreshKey: number;
	onOpenSettings: () => void;
	onOpenStockAnalysis: (analysis: StockAIAnalysis) => void;
};

const directoryStorageKey = 'easy-stock.stock-directory.v1';
type InspectionRuntimePreference = { provider: 'hermes' | DesktopChatProvider; node_id: string; workspace_id: string; model: string; reasoning_effort: string };

function runtimeNodeLabel(target: AvailableDesktopChatTarget) {
	return bridgeTargetLabel(target);
}

function inspectionProviderLabel(provider?: string) {
	if (provider === 'codex_desktop' || provider === 'antigravity_desktop') return desktopChatProviderLabel(provider);
	return 'Hermes';
}

import { startAutoRefresh, useAutoRefresh } from '../lib/use-auto-refresh';

export function PortfolioInspectionWorkspace({ config, refreshKey, onOpenSettings, onOpenStockAnalysis }: Props) {
	const [runtimePreference, setRuntimePreference] = useState<InspectionRuntimePreference>({ provider: 'hermes', node_id: '', workspace_id: '', model: '', reasoning_effort: '' });
	const [desktopTargets, setDesktopTargets] = useState<AvailableDesktopChatTarget[]>([]);
	const [hermesReady, setHermesReady] = useState(false);
	const [runtimeChecked, setRuntimeChecked] = useState(false);
	const [runtimeMessage, setRuntimeMessage] = useState('正在检查 AI 运行时…');
	const [runtimeNotice, setRuntimeNotice] = useState('');
	const [runtimeLoading, setRuntimeLoading] = useState(false);
	const [draft, setDraft] = useState(readPortfolioDraft);
	const [directory, setDirectory] = useState<StockDirectoryEntry[]>(loadCachedDirectory);
	const [history, setHistory] = useState<PortfolioInspectionJob[]>([]);
	const [job, setJob] = useState<PortfolioInspectionJob | null>(null);
	const [deletingInspectionID, setDeletingInspectionID] = useState('');
	const [loading, setLoading] = useState(true);
	const [starting, setStarting] = useState(false);
	const [error, setError] = useState('');
	const [notice, setNotice] = useState('');

	const totalWeight = useMemo(() => draft.holdings.reduce((total, item) => total + item.weight, 0), [draft.holdings]);
	const running = job?.status === 'running';
	const providerTargets = isDesktopChatProvider(runtimePreference.provider) ? targetsForDesktopProvider(desktopTargets, runtimePreference.provider) : [];
	const selectedTarget = providerTargets.find((target) => target.node_id === runtimePreference.node_id && target.workspace_id === runtimePreference.workspace_id) || providerTargets[0] || null;
	const modelGroups = useMemo(() => selectedTarget ? desktopModelGroups(selectedTarget.runtime.models, runtimePreference.provider) : [], [selectedTarget, runtimePreference.provider]);
	const resolvedModel = selectedTarget && isDesktopChatProvider(runtimePreference.provider)
		? resolveAvailableDesktopModelSelection(selectedTarget.runtime.models, runtimePreference.provider, runtimePreference.model || selectedTarget.runtime.config.model, runtimePreference.reasoning_effort || selectedTarget.runtime.config.reasoning_effort)
		: null;
	const selectedModelGroup = resolvedModel ? selectedDesktopModelGroup(modelGroups, resolvedModel.model) : undefined;
	const runtimeReady = runtimePreference.provider === 'hermes' ? hermesReady : Boolean(selectedTarget && resolvedModel);

	const saveRuntimePreference = useCallback(async (next: InspectionRuntimePreference) => {
		if (!config) return;
		setRuntimeLoading(true);
		try {
			const saved = await saveChatRuntime(config, next, true);
			setRuntimePreference(saved.config);
			window.dispatchEvent(new Event('ai-runtime-changed'));
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : '保存系统 AI 运行时失败');
		} finally {
			setRuntimeLoading(false);
		}
	}, [config]);

	const refreshRuntime = useCallback(async () => {
		if (!config) return;
		setRuntimeLoading(true);
		try {
			const [settings, globalRuntime, available] = await Promise.all([
				requestJSON<{ data: AppSettings }>(config, '/api/v1/settings').catch(() => null),
				loadChatRuntime(config).catch(() => null),
				loadAvailableDesktopChatTargets(config, 'stock_research_v1').catch(() => null),
			]);
			const nextTargets = available?.targets || [];
			const hermesStatus = settings?.data.hermes;
			const readyHermes = Boolean(hermesStatus?.available && hermesStatus.configured);
			setHermesReady(readyHermes);
			setDesktopTargets(nextTargets);
			if (globalRuntime) {
				const provider = globalRuntime.config.provider;
				const candidates = isDesktopChatProvider(provider) ? targetsForDesktopProvider(nextTargets, provider) : [];
				const target = isDesktopChatProvider(provider)
					? candidates.find((item) => item.node_id === globalRuntime.config.node_id && item.workspace_id === globalRuntime.config.workspace_id)
						|| candidates.find((item) => item.node_id === available?.strategyTarget?.node_id && item.workspace_id === available?.strategyTarget?.workspace_id)
						|| candidates[0] || null
					: null;
				const model = target && isDesktopChatProvider(provider)
					? resolveAvailableDesktopModelSelection(target.runtime.models, provider, globalRuntime.config.model || target.runtime.config.model, globalRuntime.config.reasoning_effort || target.runtime.config.reasoning_effort)
					: null;
				const group = target && model && isDesktopChatProvider(provider) ? selectedDesktopModelGroup(desktopModelGroups(target.runtime.models, provider), model.model) : undefined;
				const next: InspectionRuntimePreference = {
					provider,
					node_id: target?.node_id || globalRuntime.config.node_id || '',
					workspace_id: target?.workspace_id || globalRuntime.config.workspace_id || '',
					model: group?.id || globalRuntime.config.model || '',
					reasoning_effort: model?.reasoning_effort || globalRuntime.config.reasoning_effort || '',
				};
				setRuntimePreference(next);
				if (target && isDesktopChatProvider(provider) && model) {
					const aligned = { ...globalRuntime.config, node_id: target.node_id, workspace_id: target.workspace_id, model: model.model, reasoning_effort: model.reasoning_effort };
					if (JSON.stringify(aligned) !== JSON.stringify(globalRuntime.config)) {
						await saveChatRuntime(config, aligned);
						setRuntimePreference(aligned);
						window.dispatchEvent(new Event('ai-runtime-changed'));
						if (target.node_id !== globalRuntime.config.node_id || target.workspace_id !== globalRuntime.config.workspace_id) setRuntimeNotice(`系统设置中的桌面桥已离线，已统一切换到活跃桥：${runtimeNodeLabel(target)}`);
					}
				}
			}
			setRuntimeMessage(readyHermes ? 'Hermes 已配置，可用于持仓巡检' : hermesStatus?.message || 'Hermes 状态暂不可读或运行时未配置；可选择在线 Codex 或 Antigravity 桌面桥');
		} catch (cause) {
			setRuntimeMessage(cause instanceof Error ? cause.message : 'AI 运行时状态读取失败');
		} finally {
			setRuntimeChecked(true);
			setRuntimeLoading(false);
		}
	}, [config]);

	useEffect(() => {
		void refreshRuntime();
		const onRuntimeChanged = () => void refreshRuntime();
		window.addEventListener('ai-runtime-changed', onRuntimeChanged);
		return () => window.removeEventListener('ai-runtime-changed', onRuntimeChanged);
	}, [refreshRuntime]);
	useAutoRefresh(() => refreshRuntime(), 20_000, !!config, config);

	const chooseProvider = (provider: InspectionRuntimePreference['provider']) => {
		const candidates = isDesktopChatProvider(provider) ? targetsForDesktopProvider(desktopTargets, provider) : [];
		const target = candidates.find((item) => item.node_id === runtimePreference.node_id && item.workspace_id === runtimePreference.workspace_id) || candidates[0] || null;
		const model = target && isDesktopChatProvider(provider) ? resolveAvailableDesktopModelSelection(target.runtime.models, provider, runtimePreference.model || target.runtime.config.model, runtimePreference.reasoning_effort || target.runtime.config.reasoning_effort) : null;
		void saveRuntimePreference({ provider, node_id: target?.node_id || '', workspace_id: target?.workspace_id || '', model: model?.model || '', reasoning_effort: model?.reasoning_effort || '' });
		setRuntimeNotice('');
	};

	const chooseTarget = (target: AvailableDesktopChatTarget | null) => {
		if (!target || !isDesktopChatProvider(runtimePreference.provider)) return;
		const model = resolveAvailableDesktopModelSelection(target.runtime.models, runtimePreference.provider, runtimePreference.model || target.runtime.config.model, runtimePreference.reasoning_effort || target.runtime.config.reasoning_effort);
		void saveRuntimePreference({ ...runtimePreference, node_id: target.node_id, workspace_id: target.workspace_id, model: model?.model || '', reasoning_effort: model?.reasoning_effort || '' });
	};

	const chooseModel = (group: DesktopModelGroup | undefined) => {
		if (!selectedTarget || !isDesktopChatProvider(runtimePreference.provider) || !group) return;
		const model = resolveAvailableDesktopModelSelection(selectedTarget.runtime.models, runtimePreference.provider, group.id, runtimePreference.reasoning_effort);
		if (model) void saveRuntimePreference({ ...runtimePreference, model: model.model, reasoning_effort: model.reasoning_effort });
	};

	const chooseEffort = (effort: string) => {
		if (!selectedTarget || !isDesktopChatProvider(runtimePreference.provider)) return;
		const model = resolveAvailableDesktopModelSelection(selectedTarget.runtime.models, runtimePreference.provider, runtimePreference.model, effort);
		if (model) void saveRuntimePreference({ ...runtimePreference, model: model.model, reasoning_effort: model.reasoning_effort });
	};

	const loadWorkspace = useCallback(async (signal?: AbortSignal, background = false) => {
		if (!config) return;
		if (!background) setLoading(true);
		setError('');
		try {
			const [directoryPayload, jobsPayload] = await Promise.all([
				requestJSON<{ data: StockDirectoryData }>(config, '/api/v1/stocks/directory', { signal }),
				requestJSON<{ data: PortfolioInspectionJob[] }>(config, '/api/v1/portfolio-inspections?limit=12', { signal }),
			]);
			if (signal?.aborted) return;
			setDirectory(directoryPayload.data.stocks || []);
			cacheDirectory(directoryPayload.data.stocks || []);
			setHistory(jobsPayload.data || []);
			const active = jobsPayload.data?.find((item) => item.status === 'running');
			setJob(current => current ? jobsPayload.data?.find(item => item.id === current.id) || current : active || null);
		} catch (cause) {
			if (signal?.aborted) return;
			setError(cause instanceof Error ? cause.message : '持仓巡检数据加载失败');
		} finally {
			setLoading(false);
		}
	}, [config]);

	useEffect(() => {
		const controller = new AbortController(); void loadWorkspace(controller.signal);
		return () => controller.abort();
	}, [loadWorkspace, refreshKey]);
	useAutoRefresh(signal => loadWorkspace(signal, true), 30_000, !!config, config);

	useEffect(() => {
		writePortfolioDraft(draft);
	}, [draft]);

	useEffect(() => {
		if (!config || !job || job.status !== 'running') return;
		let active = true;
		const poll = async () => {
			try {
				const payload = await requestJSON<{ data: PortfolioInspectionJob }>(config, `/api/v1/portfolio-inspections/${encodeURIComponent(job.id)}`);
				if (!active) return;
				setJob(payload.data);
				setHistory((current) => [payload.data, ...current.filter((item) => item.id !== payload.data.id)].slice(0, 12));
				if (payload.data.status === 'succeeded') setNotice('持仓 AI 巡检已完成，报告已保存在本机');
				if (payload.data.status === 'partial') setNotice('巡检报告已生成，部分个股或组合分析使用降级结果');
			} catch {
				// Reopening this workspace recovers the persisted task state.
			}
		};
		const stopRefresh = startAutoRefresh(() => poll(), 3000);
		return () => { active = false; stopRefresh(); };
	}, [config, job?.id, job?.status]);

	const startInspection = async () => {
		if (!config || !runtimeReady || draft.holdings.length === 0 || totalWeight > 100) return;
		setStarting(true);
		setError('');
		setNotice('');
		try {
			const payload = await requestJSON<{ data: PortfolioInspectionJob }>(config, '/api/v1/portfolio-inspections', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					trader_profile: draft.profile,
					holdings: portfolioDraftToHoldings(draft.holdings),
					ai_provider: runtimePreference.provider,
					...(isDesktopChatProvider(runtimePreference.provider) && resolvedModel && selectedTarget ? {
						model: resolvedModel.model,
						reasoning_effort: resolvedModel.reasoning_effort,
						desktop_node_id: selectedTarget.node_id,
						desktop_workspace_id: selectedTarget.workspace_id,
				} : {}),
				}),
			});
			setJob(payload.data);
			setHistory((current) => [payload.data, ...current.filter((item) => item.id !== payload.data.id)].slice(0, 12));
			setNotice('巡检已在后台开始，离开当前页面不会中断，可以先使用其他功能');
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : '持仓巡检启动失败');
		} finally {
			setStarting(false);
		}
	};

	const deleteInspection = async (item: PortfolioInspectionJob) => {
		if (!config || item.status === 'running' || deletingInspectionID) return;
		if (!window.confirm('确定删除这条巡检记录？删除后不可恢复。')) return;
		setDeletingInspectionID(item.id);
		setError('');
		try {
			await requestJSON(config, `/api/v1/portfolio-inspections/${encodeURIComponent(item.id)}`, { method: 'DELETE' });
			setHistory((current) => current.filter((entry) => entry.id !== item.id));
			if (job?.id === item.id) setJob(null);
			setNotice('巡检记录已删除');
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : '删除巡检记录失败');
		} finally {
			setDeletingInspectionID('');
		}
	};

	return <div className="portfolio-inspection-workspace">
		<aside className="portfolio-history" aria-label="巡检历史">
			<header><History size={16} /><div><strong>巡检记录</strong><small>本机保存</small></div></header>
			<div className="portfolio-history-list">
				{loading && <span className="portfolio-history-empty"><LoaderCircle className="spin" size={17} />正在读取</span>}
				{!loading && history.length === 0 && <span className="portfolio-history-empty">暂无报告</span>}
				{history.map((item) => <div className={`portfolio-history-item ${job?.id === item.id ? 'active' : ''}`} key={item.id}>
					<button type="button" className="portfolio-history-select" onClick={() => setJob(item)}>
						<span>{item.status === 'running' ? <LoaderCircle className="spin" size={14} /> : item.status === 'succeeded' ? <CheckCircle2 size={14} /> : <CircleAlert size={14} />}</span>
						<div><strong>{item.request.holdings.length} 只持仓 · {profileLabel(item.request.trader_profile)}</strong><small>{inspectionProviderLabel(item.request.ai_provider)}{item.request.model ? ` · ${item.request.model} / ${item.request.reasoning_effort || '默认'}` : ''}</small><small>{formatDate(item.updated_at || item.started_at)}</small></div>
						<ChevronRight size={14} />
					</button>
					<button type="button" className="portfolio-history-delete" aria-label="删除巡检记录" title={item.status === 'running' ? '任务运行中' : '删除记录'} disabled={item.status === 'running' || deletingInspectionID === item.id} onClick={() => void deleteInspection(item)}><Trash2 size={13} /></button>
				</div>)}
			</div>
		</aside>

		<section className="portfolio-inspection-main">
			{notice && <div className="portfolio-notice"><CheckCircle2 size={16} /><span>{notice}</span></div>}
			{error && <div className="portfolio-error"><CircleAlert size={16} /><span>{error}</span>{error.includes('模型') && <button type="button" onClick={onOpenSettings}>配置模型</button>}</div>}

			{(!job?.report || job.status === 'running') && <>
				<section className="portfolio-runtime-picker" aria-label="持仓巡检 AI 运行时">
					<header><Activity size={17} /><div><strong>巡检 AI 接入</strong><small>与系统共用运行时、桥节点和模型</small></div>{runtimeLoading && <LoaderCircle className="spin" size={15} />}</header>
					<div className="portfolio-runtime-fields">
						<label><span>运行时</span><select aria-label="持仓巡检运行时" value={runtimePreference.provider} disabled={Boolean(running) || starting || runtimeLoading} onChange={(event) => chooseProvider(event.target.value as InspectionRuntimePreference['provider'])}><option value="hermes">Hermes</option><option value="codex_desktop">桌面 Codex</option><option value="antigravity_desktop">桌面 Antigravity</option></select></label>
						{isDesktopChatProvider(runtimePreference.provider) && <>
							<label><span>桌面桥</span><select aria-label="持仓巡检桌面桥" value={selectedTarget ? `${selectedTarget.node_id}::${selectedTarget.workspace_id}` : ''} disabled={Boolean(running) || starting || runtimeLoading || providerTargets.length === 0} onChange={(event) => chooseTarget(providerTargets.find((target) => `${target.node_id}::${target.workspace_id}` === event.target.value) || null)}><option value="">{providerTargets.length ? '选择在线桌面桥' : '当前没有在线可用桥'}</option>{providerTargets.map((target) => <option value={`${target.node_id}::${target.workspace_id}`} key={`${target.node_id}:${target.workspace_id}`}>{runtimeNodeLabel(target)}</option>)}</select></label>
							<label><span>模型</span><select aria-label="持仓巡检模型" value={selectedModelGroup?.id || ''} disabled={Boolean(running) || starting || runtimeLoading || !selectedTarget || modelGroups.length === 0} onChange={(event) => chooseModel(modelGroups.find((group) => group.id === event.target.value))}><option value="">{selectedTarget ? '选择可用模型' : '等待桌面桥'}</option>{modelGroups.map((group) => <option value={group.id} key={group.id}>{group.name}</option>)}</select></label>
							<label><span>推理强度</span><select aria-label="持仓巡检推理强度" value={resolvedModel?.reasoning_effort || ''} disabled={Boolean(running) || starting || runtimeLoading || !selectedModelGroup} onChange={(event) => chooseEffort(event.target.value)}>{selectedModelGroup?.efforts.map((effort) => <option value={effort} key={effort}>{effort}</option>) || <option value="">无可用档位</option>}</select></label>
						</>}
					</div>
					<div className={`portfolio-runtime-status ${runtimeReady ? 'ready' : 'unavailable'}`} role="status" title={selectedTarget && resolvedModel ? `${runtimeNodeLabel(selectedTarget)} · ${resolvedModel.model} / ${resolvedModel.reasoning_effort}` : undefined}>
						{!runtimeChecked ? '检查运行时…' : runtimePreference.provider === 'hermes' ? runtimeReady ? 'Hermes 已就绪' : runtimeMessage || 'Hermes 未配置' : selectedTarget && resolvedModel ? `系统统一 · ${runtimeNodeLabel(selectedTarget)} · ${resolvedModel.model} / ${resolvedModel.reasoning_effort}` : `${desktopChatProviderLabel(runtimePreference.provider)} 桥不可用 · 暂不能巡检`}
						{runtimePreference.provider === 'hermes' && !runtimeReady && <button type="button" onClick={onOpenSettings}>打开系统设置</button>}
					</div>
					{runtimeNotice && <div className="portfolio-runtime-notice"><CheckCircle2 size={14} /><span>{runtimeNotice}</span></div>}
				</section>
				<PortfolioSetupForm draft={draft} directory={directory} disabled={Boolean(running)} submitDisabled={!runtimeChecked || !runtimeReady || runtimeLoading} busy={starting || Boolean(running) || runtimeLoading} actionLabel="开始 AI 巡检" busyLabel={running ? '巡检进行中' : '正在启动'} onChange={setDraft} onSubmit={() => void startInspection()} />
			</>}

			{job?.status === 'running' && <InspectionProgress job={job} />}
			{job?.report && job.status !== 'running' && <PortfolioReportView report={job.report} status={job.status} onNew={() => setJob(null)} onOpenStockAnalysis={onOpenStockAnalysis} />}
		</section>
	</div>;
}

function InspectionProgress({ job }: { job: PortfolioInspectionJob }) {
	const progress = job.total_stocks > 0 ? Math.round(job.completed_stocks / job.total_stocks * (job.stage === 'aggregating' ? 85 : 75)) : 3;
	const displayProgress = job.stage === 'aggregating' ? Math.max(86, progress) : Math.max(5, progress);
	return <section className="portfolio-progress" role="status" aria-live="polite">
		<div className="portfolio-progress-icon"><RefreshCw className="spin" size={24} /></div>
		<div><span>后台任务 · {stageLabel(job.stage)}</span><strong>巡检耗时较长，可以先使用其他功能</strong><p>{job.message}</p><div className="portfolio-progress-bar"><i style={{ width: `${displayProgress}%` }} /><small>{job.completed_stocks}/{job.total_stocks} 只 · {displayProgress}%</small></div>{job.current_symbols?.length > 0 && <em>正在分析 {job.current_symbols.join('、')}</em>}</div>
		<small><Clock3 size={14} />离开页面不会中断</small>
	</section>;
}

function PortfolioReportView({ report, status, onNew, onOpenStockAnalysis }: { report: PortfolioInspectionReport; status: string; onNew: () => void; onOpenStockAnalysis: (analysis: StockAIAnalysis) => void }) {
	const { conclusion, metrics } = report;
	const isV2 = report.algorithm_version === 'portfolio-health-v2';
	const names = new Map(report.holdings.map((item) => [item.holding.symbol, item.analysis?.name || item.holding.name || item.holding.symbol]));
	const analyses = new Map<string, StockAIAnalysis>();
	report.holdings.forEach((item) => {
		if (!item.analysis || item.status !== 'succeeded') return;
		analyses.set(item.holding.symbol, item.analysis);
		analyses.set(item.analysis.symbol, item.analysis);
		analyses.set(item.holding.symbol.split('.')[0], item.analysis);
		analyses.set(item.analysis.symbol.split('.')[0], item.analysis);
	});
	return <div className="portfolio-report">
		<header className="portfolio-report-header"><div><span><WalletCards size={15} />持仓 AI 巡检报告</span><h2>{report.profile.label}型组合 · {conclusion.risk_level}风险</h2><p>{formatDate(report.generated_at)} · 覆盖 {metrics.coverage_percent}% · {conclusion.source === 'hermes-ai' ? 'AI 综合研判' : '本地规则研判'}</p></div><button type="button" onClick={onNew}><Plus size={15} />新建巡检</button></header>
		{status === 'partial' && <div className="portfolio-report-warning"><CircleAlert size={15} />报告已生成，但部分个股或组合分析使用降级结果。</div>}
		{metrics.stop_loss_coverage_percent !== undefined && metrics.stop_loss_coverage_percent < 100 && <div className="portfolio-report-warning"><CircleAlert size={15} />有效静态止损仅覆盖 {metrics.stop_loss_coverage_percent}% 的持仓；其余风险未估算，不能视为零风险。</div>}
		{isV2 ? <section className="portfolio-report-overview">
			<div className="portfolio-health-score"><span>组合健康度</span><strong>{metrics.health_score_available ? conclusion.health_score : '—'}</strong><small>{metrics.health_score_available ? 'V2 确定性评分' : '覆盖不足，暂不评分'}</small></div>
			<div><span>个股质量</span><strong>{metrics.weighted_stock_score.toFixed(1)}</strong><small>健康度权重 45%</small></div>
			<div><span>风险韧性</span><strong>{(metrics.stop_loss_coverage_percent ?? 100) < 70 ? '待补证' : metrics.risk_resilience_score}</strong><small>权重 25% · {(metrics.stop_loss_coverage_percent ?? 100) < 100 ? `止损覆盖 ${metrics.stop_loss_coverage_percent}%` : `止损风险 ${metrics.stop_loss_risk_percent.toFixed(2)}%`}</small></div>
			<div><span>分散 / 风格</span><strong>{metrics.diversification_score} / {metrics.style_match_score}</strong><small>{conclusion.style_match} · 持仓 {metrics.total_position_percent}% / 现金 {metrics.cash_percent}%</small></div>
		</section> : <section className="portfolio-report-overview">
			<div className="portfolio-health-score"><span>组合健康度</span><strong>{conclusion.health_score}</strong><small>历史评分</small></div>
			<div><span>风格匹配</span><strong>{conclusion.style_match}</strong><small>{metrics.style_match_score} 分</small></div>
			<div><span>持仓 / 现金</span><strong>{metrics.total_position_percent}% / {metrics.cash_percent}%</strong><small>当前配置</small></div>
			<div><span>预估止损风险</span><strong>{(metrics.stop_loss_coverage_percent ?? 100) < 100 ? '覆盖不完整' : `${metrics.stop_loss_risk_percent.toFixed(2)}%`}</strong><small>占组合资产</small></div>
		</section>}
		<section className="portfolio-executive"><h3>巡检结论</h3><p>{conclusion.executive_summary}</p></section>

		<div className="portfolio-report-grid">
			<section><header><CircleAlert size={16} /><strong>主要风险</strong></header><ListItems items={conclusion.primary_risks} empty="没有识别到突出风险" /></section>
			<section><header><Activity size={16} /><strong>集中与联动</strong></header><ListItems items={conclusion.concentration_findings} empty="没有识别到明显集中风险" /></section>
		</div>

		<section className="portfolio-holding-report"><header><strong>逐股巡检</strong><small>按组合风险贡献排序</small></header><div>
			{conclusion.holdings.map((item) => {
				const stockAnalysis = analyses.get(item.symbol) || analyses.get(item.symbol.split('.')[0]);
				return <article key={item.symbol}><header><div><strong>{names.get(item.symbol) || item.symbol}</strong><span>{item.symbol} · {item.portfolio_role}</span></div><em className={priorityTone(item.action_priority)}>{item.action_priority}</em><b>{item.risk_contribution.toFixed(1)}% 风险贡献</b>{stockAnalysis && <button type="button" className="portfolio-open-stock" onClick={() => onOpenStockAnalysis(stockAnalysis)} aria-label={`查看${stockAnalysis.name}个股分析报告`} title="查看已生成的个股分析报告"><ExternalLink size={14} />查看个股报告</button>}</header><p>{item.conclusion}</p><div><span><b>动作</b>{item.action}</span><span><b>确认</b>{item.confirmation}</span><span><b>失效</b>{item.invalidation}</span></div></article>;
			})}
		</div></section>

		<div className="portfolio-report-grid">
			<section><header><ChevronRight size={16} /><strong>处理顺序</strong></header><ListItems items={conclusion.adjustment_order} empty="暂无调整事项" ordered /></section>
			<section><header><ShieldCheck size={16} /><strong>下次检查</strong></header><ListItems items={conclusion.next_checklist} empty="暂无检查事项" /></section>
		</div>
		<section className="portfolio-scenarios"><header><strong>组合情景</strong></header><div>{conclusion.scenarios.map((scenario) => <article key={scenario.name}><strong>{scenario.name}</strong><span>{scenario.condition}</span><p>{scenario.portfolio_action}</p></article>)}</div></section>
		{conclusion.data_limitations.length > 0 && <section className="portfolio-limitations"><strong>数据限制</strong><ListItems items={conclusion.data_limitations} empty="" /></section>}
		<footer>仅用于信息整理、研究与复盘，不构成投资建议或交易指令。</footer>
	</div>;
}

function ListItems({ items, empty, ordered = false }: { items: string[]; empty: string; ordered?: boolean }) {
	const Tag = ordered ? 'ol' : 'ul';
	return items.length ? <Tag>{items.map((item, index) => <li key={`${index}-${item}`}>{item}</li>)}</Tag> : <p className="portfolio-list-empty">{empty}</p>;
}

function loadCachedDirectory(): StockDirectoryEntry[] {
	try {
		const parsed = JSON.parse(window.localStorage.getItem(directoryStorageKey) || '{}');
		return Array.isArray(parsed.stocks) ? parsed.stocks : [];
	} catch {
		return [];
	}
}

function cacheDirectory(stocks: StockDirectoryEntry[]) {
	try { window.localStorage.setItem(directoryStorageKey, JSON.stringify({ cachedAt: Date.now(), stocks })); } catch { /* in-memory directory remains available */ }
}

function stageLabel(stage: string) {
	if (stage === 'queued') return '排队准备';
	if (stage === 'analyzing_stocks') return '逐股分析';
	if (stage === 'aggregating') return '组合研判';
	return '处理中';
}

function profileLabel(profile: PortfolioTraderProfile) {
	return portfolioProfiles.find((item) => item.id === profile)?.label || '均衡';
}

function formatDate(value?: string) {
	if (!value) return '刚刚';
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

function priorityTone(value: string) {
	if (value === '优先处理') return 'danger';
	if (value === '保持') return 'positive';
	return 'neutral';
}
