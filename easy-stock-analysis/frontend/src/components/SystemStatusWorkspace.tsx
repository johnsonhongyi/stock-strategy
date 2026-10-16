import { Activity, Clock3, Database, RefreshCw, Server } from 'lucide-react';
import type { ReactNode } from 'react';
import { useEffect, useState } from 'react';
import { BackendConfig, requestJSON } from '../lib/backend';
import { TypographySettings } from './TypographySettings';

type MarketRuntime = {
	running: boolean;
	state: string;
	session_active: boolean;
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
	updated_at?: string;
};

type MarketDataStatus = {
	market: string;
	rows: number;
	latest_date?: string;
	expected_date?: string;
	invalid_rows: number;
	source?: string;
	tdx_codes?: number;
	tdx_fresh_codes?: number;
	tdx_stale_codes?: number;
	tdx_invalid_codes?: number;
	coverage_basis?: string;
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

type SystemStatusSnapshot = {
	generated_at: string;
	api_state: string;
	api_started_at?: string;
	api_uptime_seconds: number;
	database_state: string;
	database_error?: string;
	live_market: MarketRuntime;
	market_data: MarketDataStatus[];
	scheduled_jobs: ScheduledJob[];
	schedule_read_error?: string;
};

const MARKET_LABELS: Record<string, string> = { CN: 'A 股', US: '美股', CRYPTO: '数字货币' };

function timeLabel(value?: string) {
	if (!value) return '--';
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-HK', { hour12: false });
}

function uptimeLabel(seconds: number) {
	if (!Number.isFinite(seconds) || seconds < 0) return '--';
	const totalMinutes = Math.floor(seconds / 60);
	const days = Math.floor(totalMinutes / 1440);
	const hours = Math.floor((totalMinutes % 1440) / 60);
	const minutes = totalMinutes % 60;
	return days ? `${days} 天 ${hours} 小时` : hours ? `${hours} 小时 ${minutes} 分钟` : `${minutes} 分钟`;
}

export function SystemStatusWorkspace({ config, refreshKey, onRefresh }: { config: BackendConfig | null; refreshKey: number; onRefresh: () => void }) {
	const [snapshot, setSnapshot] = useState<SystemStatusSnapshot | null>(null);
	const [loadState, setLoadState] = useState<'loading' | 'ready' | 'error'>('loading');
	const [error, setError] = useState('');

	useEffect(() => {
		let active = true;
		const load = async () => {
			if (!config) {
				setLoadState('error');
				setError('后台服务尚未连接');
				return;
			}
			try {
				const result = await requestJSON<SystemStatusSnapshot>(config, '/api/v1/system/status');
				if (!active) return;
				setSnapshot(result);
				setError('');
				setLoadState('ready');
			} catch (cause) {
				if (!active) return;
				setError(cause instanceof Error ? cause.message : '读取后台状态失败');
				setLoadState('error');
			}
		};
		void load();
		const timer = window.setInterval(() => void load(), 60_000);
		return () => {
			active = false;
			window.clearInterval(timer);
		};
	}, [config, refreshKey]);

	return (
		<section className="system-status-workspace">
			<header className="system-status-heading">
				<div className="system-status-heading-icon"><Server size={24} aria-hidden="true" /></div>
				<div>
					<span className="system-status-kicker"><Activity size={15} aria-hidden="true" /> Runtime Operations</span>
					<h2>后台运行状态</h2>
					<p>查看后台服务、行情数据新鲜度和盘后任务计划。状态每分钟自动刷新。</p>
				</div>
				<button type="button" className="system-status-refresh" onClick={onRefresh} aria-label="刷新状态">
					<RefreshCw size={17} aria-hidden="true" />
				</button>
			</header>

			{loadState === 'error' && <p className="system-status-error" role="alert">{error || '状态读取失败'}</p>}
			{loadState === 'loading' && !snapshot && <p className="system-status-loading">正在读取后台状态…</p>}

			{snapshot && <>
				<div className="system-status-summary-grid">
					<StatusCard icon={<Server size={17} aria-hidden="true" />} title="API 服务" state={snapshot.api_state} detail={`运行 ${uptimeLabel(snapshot.api_uptime_seconds)} · 启动 ${timeLabel(snapshot.api_started_at)}`} />
					<StatusCard icon={<Database size={17} aria-hidden="true" />} title="统一行情数据库" state={snapshot.database_state} detail={snapshot.database_error || 'SQLite 可读，行情摘要查询完成'} />
					<StatusCard icon={<Activity size={17} aria-hidden="true" />} title="盘中自适应刷新" state={snapshot.live_market.state} detail={`${snapshot.live_market.interval_reason || (snapshot.live_market.session_active ? '交易时段内运行' : '非交易时段暂停刷新')} · CPU ${snapshot.live_market.cpu_known ? `${snapshot.live_market.cpu_percent.toFixed(1)}%` : '--'} · 内存 ${snapshot.live_market.memory_percent.toFixed(1)}% · 最近运行 ${timeLabel(snapshot.live_market.last_run_at)}`} />
				</div>

				<section className="system-status-panel">
					<header><div><strong>行情数据覆盖</strong><small>快照时间 {timeLabel(snapshot.generated_at)}</small></div></header>
					<div className="system-status-market-grid">
						{snapshot.market_data.map((item) => (
							<article key={item.market}>
								<div><strong>{MARKET_LABELS[item.market] || item.market}</strong><em>{item.state}</em></div>
								<p>最新日期 {item.latest_date || '--'} · 预期日期 {item.expected_date || '--'}</p>
								<p>{item.rows.toLocaleString()} 条记录 · 异常 {item.invalid_rows.toLocaleString()} 条</p>
								{item.tdx_codes !== undefined && <p>TDX 文件 {item.tdx_codes} · 新鲜 {item.tdx_fresh_codes ?? 0} · 待回补 {item.tdx_stale_codes ?? 0}</p>}
								{(item.error || item.coverage_basis) && <small>{item.error || item.coverage_basis}</small>}
							</article>
						))}
					</div>
				</section>

				<section className="system-status-panel">
					<header><div><strong>盘后任务计划</strong><small>按市场时区展示下次执行时间和最近结果</small></div></header>
					{snapshot.schedule_read_error && <p className="system-status-error">{snapshot.schedule_read_error}</p>}
					<div className="system-status-jobs">
						{snapshot.scheduled_jobs.map((job) => (
							<article key={job.id}>
								<div className="system-status-job-heading"><div><strong>{job.name}</strong><small>{job.market} · {job.schedule} · {job.timezone}</small></div><em>{job.last_status}</em></div>
								<p><Clock3 size={14} aria-hidden="true" /> 下次 {timeLabel(job.next_run_at)} · 最近完成 {timeLabel(job.last_finished_at)}</p>
								{(job.last_error || job.last_message) && <small>{job.last_error || job.last_message}</small>}
								{job.recent_log?.length ? <details className="system-status-job-logs"><summary>查看最近日志</summary><pre>{job.recent_log.slice(-12).join('\n')}</pre></details> : null}
							</article>
						))}
						{!snapshot.scheduled_jobs.length && <p className="system-status-empty">暂无盘后任务记录</p>}
					</div>
				</section>
			</>}

			<TypographySettings />
		</section>
	);
}

function StatusCard({ icon, title, state, detail }: { icon: ReactNode; title: string; state: string; detail: string }) {
	return <article className="system-status-card"><header>{icon}<span>{title}</span></header><strong>{state || '--'}</strong><p>{detail}</p></article>;
}
