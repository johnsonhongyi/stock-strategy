import type { SBCAnalysis } from '../lib/sbc';

export type SBCLayers = { ma: boolean; channel: boolean; fib: boolean; td: boolean; signals: boolean };
export const defaultSBCLayers: SBCLayers = {ma: true, channel: true, fib: false, td: true, signals: true};
const colors: Record<string, string> = {ma5:'#ffd700', ma10:'#00c9d9', ma20:'#e84afa', ch_upper:'var(--text, #334155)', ch_mid:'#9ca3af', ch_lower:'var(--text, #334155)', chan_kx:'#00ff88', reversal_line:'#fb923c'};

export function SBCOverlay({data, layers, x, y, left, right, top, bottom}: {
  data: SBCAnalysis; layers: SBCLayers; x: (index: number) => number; y: (price: number) => number;
  left: number; right: number; top: number; bottom: number;
}) {
  const points = data.points;
  if (!points.length) return null;
  const last = points.at(-1)!;
  const start = Math.max(0, points.length - Math.max(Number(last.ch_tc2) || 0, Number(last.ch_bc2) || 0));
  const path = (field: string, from = 0) => {
    let connected = false;
    return points.map((point, i) => {
      const value = point[field];
      if (i < from || typeof value !== 'number' || !Number.isFinite(value) || value <= 0 || y(value) < top || y(value) > bottom) { connected = false; return ''; }
      const command = connected ? 'L' : 'M'; connected = true;
      return command + x(i) + ' ' + y(value);
    }).join(' ');
  };
  let lastSignalX = left - 60;
  const strategy = data.strategy;
  return <g pointerEvents="none" fontSize="10">
    {Object.entries(colors).filter(([field]) => field.startsWith('ma') ? layers.ma : layers.channel).map(([field, color]) =>
      <path key={field} d={path(field, field.startsWith('ch_') ? start : 0)} fill="none" stroke={color} strokeWidth="1.3" strokeDasharray={field === 'ch_mid' ? '5 3' : undefined} />)}
    {layers.fib && data.levels.map(level => {
      const value = level.price;
      const field = String(level.ratio);
      return typeof value === 'number' && y(value) >= top && y(value) <= bottom ? <g key={field}><line x1={left} x2={right} y1={y(value)} y2={y(value)} stroke="#d4b921" strokeDasharray="3 3"/><text x={right-4} y={y(value)-3} textAnchor="end" fill="#d4b921">{(level.ratio*100).toFixed(1)}% {value.toFixed(2)}</text></g> : null;
    })}
    {layers.td && points.map((point,i) => {
      const buy = Number(point.td_buy_count), sell = Number(point.td_sell_count);
      const price = Number(point.ch_mid);
      const low = Number(point.low), high = Number(point.high);
      return buy || sell ? <text key={point.time} x={x(i)} y={buy ? Math.min(bottom-2,y(low || price)+12) : Math.max(top+10,y(high || price)-6)} fill={buy ? '#00ff88' : '#ff66aa'} textAnchor="middle" fontWeight={buy === 9 || sell === 9 ? 'bold' : undefined}>{buy || sell}</text> : null;
    })}
    {layers.signals && points.map((point,i) => {
      const labels = ['sig_launch','sig_start','sig_escape','sig_top','sig_bottom'].filter(field => Number(point[field]) === 1);
      if (!labels.length || x(i)-lastSignalX < 52) return null;
      lastSignalX=x(i);
      const label: Record<string,string> = {sig_launch:'启动',sig_start:'起涨',sig_escape:'逃顶',sig_top:'顶部',sig_bottom:'底部'};
      const price = Number(point.close);
      return <text key={point.time} x={x(i)} y={Math.max(top+10,Math.min(bottom-3,y(price)-16))} fill="#ffcc00"><title>{labels.map(field=>label[field]).join(' / ')}</title>{label[labels[0]]}</text>;
    })}
    {layers.signals && strategy.is_matched && <>
      {(['entry_price','stop_loss','target_price_1','target_price_2'] as const).map(field => {
        const value = strategy[field];
        if (!value || y(value) < top || y(value) > bottom) return null;
        const label = {entry_price:'介入',stop_loss:'止损',target_price_1:'目标1',target_price_2:'目标2'}[field];
        return <g key={field}><line x1={left} x2={right} y1={y(value)} y2={y(value)} stroke={field === 'stop_loss' ? '#ff4444' : '#00e6a0'} strokeDasharray="6 3"/><text x={left+4} y={y(value)-3} fill={field === 'stop_loss' ? '#ff4444' : '#00e6a0'}>{label}:{value.toFixed(2)}</text></g>;
      })}
      {strategy.pioneer_sig_indices?.filter(i=>i>=0&&i<points.length).map(i=><g key={i}><line x1={x(i)} x2={x(i)} y1={top} y2={bottom} stroke="#00ff88" strokeDasharray="5 3"/><text x={Math.min(right-60,x(i)+3)} y={top+12} fill="#00ff88">逆势先锋</text></g>)}
    </>}
  </g>;
}
