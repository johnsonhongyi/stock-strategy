import { linkageContextTTL, linkageOperationRefreshDelay } from "../lib/linkage";
import { useEffect, useMemo, useState } from 'react';
import { BackendRequestError, type BackendConfig } from '../lib/backend';
import {
	canReplayWatchlistAddCommand, createLinkageAttentionIntent, createWatchlistAddCommand, getLatestLinkageEvents, getLinkageAttention, getLinkageContext, getLinkageOperation, isLinkageOperationSettled as isSettled, listLinkageTargets, normalizeLinkageStockSymbol,
	isLinkageAttentionIntentSatisfied, SBC_WATCHLIST_SOURCE,
	submitLinkageAttentionIntent, submitWatchlistAdd, type LinkageAttentionIntent, type LinkageAttentionSnapshot, type LinkageContextEvent, type LinkageContextSnapshot, type LinkageOperation, type LinkageTarget, type WatchlistAddCommand,
} from '../lib/linkage';

type Props = {
	config: BackendConfig | null | undefined;
	symbol: string;
	tradeDate?: string;
};

function consumerStatus(operation: LinkageOperation | null, consumer: 'TK' | 'ATS') {
	for (const delivery of operation?.deliveries || []) {
		const status = delivery.result?.result?.consumers?.[consumer];
		if (status) return `${status.status || '待同步'} / 行情 ${status.quote_status || '未知'}`;
	}
	return '等待桌面回执';
}

function savedStatus(operation: LinkageOperation | null) {
	const statuses = (operation?.deliveries || []).map((delivery) =>
		delivery.result?.result?.saved?.status || delivery.result?.status || delivery.status,
	);
	if (!statuses.length) return '等待桌面保存';
	return statuses.map((status) => status === 'already_present' ? '已在关注' : status === 'applied' ? '已保存' : status).join('，');
}

function storageKey(symbol: string) { return `watchlist-linkage:${symbol}`; }
function attentionStorageKey(workspaceID: string, nodeID: string, symbol: string, source: string, action: string) {
	return `watchlist-attention:${workspaceID}:${nodeID}:${symbol}:${source}:${action}`;
}

function attentionSourceLabel(source: string) {
	if (source === 'manual') return '人工';
	if (source === SBC_WATCHLIST_SOURCE) return 'SBC 网页';
	return source;
}

