import { Activity, AlertTriangle, CheckCircle2, Clock3, Database, LoaderCircle, RefreshCw, Server, TimerReset } from 'lucide-react';
import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { BackendConfig, requestJSON } from '../lib/backend';
import './system-status.css';

type MarketStatus = {
	market: string;
	rows: number;
	latest_date?: string;
	expected_date?: string;
	invalid_rows: number;
	state: string;
	error?: string;
};

type ScheduledJob = {
	id: string;
	name: string;
	market: string;
	schedule: string;
	timezone: string;
	next_run_at?: string;
	last_status: string;
	last_message?: string;
	last_error?: string;
	last_started_at?: string;
	last_finished_at?: string;
	exit_code?: number;
	log_path: string;
	recent_log?: string[];
};

type SystemStatus = {
	generated_at: string;
	api_state: string;
	api_started_at: string;
	api_uptime_seconds: number;
	database_state: string;
	database_error?: string;
	live_market: {
		running: boolean;
		state: string;
		session_active: boolean;
		started_at?: string;
		last_check_at?: string;
		last_run_at?: string;
		next_run_at?: string;
		interval_minutes: number;
		interval_reason?: string;
		memory_percent: number;
		cpu_percent: number;
		cpu_known: boolean;
		last_run_status?: string;
		theme?: string;
		signal?: string;
		last_error?: string;
		updated_at: string;
	};
	market_data: MarketStatus[];
	scheduled_jobs: ScheduledJob[];
	schedule_read_error?: string;
};

const liveStateLabels: Record<string, string> = {
	starting: '启动中',
	'waiting-session': '等待交易时段',
	'calendar-error': '交易日历异常',
	scheduled: '盘中等待下一次刷新',
	running: '正在更新信号',
	ok: '最近一次更新成功',
	partial: '最近一次更新不完整',
	'rate-limited': '上游限流，已降频',
	stopped: '调度已停止',
	unavailable: '状态不可用',
};

const intervalReasonLabels: Record<string, string> = {
	'outside-trading-session': '非交易时段暂停刷新',
	'resources-normal': '资源状态正常',
	'resource-pressure-medium': '资源压力偏高，降低刷新频率',
	'resource-pressure-medium-high': '资源压力较高，进一步降低频率',
	'resource-pressure-high': '资源压力高，按最低频率运行',
	'upstream-rate-limit-cooldown': '上游限流冷却中',
	'upstream-or-signal-errors': '上游或信号异常，自动降低请求频率',
	'trading-calendar-unavailable': '交易日历暂不可用',
};

function formatTime(value?: string) {
	if (!value) return '—';
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
}

