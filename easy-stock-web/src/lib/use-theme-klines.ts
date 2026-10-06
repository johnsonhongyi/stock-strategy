import { useEffect, useMemo, useReducer } from 'react';
import { BackendConfig, KLine, requestJSON } from './backend';
import { ThemeKLineQueue } from './theme-kline-queue';
import { KLineLookup } from './short-term';

export function useThemeKLines(config: BackendConfig | null, active: boolean, symbols: string[], selected: string, prefetch: string[], refreshKey: number) {
	const [revision, changed] = useReducer(n => n + 1, 0);
	const queue = useMemo(() => new ThemeKLineQueue(async (symbol, signal) => {
		if (!config) return [];
		const result = await requestJSON<{ data: KLine[] }>(config, `/api/v1/quotes/kline?symbol=${encodeURIComponent(symbol)}&period=day&limit=60`, { signal });
		return result.data;
	}, changed), [config]);
	const key = symbols.join(',');
	const prefetchKey = prefetch.join(',');
	useEffect(() => {
		if (active && config) queue.setWanted(key.split(',').filter(Boolean), selected, prefetchKey.split(',').filter(Boolean));
	}, [active, config, queue, key, selected, prefetchKey, refreshKey]);
	useEffect(() => {
		if (!active) queue.pause();
		return () => queue.pause();
	}, [active, queue]);
	const snapshot = useMemo(() => {
		const histories: KLineLookup = {};
		const failed = new Set<string>();
		let pending = 0;
		for (const symbol of new Set([...symbols, selected].filter(Boolean))) {
			const item = queue.entries.get(symbol);
			if (item?.lines?.length) histories[symbol] = item.lines.slice(-40);
			if (item?.error) failed.add(symbol);
			if (item?.pending || !item) pending++;
		}
		const entry = queue.entries.get(selected);
		const ready = symbols.filter(symbol => histories[symbol]?.length).length;
		const historyState: 'idle' | 'loading' | 'partial' | 'error' | 'ready' = !symbols.length ? 'idle' : pending ? 'loading' : failed.size ? (ready ? 'partial' : 'error') : 'ready';
		const klineState: 'idle' | 'loading' | 'error' | 'ready' = !selected ? 'idle' : entry?.lines?.length ? 'ready' : entry?.error ? 'error' : 'loading';
		return { histories, failed, ready, historyState, klineState, lines: selected ? entry?.lines || [] : [] };
	}, [queue, revision, selected, symbols]);
	useEffect(() => { if (refreshKey > 0 && active) queue.refresh(); }, [refreshKey, queue]);
	return { ...snapshot, retry: () => queue.retryFailed() } as const;
}
