import { Activity, BarChart3, Database, ExternalLink, LoaderCircle, Plus, RefreshCw, Search, Trash2 } from 'lucide-react';
import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react';
import { BackendConfig, KLine, requestJSON } from '../lib/backend';
import type { StockDirectoryData, StockDirectoryEntry } from '../lib/backend';
import './stock-data.css';

type Market = 'CN' | 'US' | 'CRYPTO';
type MarketFilter = 'ALL' | Market;
export type StockDataRow = {
	market: Market;
	symbol: string;
	name: string;
	tracked: boolean;
	tracking_source?: string;
	bar_count: number;
	first_date?: string;
	latest_date?: string;
	latest_close?: number;
	latest_source?: string;
};

type Props = {
	config: BackendConfig | null;
	refreshKey: number;
	onOpenAnalysis: (stock: StockDataRow) => void;
	onOpenStrategy: (stock: StockDataRow) => void;
};

const marketNames: Record<Market, string> = { CN: 'A股', US: '美股', CRYPTO: '币圈' };
const directoryStorageKey = 'easy-stock.stock-directory.v1';
const directoryStorageTTL = 24 * 60 * 60 * 1000;
const cryptoNames: Record<string, string> = {
	ADA: '艾达币', AAVE: 'Aave', AVAX: '雪崩协议', BCH: '比特币现金', BNB: '币安币', BTC: '比特币',
	DOGE: '狗狗币', DOT: '波卡', ETH: '以太坊', FIL: 'Filecoin', LINK: 'Chainlink', LTC: '莱特币',
	NEAR: 'NEAR', PEPE: '佩佩币', SHIB: '柴犬币', SOL: '索拉纳', TON: 'Toncoin', TRX: '波场币',
	UNI: 'Uniswap', USDC: '美元币', USDT: '泰达币', XLM: '恒星币', XRP: '瑞波币',
};
const usNames: Record<string, string> = {
	AAPL: '苹果', ABBV: '艾伯维', AMZN: '亚马逊', AMD: '超威半导体', AVGO: '博通', BAC: '美国银行',
	BABA: '阿里巴巴', BRK_B: '伯克希尔·哈撒韦', COIN: 'Coinbase', COST: '好市多', DIS: '迪士尼',
	GOOG: '谷歌', GOOGL: '谷歌', INTC: '英特尔', JPM: '摩根大通', META: 'Meta', MSFT: '微软',
	NFLX: '奈飞', NIO: '蔚来', NVDA: '英伟达', ORCL: '甲骨文', PDD: '拼多多', PLTR: 'Palantir',
	QCOM: '高通', TSLA: '特斯拉', TSM: '台积电', XOM: '埃克森美孚',
};
const keyOf = (stock: StockDataRow) => `${stock.market}:${stock.symbol}`;
const formatPrice = (value?: number) => value && Number.isFinite(value) ? value.toFixed(2) : '--';
const normalizeSymbol = (value: string) => value.trim().toUpperCase().split(/[./:_-]/)[0];

function loadDirectoryCache(): { cachedAt: number; stocks: StockDirectoryEntry[] } {
	try {
		const cached = JSON.parse(window.localStorage.getItem(directoryStorageKey) || '{}') as { cachedAt?: number; stocks?: StockDirectoryEntry[] };
		return { cachedAt: cached.cachedAt || 0, stocks: Array.isArray(cached.stocks) ? cached.stocks : [] };
	} catch {
		return { cachedAt: 0, stocks: [] };
	}
}