function duration(seconds: number) {
	if (!Number.isFinite(seconds) || seconds < 0) return '—';
	const days = Math.floor(seconds / 86400);
	const hours = Math.floor((seconds % 86400) / 3600);
	const minutes = Math.floor((seconds % 3600) / 60);
	return days ? `${days} 天 ${hours} 小时` : hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`;
}

function stateTone(value: string) {
	if (/正常|新鲜|成功|运行中|已跳过|已完成/.test(value)) return 'ok';
	if (/异常|失败|缺失|待回补|中断|不完整|限流/.test(value)) return 'warn';
	return 'muted';
}

export function SystemStatusWorkspace({ config, refreshKey }: { config: BackendConfig | null; refreshKey: number }) {
	const [status, setStatus] = useState<SystemStatus | null>(null);
	const [loadState, setLoadState] = useState<'loading' | 'ready' | 'error'>('loading');
	const [loadError, setLoadError] = useState('');
	const [lastUpdated, setLastUpdated] = useState('');
	const [manualRefreshKey, setManualRefreshKey] = useState(0);

	useEffect(() => {
		let active = true;
		let running = false;
		const controller = new AbortController();
		const load = async () => {
			if (!config || running) return;
			running = true;
			try {
				const payload = await requestJSON<SystemStatus>(config, '/api/v1/system/status', { signal: controller.signal });
				if (!active) return;
				setStatus(payload);
				setLoadState('ready');
				setLoadError('');
				setLastUpdated(payload.generated_at);
			} catch (error) {
				if (!active || controller.signal.aborted) return;
				setLoadState('error');
				setLoadError(error instanceof Error ? error.message : '状态接口请求失败');
			} finally {
				running = false;
			}
		};
		if (!config) {
			setLoadState('error');
			setLoadError('后端连接配置尚未就绪');
			return () => { active = false; controller.abort(); };
		}
		void load();
		const timer = window.setInterval(() => { void load(); }, 60_000);
		return () => {
			active = false;
			controller.abort();
			window.clearInterval(timer);
		};
	}, [config, refreshKey, manualRefreshKey]);

	const live = status?.live_market;
	return (
		<section className="system-status-workspace" aria-label="系统运行状态">
			<header className="system-status-hero">
				<div>
					<span className="workspace-kicker">RUNTIME OPERATIONS</span>
					<h2>后台运行状态</h2>
					<p>查看盘中信号调度、行情库覆盖、盘后回补计划和任务日志。状态每分钟自动刷新。</p>
				</div>
				<div className="system-status-hero-actions">
					{lastUpdated && <small>最近读取：{formatTime(lastUpdated)}</small>}
					<button type="button" onClick={() => setManualRefreshKey((value) => value + 1)} aria-label="刷新状态"><RefreshCw size={16} />刷新</button>
				</div>
			</header>
			{loadError && <div className="system-status-alert"><AlertTriangle size={16} /><span>状态读取失败：{loadError}{status ? '；以下显示最近一次成功读取的数据' : ''}</span></div>}
			{!status && loadState === 'loading' && <div className="system-status-loading"><LoaderCircle className="system-status-spin" size={18} />正在读取后端状态…</div>}
			{status && <>
				<section className="system-status-overview">
					<StatusCard icon={<Server size={17} />} label="API 服务" value={status.api_state} detail={`运行 ${duration(status.api_uptime_seconds)} · 启动 ${formatTime(status.api_started_at)}`} tone={stateTone(status.api_state)} />
					<StatusCard icon={<Database size={17} />} label="统一行情数据库" value={status.database_state} detail={status.database_error || 'SQLite 可读，行情摘要查询正常'} tone={stateTone(status.database_state)} />
					<StatusCard icon={<Activity size={17} />} label="盘中自适应调度" value={liveStateLabels[live?.state || ''] || live?.state || '未知'} detail={live?.running ? `最近执行 ${formatTime(live.last_run_at)} · 下次 ${formatTime(live.next_run_at)}` : `最近检查 ${formatTime(live?.last_check_at)} · 当前不在运行`} tone={stateTone(live?.last_run_status || liveStateLabels[live?.state || ''] || '')} />
				</section>

				<section className="system-status-panel">
					<header><div><span><TimerReset size={15} /> 实时信号更新</span><strong>盘中自适应刷新</strong></div><em className={`system-status-pill ${stateTone(live?.last_run_status || '')}`}>{live?.last_run_status || '等待首次执行'}</em></header>
					<div className="system-status-live-grid">
						<Metric label="交易时段" value={live?.session_active ? '盘中' : '非交易时段'} detail={live?.running ? '调度器运行中' : '调度器未运行'} />
						<Metric label="刷新间隔" value={live?.interval_minutes ? `${live.interval_minutes} 分钟` : live?.state === 'waiting-session' ? '开盘后判定' : '尚未采样'} detail={intervalReasonLabels[live?.interval_reason || ''] || live?.interval_reason || '等待资源与交易时段检查'} />
						<Metric label="容器内存" value={live?.memory_percent ? `${live.memory_percent.toFixed(1)}%` : '—'} detail="cgroup 当前使用率" />
						<Metric label="容器 CPU" value={live?.cpu_known ? `${live.cpu_percent.toFixed(1)}%` : '采样中'} detail="相对容器 CPU 配额" />
						<Metric label="最近检查" value={formatTime(live?.last_check_at)} detail={`启动于 ${formatTime(live?.started_at)}`} />
						<Metric label="最近运行结果" value={live?.last_run_status || '尚无结果'} detail={`下次计划 ${formatTime(live?.next_run_at)}`} />
					</div>
					{(live?.theme || live?.signal) && <div className="system-status-result"><span>题材：{live?.theme || '暂无有效题材快照'}</span><span>信号：{live?.signal || '暂无有效信号'}</span></div>}
					{live?.last_error && <p className="system-status-inline-error">最近错误：{live.last_error}</p>}
					<p className="system-status-footnote">间隔由后端按容器资源与上游失败状态在 15、30、45、60 分钟档位自适应；本面板展示后端实际采样值与最近执行结果。</p>
				</section>

				<section className="system-status-panel">
					<header><div><span><Database size={15} /> 数据覆盖与完整性</span><strong>统一行情底座</strong></div></header>
					{status.market_data?.length ? <div className="system-status-market-grid">{status.market_data.map((market) => <article key={market.market}>
						<div className="system-status-market-heading"><strong>{marketName(market.market)}</strong><em className={`system-status-pill ${stateTone(market.state)}`}>{market.state}</em></div>
						<div className="system-status-market-values"><span>日线 <strong>{market.rows.toLocaleString('zh-CN')}</strong></span><span>最新 <strong>{market.latest_date || '—'}</strong></span><span>应到 <strong>{market.expected_date || '—'}</strong></span><span>无效行 <strong className={market.invalid_rows ? 'bad' : ''}>{market.invalid_rows.toLocaleString('zh-CN')}</strong></span></div>
						{market.error && <small className="system-status-inline-error">{market.error}</small>}
					</article>)}</div> : <div className="system-status-empty">未读取到市场日线摘要。</div>}
				</section>

				<section className="system-status-panel">
					<header><div><span><Clock3 size={15} /> 盘后自动回补</span><strong>计划、执行结果与可回溯日志</strong></div><small>日志保存在外置持久化目录</small></header>
				{status.schedule_read_error && <div className="system-status-alert compact"><AlertTriangle size={15} /><span>{status.schedule_read_error}</span></div>}
				<div className="system-status-jobs">{status.scheduled_jobs.map((job) => <article className="system-status-job" key={job.id}>
					<div className="system-status-job-heading"><div><strong>{job.name}</strong><small>{job.market} · {job.schedule} · {job.timezone}</small></div><em className={`system-status-pill ${stateTone(job.last_status)}`}>{job.last_status}</em></div>
					<div className="system-status-job-times"><span>最近开始<strong>{formatTime(job.last_started_at)}</strong></span><span>最近结束<strong>{formatTime(job.last_finished_at)}</strong></span><span>下次计划<strong>{formatTime(job.next_run_at)}</strong></span><span>退出码<strong>{job.exit_code ?? '—'}</strong></span></div>
					{job.last_message && <p>{job.last_message}</p>}
					{job.last_error && <p className="system-status-inline-error">错误：{job.last_error}</p>}
					<details className="system-status-log"><summary>查看最近日志 · {job.log_path}</summary>{job.recent_log?.length ? <pre>{job.recent_log.join('\n')}</pre> : <p>当前没有可显示的任务日志。</p>}</details>
				</article>)}</div>
				</section>
				<p className="system-status-storage-note"><CheckCircle2 size={15} />数据库与增长型日志均从外置持久化卷读取；本看板只读状态，不在容器内写入监控数据。接口生成时间：{formatTime(status.generated_at)}</p>
			</>}
		</section>
	);
}

function StatusCard({ icon, label, value, detail, tone }: { icon: ReactNode; label: string; value: string; detail: string; tone: string }) {
	return <article className="system-status-card"><span>{icon}{label}</span><strong className={tone}>{value}</strong><small>{detail}</small></article>;
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
	return <div><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
}

function marketName(market: string) {
	return market === 'CN' ? 'A 股' : market === 'US' ? '美股' : market === 'CRYPTO' ? '币圈' : market;
}