export function WatchlistLinkageButton({ config, symbol, tradeDate }: Props) {
	const normalizedSymbol = useMemo(() => normalizeLinkageStockSymbol(symbol), [symbol]);
	const [targets, setTargets] = useState<LinkageTarget[]>([]);
	const [selectedNode, setSelectedNode] = useState('');
	const selectedTarget = targets.find((item) => item.node_id === selectedNode);
	const [command, setCommand] = useState<WatchlistAddCommand | null>(null);
	const [operation, setOperation] = useState<LinkageOperation | null>(null);
	const [attention, setAttention] = useState<LinkageAttentionSnapshot | null>(null);
	const [pendingAttention, setPendingAttention] = useState<LinkageAttentionIntent | null>(null);
	const [context, setContext] = useState<LinkageContextSnapshot | null>(null);
	const [contextExpired, setContextExpired] = useState(true);
	const [events, setEvents] = useState<LinkageContextEvent[]>([]);
	const [error, setError] = useState('');
	const [attentionError, setAttentionError] = useState('');
	const [attentionMessage, setAttentionMessage] = useState('');
	const [busy, setBusy] = useState(false);
	const [attentionBusy, setAttentionBusy] = useState(false);
	const currentAttention = selectedTarget && attention?.workspace_id === selectedTarget.workspace_id
		&& attention.node_id === selectedTarget.node_id ? attention : null;

	useEffect(() => {
		let active = true;
		if (!config) return () => { active = false; };
		listLinkageTargets(config).then((response) => {
			if (!active) return;
			const items = response.targets || [];
			setTargets(items);
			setSelectedNode((current) => {
				if (items.some((item) => item.node_id === current)) return current;
				const saved = localStorage.getItem('watchlist-linkage:selected-node');
				if (saved && items.some((item) => item.node_id === saved)) return saved;
				return items.length === 1 ? items[0].node_id : '';
			});
		}).catch((loadError) => {
			if (active) setError(loadError instanceof Error ? loadError.message : '无法读取桌面列表');
		});
		return () => { active = false; };
	}, [config]);

	useEffect(() => {
		setCommand(null);
		setOperation(null);
		setError('');
		if (!normalizedSymbol) return;
		try {
			const saved = sessionStorage.getItem(storageKey(normalizedSymbol));
			if (saved) setCommand(JSON.parse(saved) as WatchlistAddCommand);
		} catch {
			setError('无法读取待处理的关注请求');
		}
	}, [normalizedSymbol]);

	useEffect(() => {
		setAttention(null);
		setAttentionError('');
		setAttentionMessage('');
		if (!config || !selectedTarget) return;
		let active = true;
		let timer: number | undefined;
		const refresh = async () => {
			try {
				const snapshot = await getLinkageAttention(config, selectedTarget.workspace_id, selectedTarget.node_id);
				if (active) { setAttention(snapshot); setAttentionError(''); }
			} catch (loadError) {
				if (active) setAttentionError(loadError instanceof Error ? loadError.message : '无法读取工作区关注来源');
			}
			if (active) timer = window.setTimeout(() => { void refresh(); }, 15_000);
		};
		void refresh();
		return () => { active = false; if (timer !== undefined) window.clearTimeout(timer); };
	}, [config, selectedTarget?.workspace_id, selectedTarget?.node_id]);

	useEffect(() => {
		setContext(null);
		setEvents([]);
		setContextExpired(true);
		if (!config) return;
		let active = true;
		let timer: number | undefined;
		let expiryTimer: number | undefined;
		const refresh = async () => {
			const [contextResult, eventsResult] = await Promise.allSettled([
				getLinkageContext(config, selectedTarget?.workspace_id), getLatestLinkageEvents(config, 12, selectedTarget?.workspace_id),
			]);
			if (active && contextResult.status === 'fulfilled') {
				const snapshot = contextResult.value;
				setContext(snapshot);
				const ttl = linkageContextTTL(snapshot);
				if (expiryTimer !== undefined) window.clearTimeout(expiryTimer);
				setContextExpired(ttl <= 0);
				if (ttl > 0) expiryTimer = window.setTimeout(() => { if (active) setContextExpired(true); }, ttl);
			} else if (active) {
				setContextExpired(true);
			}
			if (active && eventsResult.status === 'fulfilled') setEvents(eventsResult.value.events || []);
			if (active) timer = window.setTimeout(() => { void refresh(); }, 30_000);
		};
		void refresh();
		return () => {
			active = false;
			if (timer !== undefined) window.clearTimeout(timer);
			if (expiryTimer !== undefined) window.clearTimeout(expiryTimer);
		};
	}, [config, selectedTarget?.workspace_id]);

	useEffect(() => {
		if (!config || !command) return;
		let active = true;
		let timer: number | undefined;
		let delay = 2000;
		const poll = async () => {
			try {
				const current = await getLinkageOperation(config, command.operation_id);
				if (active) setOperation(current);
				delay = linkageOperationRefreshDelay(current);
			} catch { delay = Math.min(Math.max(delay, 2000) * 2, 30_000); }
			if (active && delay > 0) timer = window.setTimeout(() => { void poll(); }, delay);
		};
		void poll();
		return () => { active = false; if (timer !== undefined) window.clearTimeout(timer); };
	}, [config, command]);

	const submit = async () => {
		if (!config || !normalizedSymbol || !selectedNode || busy) return;
		const target = targets.find((item) => item.node_id === selectedNode);
		if (!target) return;
		setBusy(true);
		setError('');
		try {
			let next = command;
			if (next && !isSettled(operation)) {
				try { setOperation(await getLinkageOperation(config, next.operation_id)); return; }
				catch (lookupError) {
					if (!(lookupError instanceof BackendRequestError) || lookupError.status !== 404) throw lookupError;
					if (!canReplayWatchlistAddCommand(next)) next = null;
				}
			}
			if (!next || isSettled(operation)) {
				next = createWatchlistAddCommand({
					workspaceID: target.workspace_id, targetNodeID: target.node_id, symbol: normalizedSymbol,
					source: SBC_WATCHLIST_SOURCE, reason: 'SBC K 线图重点关注',
					contextRefs: [`sbc-chart:${normalizedSymbol}${tradeDate ? `:${tradeDate}` : ''}`], observedTradeDate: tradeDate,
				});
				setCommand(next);
				setOperation(null);
				sessionStorage.setItem(storageKey(normalizedSymbol), JSON.stringify(next));
			}
			setOperation((await submitWatchlistAdd(config, next)).operation);
		} catch (submitError) {
			setError(submitError instanceof Error ? submitError.message : '关注请求提交失败，可使用相同请求重试');
		} finally { setBusy(false); }
	};

	const updateWorkspaceSource = async (source: string, action: LinkageAttentionIntent['action']) => {
		if (!config || !selectedTarget || !normalizedSymbol || !currentAttention || attentionBusy) return;
		const storageKeyForIntent = attentionStorageKey(selectedTarget.workspace_id, selectedTarget.node_id, normalizedSymbol, source, action);
		setAttentionBusy(true);
		setAttentionError('');
		setAttentionMessage('');
		try {
			let intent: LinkageAttentionIntent | null = pendingAttention;
			if (intent && (intent.workspace_id !== selectedTarget.workspace_id || intent.node_id !== selectedTarget.node_id
				|| intent.symbol !== normalizedSymbol || intent.source !== source || intent.action !== action)) intent = null;
			try {
				const saved = sessionStorage.getItem(storageKeyForIntent);
				if (saved) {
					const pending = JSON.parse(saved) as LinkageAttentionIntent;
					if (pending.workspace_id === selectedTarget.workspace_id && pending.node_id === selectedTarget.node_id
						&& pending.symbol === normalizedSymbol && pending.source === source
						&& pending.action === action && Number.isInteger(pending.base_revision)
						&& /^[A-Za-z0-9_-]{1,128}$/.test(pending.intent_id)) intent = pending;
				}
			} catch { try { sessionStorage.removeItem(storageKeyForIntent); } catch { /* Keep in-memory retries available. */ } }
			intent ||= createLinkageAttentionIntent({
				workspace_id: selectedTarget.workspace_id, node_id: selectedTarget.node_id,
				symbol: normalizedSymbol, source, action, base_revision: currentAttention.revision,
			});
			setPendingAttention(intent);
			try { sessionStorage.setItem(storageKeyForIntent, JSON.stringify(intent)); } catch { /* Keep in-memory retries available. */ }
			try {
				const result = await submitLinkageAttentionIntent(config, intent);
				const latest = await getLinkageAttention(config, selectedTarget.workspace_id, selectedTarget.node_id);
				try { sessionStorage.removeItem(storageKeyForIntent); } catch { /* The applied intent remains idempotent. */ }
				setPendingAttention(null);
				setAttention(latest);
				const actionLabel = action === 'add' ? `已加入 ${attentionSourceLabel(source)} 来源`
					: action === 'remove_all' ? '已清除全部工作区来源' : `已取消 ${attentionSourceLabel(source)} 来源`;
				setAttentionMessage(isLinkageAttentionIntentSatisfied(latest, normalizedSymbol, source, action)
					? `${actionLabel} · 修订 ${result.revision}；桌面同步中`
					: `操作已登记，但工作区已有更新状态；以当前来源列表为准 · 修订 ${latest.revision}`);
			} catch (requestError) {
				if (requestError instanceof BackendRequestError && requestError.status === 409) {
					const latest = await getLinkageAttention(config, selectedTarget.workspace_id, selectedTarget.node_id);
					setAttention(latest);
					try { sessionStorage.removeItem(storageKeyForIntent); } catch { /* Ignore unavailable browser storage. */ }
					setPendingAttention(null);
					setAttentionMessage(isLinkageAttentionIntentSatisfied(latest, normalizedSymbol, source, action)
						? '工作区来源已是目标状态。'
						: '工作区状态刚被更新，请确认当前状态后再次操作。');
					return;
				}
				throw requestError;
			}
		} catch (requestError) {
			setAttentionError(`${requestError instanceof Error ? requestError.message : '关注来源更新失败'}；重试会复用同一请求`);
		} finally { setAttentionBusy(false); }
	};

	const clearWorkspaceSources = () => {
		if (!window.confirm(`从整个工作区取消 ${normalizedSymbol} 的全部关注来源？这会同步到已连接桌面。`)) return;
		void updateWorkspaceSource(SBC_WATCHLIST_SOURCE, 'remove_all');
	};

	const themes = [...new Set((context?.themes || []).filter((theme) =>
		theme.members.some((member) => member.symbol === normalizedSymbol),
	).map((theme) => theme.name).filter(Boolean))].slice(0, 3);
	const themeMetrics = (context?.themes || []).filter((theme) =>
		theme.members.some((member) => member.symbol === normalizedSymbol),
	).slice(0, 2).map((theme) => {
		const metrics = [
			theme.change_percent !== undefined ? `涨幅 ${theme.change_percent.toFixed(2)}%` : '',
			theme.trend_score !== undefined ? `趋势 ${theme.trend_score}` : '',
			theme.daily_strength_score !== undefined ? `日强 ${theme.daily_strength_score}` : '',
			theme.five_day_strength_score !== undefined ? `五日 ${theme.five_day_strength_score}` : '',
		].filter(Boolean).join(' · ');
		return `${theme.name}${metrics ? ` ${metrics}` : ''}`;
	});
	const ranks = context?.heat.stocks.find((stock) => stock.symbol === normalizedSymbol)?.ranks || {};
	const rankSummary = Object.entries(ranks).sort((left, right) => left[1] - right[1])
		.slice(0, 3).map(([source, rank]) => `${source} #${rank}`).join(' · ');
	const ladder = context?.ladder?.current.levels.flatMap((level) => level.stocks
		.filter((stock) => stock.symbol === normalizedSymbol).map((stock) => ({ ...stock, level })))[0];
	const coverage = context?.coverage?.slice(0, 2).map((item) =>
		`${item.scope} ${item.complete ? '完整' : `${item.observed_count}/${item.total_expected || '样本'}`}`,
	).join(' · ');
	const unavailableSources = context?.sources?.filter((source) => source.status !== 'available')
		.map((source) => `${source.name} ${source.status}`).slice(0, 2).join(' · ');
	const leaderEvidence = context?.evidence?.find((item) => item.source_id === 'ats.leader');
	const leaderCandidate = leaderEvidence?.payload.roles?.find((item) => item.symbol === normalizedSymbol);
	const leaderIsStale = Boolean(leaderEvidence && (
		contextExpired || leaderEvidence.freshness !== 'fresh' || leaderEvidence.payload.source_freshness === 'stale'
		|| leaderEvidence.trade_date !== context?.trade_date
	));
	const leaderSummary = leaderEvidence
		? `ATS ${leaderIsStale ? '历史' : ''}角色候选 · ${leaderCandidate?.role || '当前样本未列入'} · ${leaderEvidence.trade_date}`
		: '';
	const auction = context?.evidence?.find((item) => item.source_id === 'tk.auction' && item.trade_date === context.trade_date);
	const auctionCodes = auction?.payload.candidate_codes || [];
	const auctionCandidate = Boolean(auction && auctionCodes.includes(normalizedSymbol));
	const auctionSummary = auction ? `竞价 ${auction.payload.state || '已记录'} · ${auctionCandidate ? '候选命中' : auction.payload.candidate_codes_complete ? '未入选' : '样本未命中'} · ${auction.payload.candidate_count ?? '?'} 只 · ${contextExpired || auction.freshness !== 'fresh' ? '历史' : '盘中'}` : '';
	const instockEvidence = context?.evidence?.find((item) => item.source_id === 'instock.candidates');
	const instockCandidate = instockEvidence?.payload.candidates?.find((item) => item.symbol === normalizedSymbol);
	const instockCandidateListed = Boolean(instockCandidate || instockEvidence?.payload.candidate_codes?.includes(normalizedSymbol));
	const instockIsStale = Boolean(instockEvidence && (
		contextExpired || instockEvidence.freshness === 'stale' || instockEvidence.payload.source_freshness === 'stale'
		|| instockEvidence.trade_date !== context?.trade_date
	));
	const instockIsFresh = Boolean(instockEvidence && !instockIsStale && instockEvidence.freshness === 'fresh');
	const completeValue = instockEvidence?.payload.candidate_codes_complete;
	const instockComplete = completeValue === true
		|| (typeof completeValue !== 'boolean' && instockEvidence?.payload.coverage?.complete === true);
	const instockLabel = instockCandidateListed
		? (instockIsStale ? '历史候选' : instockIsFresh ? '当前候选' : '候选状态待确认')
		: instockIsStale
			? (instockComplete ? '历史 selector 未列入' : '历史样本未列入')
			: instockIsFresh
				? (instockComplete ? '当前 selector 未列入' : '当前样本未列入')
				: '候选状态待确认';
	const instockSummary = instockEvidence
		? `Instock ${instockLabel}${instockCandidate?.grade ? ` · ${instockCandidate.grade}级` : ''}${typeof instockCandidate?.score === 'number' ? ` · ${instockCandidate.score}分` : ''} · ${instockEvidence.payload.candidate_count ?? '?'} 只 · ${instockEvidence.trade_date}`
		: '';
	const eventSummary = events.filter((event) => {
		const payload = event.payload || {};
		return payload.symbol === normalizedSymbol || payload.theme_id && (context?.themes || []).some((theme) =>
			theme.theme_id === payload.theme_id && theme.members.some((member) => member.symbol === normalizedSymbol),
		) || Array.isArray(payload.candidate_codes) && payload.candidate_codes.includes(normalizedSymbol);
	}).slice(-3).map((event) => {
		const signalType = typeof event.payload.signal_type === 'string' ? ` ${event.payload.signal_type}` : '';
		return `${event.event_type}${signalType} · ${new Date(event.occurred_at).toLocaleTimeString()}`;
	});
	const activeAttentionSources = currentAttention?.sources.filter((item) => item.symbol === normalizedSymbol && item.active) || [];
	const sbcSourceActive = activeAttentionSources.some((item) => item.source === SBC_WATCHLIST_SOURCE);
	const attentionSourceSummary = currentAttention
		? activeAttentionSources.map((item) => attentionSourceLabel(item.source)).join('、') || '无活动来源'
		: attentionError ? '状态不可用' : '读取中';

	return <section className="watchlist-linkage" aria-label="桌面重点关注联动">
		<div className="watchlist-linkage-context" aria-live="polite">
			<strong>共享证据</strong>
			<span>{context ? `${context.trade_date || '日期未知'} · ${contextExpired ? '过期/断线' : context.freshness}` : '等待统一快照'}</span>
			<span>题材：{themes.join('、') || '暂无可确认归属'}</span>
			{themeMetrics.map((summary) => <span key={summary}>题材强度：{summary}</span>)}
			<span>人气：{rankSummary || '不在当前 Top 100 观测范围'}</span>
			<span>天梯：{ladder ? `${ladder.level.label} · ${ladder.streak} 连板` : '当前快照未列入'}</span>
			{leaderSummary && <span>{leaderSummary}</span>}
			{auctionSummary && <span>竞价证据：{auctionSummary}</span>}
			{instockSummary && <span>{instockSummary}</span>}
			{eventSummary.map((summary, index) => <span key={`${summary}-${index}`}>异动：{summary}</span>)}
			{coverage && <span>覆盖：{coverage}</span>}
			{unavailableSources && <span>来源状态：{unavailableSources}</span>}
		</div>
		<div className="watchlist-linkage-controls">
			<label><span>桌面</span><select value={selectedNode} onChange={(event) => { setSelectedNode(event.target.value); localStorage.setItem('watchlist-linkage:selected-node', event.target.value); }} disabled={busy || attentionBusy || targets.length === 0 || Boolean(command && !isSettled(operation))}>
				<option value="">{targets.length ? '选择桌面' : '暂无已配置桌面'}</option>
				{targets.map((target) => <option key={target.node_id} value={target.node_id}>{target.label || target.node_id} · {target.status}</option>)}
			</select></label>
			<button type="button" onClick={() => void submit()} disabled={!config || !normalizedSymbol || !selectedNode || busy}>
				{busy ? '提交中…' : command && !isSettled(operation) ? '重试 / 刷新状态' : '添加到重点关注'}
			</button>
		</div>
		<div className="watchlist-linkage-attention" aria-live="polite">
			<span>工作区来源：{attentionSourceSummary}</span>
			<div className="watchlist-linkage-attention-actions">
			<button type="button" onClick={() => void updateWorkspaceSource(SBC_WATCHLIST_SOURCE, sbcSourceActive ? 'remove_source' : 'add')}
				disabled={!config || !selectedTarget || !normalizedSymbol || !currentAttention || attentionBusy}>
				{attentionBusy ? '更新中…' : sbcSourceActive ? '取消 SBC 网页来源' : '添加 SBC 网页来源'}
			</button>
			{activeAttentionSources.filter((item) => item.source !== SBC_WATCHLIST_SOURCE).map((item) =>
				<button key={item.source} type="button" onClick={() => void updateWorkspaceSource(item.source, 'remove_source')}
					disabled={attentionBusy}>取消 {attentionSourceLabel(item.source)} 来源</button>,
			)}
			{activeAttentionSources.length > 0 && <button className="danger" type="button" onClick={clearWorkspaceSources}
				disabled={attentionBusy}>清除全部来源</button>}
			</div>
		</div>
		{attentionMessage && <small className="watchlist-linkage-attention-message">{attentionMessage}</small>}
		{attentionError && <small className="watchlist-linkage-error">{attentionError}</small>}
		{command && <div className="watchlist-linkage-status" aria-live="polite">
			<span>本地保存：{savedStatus(operation)}</span><span>TK：{consumerStatus(operation, 'TK')}</span><span>ATS：{consumerStatus(operation, 'ATS')}</span>
		</div>}
		{error && <small className="watchlist-linkage-error">{error}</small>}
		{!normalizedSymbol && <small className="watchlist-linkage-error">当前标的不是受支持的沪深 A 股代码。</small>}
	</section>;
}
