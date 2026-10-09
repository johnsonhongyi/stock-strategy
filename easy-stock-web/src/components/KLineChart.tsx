import './sbc.css';
import { useSBC, type SBCProvider } from '../lib/sbc';
import { SBCOverlay, defaultSBCLayers } from './SBCOverlay';
import type { BackendConfig } from '../lib/backend';
import { KLine } from '../lib/backend';
import { type MouseEvent, useState, useMemo, useEffect, useRef } from 'react';
import { KLineExplorer } from './KLineExplorer';
import { KLineDetails } from './KLineDetails';
import { SBCIntradayOverlay } from './SBCIntradayOverlay';

export type KLineChartProps = {
	referenceLines?: KLine[];
	onNavigate?: (direction: number) => void;
	title?: string;
	lines: KLine[];
	market?: string;
	onInvalidData?: () => void;
	onRecovered?: (lines: KLine[]) => void;
	config?: BackendConfig | null;
	sbcProvider?: SBCProvider;
	symbol?: string;
	state?: 'loading' | 'ready' | 'error' | 'idle';
	mode?: 'intraday' | 'daily';
	periodLabel?: string;
	compact?: boolean;
};

function formatTime(value: string, mode: 'intraday' | 'daily', periodLabel: string) {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return value.slice(0, 16);
	if (mode === 'intraday' && /^[35]日/.test(periodLabel)) return `${date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })} ${date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}`;
	if (mode === 'intraday') return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
	return date.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' });
}

function formatLongDate(value: string) {
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return value.slice(0, 16);
	return date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
}

function formatVolume(value: number) {
	if (value >= 100_000_000) return `${(value / 100_000_000).toFixed(1)}亿`;
	if (value >= 10_000) return `${(value / 10_000).toFixed(0)}万`;
	return value.toFixed(0);
}

function formatAmount(value: number) {
	if (!Number.isFinite(value) || value <= 0) return '--';
	if (value >= 100_000_000) return `${(value / 100_000_000).toFixed(2)}亿`;
	if (value >= 10_000) return `${(value / 10_000).toFixed(2)}万`;
	return value.toFixed(0);
}

function formatSignedPercent(value: number) {
	if (!Number.isFinite(value)) return '--';
	return `${value > 0 ? '+' : ''}${value.toFixed(2)}%`;
}

function formatAmplitude(value: number) {
	if (!Number.isFinite(value)) return '--';
	return `${value.toFixed(2)}%`;
}

function intradayLimitPercent(symbol = '') {
	const [rawCode, rawMarket] = symbol.toUpperCase().split('.');
	const code = rawCode || '';
	const market = rawMarket || '';
	if (market === 'BJ' || /^(4|8|92)/.test(code)) return 30;
	if (/^(300|301|302|303|688|689)/.test(code)) return 20;
	return 10;
}

function sbcValue(value: unknown, digits = 2) { return typeof value === 'number' && Number.isFinite(value) ? value.toFixed(digits) : '--'; }

export function KLineChart(props: KLineChartProps) { return <KLineExplorer {...props} render={next => <KLineCanvas {...next} />} />; }