export function StockDataWorkspace({ config, refreshKey, onOpenAnalysis, onOpenStrategy }: Props) {
	const [stocks, setStocks] = useState<StockDataRow[]>([]);
	const [directoryCache, setDirectoryCache] = useState(loadDirectoryCache);
	const [marketFilter, setMarketFilter] = useState<MarketFilter>('ALL');
	const [trackedOnly, setTrackedOnly] = useState(false);
	const [addMarket, setAddMarket] = useState<Market>('CN');
	const [symbolInput, setSymbolInput] = useState('');
	const [nameInput, setNameInput] = useState('');
	const [query, setQuery] = useState('');
	const [selectedKey, setSelectedKey] = useState('');
	const [bars, setBars] = useState<KLine[]>([]);
	const [loading, setLoading] = useState(false);
	const [barsLoading, setBarsLoading] = useState(false);
	const [busyKey, setBusyKey] = useState('');
	const [error, setError] = useState('');
	const [barsError, setBarsError] = useState('');
	const [notice, setNotice] = useState('');
	const [dataVersion, setDataVersion] = useState(0);

	const reload = useCallback(async () => {
		if (!config) return;
		setLoading(true);
		setError('');
		try {
			const payload = await requestJSON<{ data: StockDataRow[] }>(config, '/api/v1/market-data/stocks');
			setStocks(payload.data || []);
			setDataVersion((value) => value + 1);
		} catch (loadError) {
			setError(loadError instanceof Error ? loadError.message : '读取行情库失败');
		} finally {
			setLoading(false);
		}
	}, [config]);

	useEffect(() => { void reload(); }, [reload, refreshKey]);

	useEffect(() => {
		if (!config || !stocks.some((stock) => stock.market === 'CN' && (!stock.name.trim() || stock.name.trim().toUpperCase() === stock.symbol.toUpperCase()))) return;
		if (directoryCache.cachedAt && Date.now() - directoryCache.cachedAt < directoryStorageTTL) return;
		let cancelled = false;
		void requestJSON<{ data: StockDirectoryData }>(config, '/api/v1/stocks/directory')
			.then((payload) => {
				if (cancelled) return;
				const stocks = payload.data.stocks || [];
				const cachedAt = Date.now();
				setDirectoryCache({ cachedAt, stocks });
				try { window.localStorage.setItem(directoryStorageKey, JSON.stringify({ cachedAt, stocks })); } catch { /* in-memory names remain available */ }
			})
			.catch(() => { /* Keep cached names and symbols usable if the directory is unavailable. */ });
		return () => { cancelled = true; };
	}, [config, stocks, directoryCache.cachedAt]);

	const directoryNames = useMemo(() => {
		const names = new Map<string, string>();
		for (const item of directoryCache.stocks) {
			if (!item.name.trim()) continue;
			names.set(normalizeSymbol(item.symbol), item.name.trim());
			names.set(normalizeSymbol(item.code), item.name.trim());
		}
		return names;
	}, [directoryCache.stocks]);
	const namedStocks = useMemo(() => stocks.map((stock) => {
		const storedName = stock.name.trim();
		const hasStoredName = storedName && storedName.toUpperCase() !== stock.symbol.trim().toUpperCase();
		const symbol = normalizeSymbol(stock.symbol);
		const ticker = stock.symbol.trim().toUpperCase().replace(/[./-]/g, '_');
		const directoryName = stock.market === 'CN' ? directoryNames.get(symbol) : undefined;
		const knownName = stock.market === 'CRYPTO' ? cryptoNames[symbol] : stock.market === 'US' ? usNames[ticker] || usNames[symbol] : undefined;
		return { ...stock, name: hasStoredName ? storedName : directoryName || knownName || stock.symbol };
	}), [stocks, directoryNames]);

	const visibleStocks = useMemo(() => {
		const normalizedQuery = query.trim().toLowerCase();
		return namedStocks.filter((stock) => (marketFilter === 'ALL' || stock.market === marketFilter)
			&& (!trackedOnly || stock.tracked)
			&& (!normalizedQuery || `${stock.symbol} ${stock.name}`.toLowerCase().includes(normalizedQuery)));
	}, [namedStocks, marketFilter, trackedOnly, query]);
	const selected = visibleStocks.find((stock) => keyOf(stock) === selectedKey) || visibleStocks[0] || null;
	const marketCounts = useMemo(() => ({
		ALL: stocks.length,
		CN: stocks.filter((stock) => stock.market === 'CN').length,
		US: stocks.filter((stock) => stock.market === 'US').length,
		CRYPTO: stocks.filter((stock) => stock.market === 'CRYPTO').length,
	}), [stocks]);
	const stats = useMemo(() => ({
		tracked: stocks.filter((stock) => stock.tracked).length,
		bars: stocks.reduce((sum, stock) => sum + stock.bar_count, 0),
		latest: (() => { const dates = stocks.map((stock) => stock.latest_date || '').filter(Boolean).sort(); return dates[dates.length - 1] || '--'; })(),
	}), [stocks]);

	useEffect(() => {
		if (!selected) {
			setBars([]);
			setBarsError('');
			return;
		}
		if (!config) return;
		let cancelled = false;
		setBarsLoading(true);
		setBarsError('');
		void requestJSON<{ data: KLine[] }>(config, `/api/v1/market-data/stocks/${selected.market}/${encodeURIComponent(selected.symbol)}/bars?limit=60`)
			.then((payload) => { if (!cancelled) setBars(payload.data || []); })
			.catch((loadError) => { if (!cancelled) setBarsError(loadError instanceof Error ? loadError.message : '读取日线失败'); })
			.finally(() => { if (!cancelled) setBarsLoading(false); });
		return () => { cancelled = true; };
	}, [config, selected?.market, selected?.symbol, dataVersion]);

	const addStock = async (event: FormEvent) => {
		event.preventDefault();
		if (!config || !symbolInput.trim()) return;
		setBusyKey('add');
		setError('');
		setNotice('');
		try {
			const stock = await requestJSON<{ market: Market; symbol: string }>(config, '/api/v1/market-data/stocks', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ market: addMarket, symbol: symbolInput.trim(), name: nameInput.trim() }),
			});
			setSymbolInput('');
			setNameInput('');
			setMarketFilter(stock.market);
			setSelectedKey(`${stock.market}:${stock.symbol}`);
			setNotice(stock.market === 'CRYPTO'
				? `${marketNames[stock.market]} ${stock.symbol} 已加入跟踪名单；当前 CN/US 盘后任务不会回补币圈行情。`
				: `${marketNames[stock.market]} ${stock.symbol} 已加入自动更新名单；首次历史回补由盘后任务处理。`);
			await reload();
		} catch (saveError) {
			setError(saveError instanceof Error ? saveError.message : '加入更新名单失败');
		} finally {
			setBusyKey('');
		}
	};

	const setTracking = async (stock: StockDataRow, enabled: boolean) => {
		if (!config) return;
		if (!enabled && !window.confirm(`将 ${stock.name || stock.symbol} 移出自动更新名单。已有日线会保留；确认继续？`)) return;
		setBusyKey(keyOf(stock));
		setError('');
		setNotice('');
		try {
			if (enabled) {
				await requestJSON(config, '/api/v1/market-data/stocks', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ market: stock.market, symbol: stock.symbol, name: stock.name }),
				});
			} else {
				await requestJSON(config, `/api/v1/market-data/stocks/${stock.market}/${encodeURIComponent(stock.symbol)}`, { method: 'DELETE' });
			}
			setNotice(enabled ? `${stock.symbol} 已加入自动更新名单。` : `${stock.symbol} 已移出自动更新名单，历史行情保留。`);
			await reload();
		} catch (saveError) {
			setError(saveError instanceof Error ? saveError.message : '更新名单失败');
		} finally {
			setBusyKey('');
		}
	};

	const clearCache = async (stock: StockDataRow) => {
		if (!config || !window.confirm(`永久删除 ${stock.market} ${stock.symbol} 的 ${stock.bar_count} 条本地日线？自动更新名单状态不变。`)) return;
		setBusyKey(keyOf(stock));
		setError('');
		setNotice('');
		try {
			const result = await requestJSON<{ deleted_bars: number }>(config, `/api/v1/market-data/stocks/${stock.market}/${encodeURIComponent(stock.symbol)}/cache`, { method: 'DELETE' });
			setNotice(`已删除 ${result.deleted_bars} 条 ${stock.symbol} 日线；若仍在更新名单，后续盘后任务会重新回补。`);
			await reload();
		} catch (saveError) {
			setError(saveError instanceof Error ? saveError.message : '删除本地日线失败');
		} finally {
			setBusyKey('');
		}
	};

	return <main className="stock-data-workspace">
		<section className="stock-data-summary">
			<div><span>本地行情标的</span><strong>{stocks.length}</strong><small>跨市场持久化缓存</small></div>
			<button type="button" className={`stock-data-summary-tracked ${trackedOnly ? 'active' : ''}`} aria-pressed={trackedOnly} onClick={() => setTrackedOnly(true)}><span>跟踪名单</span><strong>{stats.tracked}</strong><small>点击查看 · CN / US 盘后回补</small></button>
			<div><span>本地日线</span><strong>{stats.bars.toLocaleString()}</strong><small>数据范围截至 {stats.latest}</small></div>
		</section>

		<section className="stock-data-panel">
			<header className="stock-data-heading">
				<div><span><Database size={16} /> 统一行情底座</span><h2>持久化行情管理</h2><p>行情请求缓存与盘后跟踪名单分开管理；移出名单保留历史，清理缓存单独操作。</p></div>
			<button type="button" className="stock-data-refresh" onClick={() => void reload()} disabled={loading}><RefreshCw size={15} className={loading ? 'stock-data-spin' : ''} />刷新</button>
			</header>
			<form className="stock-data-add" onSubmit={(event) => void addStock(event)}>
				<label><span>市场</span><select value={addMarket} onChange={(event) => setAddMarket(event.target.value as Market)}><option value="CN">A股</option><option value="US">美股</option><option value="CRYPTO">币圈</option></select></label>
				<label><span>代码</span><input required value={symbolInput} onChange={(event) => setSymbolInput(event.target.value)} placeholder={addMarket === 'CN' ? '例如 600519' : addMarket === 'US' ? '例如 AAPL' : '例如 BTC'} /></label>
				<label><span>名称（可选）</span><input value={nameInput} onChange={(event) => setNameInput(event.target.value)} placeholder="展示名称" /></label>
				<button type="submit" disabled={!config || busyKey === 'add'}><Plus size={15} />{addMarket === 'CRYPTO' ? '加入跟踪名单' : '加入自动更新'}</button>
			</form>
			{error && <p className="stock-data-message error" role="alert">{error}</p>}
			{notice && <p className="stock-data-message" role="status">{notice}</p>}
			<div className="stock-data-controls">
				<div className="stock-data-scope-tabs" role="tablist" aria-label="行情范围">
					<button type="button" role="tab" aria-selected={!trackedOnly} className={!trackedOnly ? 'active' : ''} onClick={() => setTrackedOnly(false)}>全部标的 <span>{stocks.length}</span></button>
					<button type="button" role="tab" aria-selected={trackedOnly} className={trackedOnly ? 'active' : ''} onClick={() => setTrackedOnly(true)}>跟踪名单 <span>{stats.tracked}</span></button>
				</div>
				<div className="stock-data-market-row">
				<div className="stock-data-markets" role="group" aria-label="按市场筛选">
					{(['ALL', 'CN', 'US', 'CRYPTO'] as MarketFilter[]).map((market) => <button type="button" key={market} className={marketFilter === market ? 'active' : ''} onClick={() => setMarketFilter(market)}>{market === 'ALL' ? '全部' : marketNames[market]} <span>{marketCounts[market]}</span></button>)}
				</div>
				<label className="stock-data-search"><Search size={15} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索代码或名称" /></label>
				</div>
			</div>
			<div className="stock-data-columns">
				<div className="stock-data-list-wrap">
					<table className="stock-data-list">
						<thead><tr><th>标的</th><th>市场</th><th>本地日线</th><th>最新日期 / 收盘</th><th>更新来源</th><th>操作</th></tr></thead>
						<tbody>
							{visibleStocks.map((stock) => <tr key={keyOf(stock)} className={selected && keyOf(selected) === keyOf(stock) ? 'selected' : ''} onClick={() => setSelectedKey(keyOf(stock))}>
								<td><strong>{stock.name || stock.symbol}</strong><small>{stock.symbol}</small></td>
								<td>{marketNames[stock.market]}</td>
								<td>{stock.bar_count.toLocaleString()} 条</td>
								<td>{stock.latest_date || '--'} / {formatPrice(stock.latest_close)}</td>
								<td><span className={`stock-data-state ${stock.tracked ? 'tracked' : ''}`}>{stock.tracked ? '自动更新' : '仅缓存'}</span><small className="stock-data-source">{stock.latest_source || stock.tracking_source || '—'}</small></td>
								<td><div className="stock-data-row-actions">
									<button type="button" title={stock.tracked ? '移出自动更新名单' : '加入自动更新名单'} aria-label={stock.tracked ? `移出 ${stock.symbol} 更新名单` : `加入 ${stock.symbol} 更新名单`} disabled={busyKey === keyOf(stock)} onClick={(event) => { event.stopPropagation(); void setTracking(stock, !stock.tracked); }}>{stock.tracked ? <Trash2 size={14} /> : <Plus size={14} />}</button>
									<button type="button" title="打开系统个股分析" aria-label={`分析 ${stock.symbol}`} onClick={(event) => { event.stopPropagation(); onOpenAnalysis(stock); }}><ExternalLink size={14} /></button>
								</div></td>
							</tr>)}
							{!loading && visibleStocks.length === 0 && <tr><td colSpan={6} className="stock-data-empty">{error ? '行情库暂不可用' : trackedOnly ? '当前市场还没有跟踪标的。' : '当前市场还没有持久化标的。'}</td></tr>}
						</tbody>
					</table>
					{loading && <div className="stock-data-loading"><LoaderCircle size={17} className="stock-data-spin" />正在读取共享 SQLite 行情库…</div>}
				</div>
				<aside className="stock-data-detail">
					{selected ? <>
						<div className="stock-data-detail-heading"><div><span>{marketNames[selected.market]} · {selected.symbol}</span><h3>{selected.name || selected.symbol}</h3><small>{selected.bar_count} 条日线 · {selected.first_date || '--'} 至 {selected.latest_date || '--'}</small></div><BarChart3 size={18} /></div>
						<div className="stock-data-detail-actions"><button type="button" onClick={() => onOpenAnalysis(selected)}><Activity size={14} />个股分析</button><button type="button" onClick={() => onOpenStrategy(selected)}><ExternalLink size={14} />策略研判</button><button type="button" className="danger" disabled={busyKey === keyOf(selected)} onClick={() => void clearCache(selected)}><Trash2 size={14} />清空日线</button></div>
						{barsError && <p className="stock-data-message error">{barsError}</p>}
						<div className="stock-data-bars-wrap">
							<table className="stock-data-bars"><thead><tr><th>日期</th><th>开</th><th>高</th><th>低</th><th>收</th><th>成交量</th></tr></thead><tbody>
								{[...bars].reverse().map((bar) => <tr key={`${bar.time}-${bar.symbol}`}><td>{bar.time.slice(0, 10)}</td><td>{formatPrice(bar.open)}</td><td>{formatPrice(bar.high)}</td><td>{formatPrice(bar.low)}</td><td>{formatPrice(bar.close)}</td><td>{Math.round(bar.volume).toLocaleString()}</td></tr>)}
								{!barsLoading && bars.length === 0 && <tr><td colSpan={6} className="stock-data-empty">暂无本地日线；加入名单后由盘后任务补齐。</td></tr>}
							</tbody></table>
							{barsLoading && <div className="stock-data-loading"><LoaderCircle size={16} className="stock-data-spin" />读取日线明细…</div>}
						</div>
					</> : <div className="stock-data-no-selection"><Database size={22} /><strong>选择标的查看本地行情</strong><span>新增标的会在下次盘后更新任务中进行初次回补。</span></div>}
				</aside>
			</div>
			<footer className="stock-data-note"><Activity size={14} /><span>已存日线含按需行情请求缓存；名单汇总原有自选/持仓/复盘候选及手动添加。CN/US 盘后任务读取该名单，币圈需另行运行币圈回补任务。移出名单不删除历史行情；清理后仍跟踪的标的会在后续任务重新回补。</span></footer>
		</section>
	</main>;
}