function KLineCanvas({ referenceLines, lines, symbol, state = 'ready', mode = 'daily', periodLabel = '日K', compact = false, config, sbcProvider, onInvalidData }: KLineChartProps) {
	const [selectedIndex, setHoveredIndex] = useState<number | null>(null);
	const [layers, setLayers] = useState(defaultSBCLayers);
	const all = useMemo(() => [...lines].sort((a,b) => Date.parse(a.time)-Date.parse(b.time)), [lines]);
    const [view, setView] = useState({count: mode === 'intraday' ? 8000 : 100, offset: 0});
    const svg = useRef<SVGSVGElement>(null);
    const drag = useRef<{x: number; offset: number} | null>(null);
    useEffect(() => { setView({count: mode === 'intraday' ? 8000 : 100, offset: 0}); setHoveredIndex(null); }, [symbol, periodLabel]);
    const count = Math.min(all.length, view.count);
    const end = Math.max(count, all.length - view.offset);
    const startIndex = Math.max(0, end - count);
    const sorted = all.slice(startIndex, end);
    const zoom = (direction: number) => { setHoveredIndex(null); setView(previous => ({...previous, count: Math.max(10, Math.min(all.length, Math.round(Math.min(all.length,previous.count) * (direction>0 ? 1.25 : .8))))})); };
    const pan = (direction: number) => { setHoveredIndex(null); setView(previous => ({...previous, offset: Math.max(0, Math.min(Math.max(0,all.length-previous.count),previous.offset+direction*Math.max(1,Math.round(previous.count/5))))})); };
    useEffect(() => {
        const element = svg.current; if (!element) return;
        const wheel = (event: WheelEvent) => { event.preventDefault(); if (event.shiftKey) pan(event.deltaY>0 ? -1 : 1); else zoom(event.deltaY>0 ? 1 : -1); };
        element.addEventListener('wheel', wheel, {passive:false});
        return () => element.removeEventListener('wheel', wheel);
    }, [all.length, state]);
	const period = ({'日K':'day','2D':'2d','3D':'3d','周K':'week','月K':'month','5分':'5','15分':'15','30分':'30','60分':'60'} as Record<string,string>)[periodLabel] || 'day';
	const sbc = useSBC(config, mode === 'intraday' ? referenceLines || [] : all, symbol || all[0]?.symbol || '', mode === 'intraday' ? 'day' : period, state === 'ready', sbcProvider);
    useEffect(()=>{if (mode === 'daily' && /invalid SBC input.*(OHLC|non-finite|timestamp|symbol)/i.test(sbc.status)) onInvalidData?.();},[sbc.status,onInvalidData]);
	const placeholderClass = compact ? 'kline-chart-placeholder compact' : 'kline-chart-placeholder';
	if (state === 'loading') return <div className={placeholderClass}>正在加载{periodLabel}数据…</div>;
	if (!lines.length && state === 'idle' && !symbol) return <div className={placeholderClass}>请选择股票查看行情</div>;
	if (!lines.length) return <div className={placeholderClass}>{state === 'error' ? `${periodLabel}数据暂不可用，请稍后重试。` : `暂无${periodLabel}数据。`}</div>;

	const width = compact ? 640 : 960;
	const height = compact ? 300 : 430;
	const left = compact ? 38 : 52;
	const right = mode === 'intraday' ? (compact ? 120 : 150) : compact ? 58 : 72;
	const chartTop = compact ? 14 : 20;
	const chartBottom = compact ? 210 : 316;
	const volumeTop = compact ? 232 : 338;
	const volumeBottom = compact ? 266 : 388;
	const plotWidth = width - left - right;
    // Only visible candles control the automatic axis; projected channel/targets
    // are clipped rather than compressing the price action.
    const intradayRefs = mode === 'intraday' ? [all[0]?.previous_close || all[0]?.open, ...sorted.map(line=>line.vwap || line.close), ...['ch_upper','reversal_line','ch_supp_price'].map(field=>Number(sbc.data?.points.at(-1)?.[field])).filter(value=>value>0 && Math.abs(value/all[0].close-1)<.15)].filter((value):value is number=>Boolean(value && Number.isFinite(value))) : [];
    const minPrice = Math.min(...sorted.map(line => line.low), ...intradayRefs);
    const maxPrice = Math.max(...sorted.map(line => line.high), ...intradayRefs);
    const visibleSBC = sbc.data ? {...sbc.data, points:sbc.data.points.slice(startIndex,end), strategy:{...sbc.data.strategy, pioneer_sig_indices:sbc.data.strategy.pioneer_sig_indices?.map(i=>i-startIndex).filter(i=>i>=0&&i<sorted.length)}} : undefined;
	// One-day intraday data is filtered to the latest trading day, so retain
	// the previous close supplied by the API as the 0% baseline.
	const suppliedPreviousClose = all.find((line) => line.previous_close != null && line.previous_close > 0)?.previous_close;
	const referencePrice = mode === 'intraday' && suppliedPreviousClose && suppliedPreviousClose > 0
		? suppliedPreviousClose
		: mode === 'intraday' ? all[0].open : sorted[0].close > 0 ? sorted[0].close : Math.max(minPrice, 0.01);
	const percentChange = (value: number) => ((value - referencePrice) / referencePrice) * 100;
	const percentRange = intradayLimitPercent(symbol);
	const maxVolume = Math.max(...sorted.map((line) => line.volume), 1);
	const step = plotWidth / sorted.length;
	const bodyWidth = Math.max(2, Math.min(10, step * 0.62));
	const padding = Math.max((maxPrice-minPrice)*.08, maxPrice*.005);
    const lowPercent = ((Math.max(.001,minPrice-padding)-referencePrice)/referencePrice)*100;
    const highPercent = ((maxPrice+padding-referencePrice)/referencePrice)*100;
    const percentY = (value: number) => {
		const bounded = value;
		return chartTop + ((highPercent - bounded) / (highPercent-lowPercent)) * (chartBottom - chartTop);
	};
	const priceY = (value: number) => percentY(percentChange(value));
	const volumeY = (value: number) => volumeBottom - (value / maxVolume) * (volumeBottom - volumeTop);
	const percentTicks = Array.from({ length: 7 }, (_, index) => highPercent - ((highPercent-lowPercent) * index) / 6);
	const labelIndexes = sorted.length <= 5 ? sorted.map((_, index) => index) : [0, Math.floor((sorted.length - 1) / 2), sorted.length - 1];
	const dayKey = (value: string) => value.slice(0, 10);
	const intradayBaselines: Array<number | null> = [];
	let currentDay = '';
	let previousClose: number | null = null;
	let currentDayBaseline = referencePrice;
	for (const line of sorted) {
		const key = dayKey(line.time);
		if (key !== currentDay) {
			if (currentDay && previousClose && previousClose > 0) currentDayBaseline = previousClose;
			currentDay = key;
		}
		intradayBaselines.push(currentDayBaseline);
		previousClose = line.close;
	}
	const barBasePrice = (index: number) => {
		if (mode === 'intraday') return intradayBaselines[index] || referencePrice;
		return index > 0 && sorted[index - 1].close > 0 ? sorted[index - 1].close : referencePrice;
	};
	const barChangePercent = (index: number) => {
		const line = sorted[index];
		const base = barBasePrice(index);
		if (base > 0 && (mode === 'intraday' || index > 0)) return ((line.close - base) / base) * 100;
		return line.change_percent != null && Number.isFinite(line.change_percent) ? line.change_percent : percentChange(line.close);
	};
	const hoveredIndex = selectedIndex != null && selectedIndex < sorted.length ? selectedIndex : sorted.length - 1;
	const hoveredLine = sorted[hoveredIndex];
	const hoveredPrevious = hoveredIndex != null && hoveredIndex > 0 ? sorted[hoveredIndex - 1] : null;
	const hoveredBase = hoveredIndex != null ? barBasePrice(hoveredIndex) : hoveredPrevious?.close || referencePrice;
	const dailyChangePercent = (index: number) => {
		const line = sorted[index];
		if (index > 0 && sorted[index - 1].close > 0) return ((line.close - sorted[index - 1].close) / sorted[index - 1].close) * 100;
		return line.change_percent != null && Number.isFinite(line.change_percent) ? line.change_percent : NaN;
	};
	const hoveredChange = hoveredLine && hoveredIndex != null
		? mode === 'daily' ? dailyChangePercent(hoveredIndex) : barChangePercent(hoveredIndex)
		: NaN;
	const hoveredAmplitude = hoveredLine && hoveredBase > 0 ? ((hoveredLine.high - hoveredLine.low) / hoveredBase) * 100 : NaN;
	const hoveredX = hoveredIndex == null || !hoveredLine ? null : left + hoveredIndex * step + step / 2;
	const hoveredY = hoveredLine ? priceY(hoveredLine.close) : null;
	const handleChartMove = (event: MouseEvent<SVGRectElement>) => {
		const bounds = event.currentTarget.getBoundingClientRect();
		if (!bounds.width) return;
		const plotX = left + ((event.clientX - bounds.left) / bounds.width) * plotWidth;
		setHoveredIndex(Math.max(0, Math.min(sorted.length - 1, Math.floor((plotX - left) / step))));
	};
	const linePath = sorted.map((line, index) => {
		const x = left + index * step + step / 2;
		return `${index === 0 ? 'M' : 'L'} ${x.toFixed(2)} ${priceY(line.close).toFixed(2)}`;
	}).join(' ');

	return (
		<div className={`kline-chart-wrap ${mode === 'intraday' ? 'intraday-chart' : 'daily-chart'}${compact ? ' compact' : ''}`}>
			{<div className="sbc-toolbar"><strong>SBC</strong>{(Object.keys(layers) as Array<keyof typeof layers>).filter(key=>mode==='daily'||key==='channel'||key==='signals').map(key => <label key={key}><input type="checkbox" checked={layers[key]} onChange={() => setLayers(previous => ({...previous,[key]:!previous[key]}))}/>{{ma:'均线',channel:'通道/支撑/反转',fib:'黄金分割',td:'九转',signals:'策略信号'}[key]}</label>)}</div>}
			{<div className="sbc-summary" role="status">{mode==='intraday' && <span>日线关键价位参考 · </span>}{sbc.status || (sbc.data && <>通道斜率 {sbcValue(sbc.data.points.at(-1)?.ch_slope_deg, 1)}° · 支撑 {sbcValue(sbc.data.points.at(-1)?.ch_supp_price, 2)} · 反转 {sbcValue(sbc.data.points.at(-1)?.reversal_line, 2)}{sbc.data.activity && <> · 近{sbc.data.activity.days.length}日均振 {sbc.data.activity.avg_amp}% · {sbc.data.activity.rating}</>} · {sbc.data.strategy.is_matched ? `评分 ${sbc.data.strategy.score} · 介入 ${sbc.data.strategy.entry_price} · 止损 ${sbc.data.strategy.stop_loss} · 目标 ${sbc.data.strategy.target_price_1}` : sbc.data.strategy.reason || '未命中策略'}{sbc.data.market_meta?.stale && ' · 陈旧行情'} · 截至 {sbc.data.trade_date}</>)}</div>}
			<div className="kline-viewport-tools"><button type="button" onClick={()=>zoom(-1)}>＋ 放大</button><button type="button" onClick={()=>zoom(1)}>－ 缩小</button><button type="button" onClick={()=>pan(1)}>← 历史</button><button type="button" onClick={()=>pan(-1)}>最新 →</button><button type="button" onClick={()=>{setView({count:mode === 'intraday' ? 8000 : 100,offset:0});setHoveredIndex(null);}}>回到最新</button><span>{sorted.length} / {all.length} 根 · 滚轮缩放，Shift+滚轮平移</span></div>
            <svg ref={svg} className="kline-chart" viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`${periodLabel}价格、涨跌幅和成交量图`}>
				{percentTicks.map((tick) => {
					const y = percentY(tick);
					const price = referencePrice * (1 + tick / 100);
					const isZero = Math.abs(tick) < 0.001;
					const percentLabel = `${tick > 0 ? '+' : ''}${tick.toFixed(tick % 1 === 0 ? 0 : 1)}%`;
					return <g key={tick}><line className={`kline-grid ${isZero ? 'kline-zero-grid' : ''}`} x1={left} x2={width - right} y1={y} y2={y} /><text className="kline-percent-axis-label" x={left - 9} y={y + 4} textAnchor="end">{percentLabel}</text><text className="kline-axis-label" x={width - right + 9} y={y + 4}>{price.toFixed(2)}</text></g>;
				})}
				<line className="kline-divider" x1={left} x2={width - right} y1={volumeTop - 12} y2={volumeTop - 12} />
				{mode === 'intraday' ? (
					<>
						<path className="kline-close-line" d={linePath} />
                        <path className="kline-average-line" d={sorted.map((line,index)=>line.vwap && Number.isFinite(line.vwap) ? `${index===0||!sorted[index-1].vwap?'M':'L'} ${(left+index*step+step/2).toFixed(2)} ${priceY(line.vwap).toFixed(2)}`:'').join(' ')}/>
                        <SBCIntradayOverlay lines={all} reference={sbc.data} layers={layers} y={priceY} left={left} right={width-right} top={chartTop} bottom={chartBottom}/>

						{sorted.map((line, index) => {
							const x = left + index * step + step / 2;
							const barTop = volumeY(line.volume);
							const change = barChangePercent(index);
							return <g key={`${line.time}-${index}`} className={line.close >= line.open ? 'kline-up' : 'kline-down'}><title>{`${formatLongDate(line.time)} 收 ${line.close.toFixed(2)}（${change >= 0 ? '+' : ''}${change.toFixed(2)}%）成交量 ${formatVolume(line.volume)}`}</title><circle className="kline-close-point" cx={x} cy={priceY(line.close)} r={Math.max(1.5, Math.min(3, step / 3))} /><rect className="kline-volume" x={x - bodyWidth / 2} y={barTop} width={bodyWidth} height={volumeBottom - barTop} /></g>;
						})}
					</>
				) : sorted.map((line, index) => {
					const x = left + index * step + step / 2;
					const rising = line.close >= line.open;
					const bodyTop = priceY(Math.max(line.open, line.close));
					const bodyBottom = priceY(Math.min(line.open, line.close));
					const barTop = volumeY(line.volume);
					const change = barChangePercent(index);
					return <g className={rising ? 'kline-up' : 'kline-down'} key={`${line.time}-${index}`}><title>{`${formatLongDate(line.time)} 开 ${line.open.toFixed(2)} 高 ${line.high.toFixed(2)} 低 ${line.low.toFixed(2)} 收 ${line.close.toFixed(2)}（${change >= 0 ? '+' : ''}${change.toFixed(2)}%）成交量 ${formatVolume(line.volume)}`}</title><line className="kline-wick" x1={x} x2={x} y1={priceY(line.high)} y2={priceY(line.low)} /><rect className="kline-body" x={x - bodyWidth / 2} y={bodyTop} width={bodyWidth} height={Math.max(bodyBottom - bodyTop, 1.5)} /><rect className="kline-volume" x={x - bodyWidth / 2} y={barTop} width={bodyWidth} height={volumeBottom - barTop} /></g>;
				})}
				{mode === 'daily' && visibleSBC && <SBCOverlay data={visibleSBC} layers={layers} x={i => left+i*step+step/2} y={priceY} left={left} right={width-right} top={chartTop} bottom={chartBottom}/>}
				{labelIndexes.map((index) => {
					const line = sorted[index];
					const x = left + index * step + step / 2;
					return <text className="kline-date-label" x={x} y={height - 15} textAnchor={index === 0 ? 'start' : index === sorted.length - 1 ? 'end' : 'middle'} key={line.time}>{formatTime(line.time, mode, periodLabel)}</text>;
				})}
				{hoveredX != null && hoveredY != null && <>
					<line className="kline-crosshair" x1={hoveredX} x2={hoveredX} y1={chartTop} y2={volumeBottom} />
					<line className="kline-crosshair kline-crosshair-horizontal" x1={left} x2={width - right} y1={hoveredY} y2={hoveredY} />
					<circle className="kline-crosshair-point" cx={hoveredX} cy={hoveredY} r="4" />
				</>}
				<rect onPointerDown={event=>{drag.current={x:event.clientX,offset:view.offset};event.currentTarget.setPointerCapture(event.pointerId);}} onPointerUp={()=>{drag.current=null;}} onPointerCancel={()=>{drag.current=null;}} onPointerMove={event=>{if(!drag.current)return;const bounds=event.currentTarget.getBoundingClientRect();const offset=drag.current.offset+Math.round((event.clientX-drag.current.x)/bounds.width*count);setHoveredIndex(null);setView(previous=>({...previous,offset:Math.max(0,Math.min(all.length-count,offset))}));}} className="kline-hover-layer" x={left} y={chartTop} width={plotWidth} height={volumeBottom - chartTop} onMouseMove={handleChartMove} aria-label="悬浮查看行情明细" />
			</svg>
			{hoveredLine && <KLineDetails compact={compact}>
				<strong>{mode === 'intraday' ? formatLongDate(hoveredLine.time) : formatTime(hoveredLine.time, mode, periodLabel)}</strong>
				<div><span>开盘</span><b>{hoveredLine.open.toFixed(2)}</b></div>
				<div><span>收盘</span><b className={hoveredLine.close >= hoveredLine.open ? 'up' : 'down'}>{hoveredLine.close.toFixed(2)}</b></div>
				<div><span>最高</span><b className="up">{hoveredLine.high.toFixed(2)}</b></div>
				<div><span>最低</span><b className="down">{hoveredLine.low.toFixed(2)}</b></div>
				<div><span>涨跌幅</span><b className={hoveredChange >= 0 ? 'up' : hoveredChange < 0 ? 'down' : ''}>{formatSignedPercent(hoveredChange)}</b></div>
				<div><span>振幅</span><b>{formatAmplitude(hoveredAmplitude)}</b></div>
				<div><span>成交量</span><b>{formatVolume(hoveredLine.volume)}</b></div>
				<div><span>成交额</span><b>{formatAmount(hoveredLine.amount)}</b></div>
				<div><span>换手率</span><b>{hoveredLine.turnover_rate != null && Number.isFinite(hoveredLine.turnover_rate) ? `${hoveredLine.turnover_rate.toFixed(2)}%` : '--'}</b></div>
			</KLineDetails>}
			<div className="kline-chart-legend"><span className="zero-dot" />0%基准{mode==='intraday'?(suppliedPreviousClose?'（昨收）':'（首根开盘，昨收缺失）'):''}：{referencePrice.toFixed(2)}{mode === 'intraday' && <span className="limit-note">制度参考 ±{percentRange}%</span>} <span className="volume-note">左侧涨跌幅 · 右侧价格 · 柱体为成交量</span>{mode === 'intraday' && <><span className="close-dot" />价格 <span className="average-dot" />VWAP{all.at(-1)?.vwap_estimated?'（量加权估算）':!all.at(-1)?.vwap?'（数据缺失）':''}</>}</div>
		</div>
	);
}
