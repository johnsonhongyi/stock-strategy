import type { SBCAnalysis } from '../lib/sbc';
import { SBC_COLORS as C, fibColor, clusterSignalMarkers } from '../lib/sbc-style';

export type SBCLayers = { ma: boolean; channel: boolean; fib: boolean; td: boolean; signals: boolean };
export const defaultSBCLayers: SBCLayers = {ma: true, channel: true, fib: false, td: true, signals: true};
const colors: Record<string, string> = {ma5:C.ma5, ma20:C.ma20, ch_upper:C.upper, ch_mid:C.middle, ch_lower:C.lower, chan_kx:C.channelSupport, reversal_line:C.klineReversal};

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
  const plans = ['sig_launch','sig_escape'].map(field => ({field, markers:clusterSignalMarkers(points.flatMap((point,i)=>Number(point[field])===1?[i]:[]))}));
  const strategy = data.strategy;
  return <g data-sbc-layer="daily" pointerEvents="none" fontSize="10">
    {Object.entries(colors).filter(([field]) => field.startsWith('ma') ? layers.ma : layers.channel).map(([field, color]) =>
      <path key={field} d={path(field, field.startsWith('ch_') ? start : 0)} fill="none" stroke={color} strokeWidth={field === 'chan_kx' ? 2 : field.startsWith('ch_') ? 1.5 : 1} strokeDasharray={field === 'ch_mid' ? '5 3' : undefined} />)}
    {layers.channel && (()=>{
      const entries:[string,number,string][]=[['上轨',Number(last.ch_upper),'#ffffff'],['中轨',Number(last.ch_mid),'#c0c0d0'],['下轨',Number(last.ch_lower),'#ffffff'],['支撑',Number(last.ch_supp_price),'#ffd700'],['反转',Number(last.reversal_line),'#00ff88']];
      const used:number[]=[];
      return entries.map(([label,value,color])=>{
        const cy=y(value);if(!Number.isFinite(cy)||value<=0||cy<top||cy>bottom)return null;
        let labelY=cy;while(used.some(other=>Math.abs(other-labelY)<12)&&labelY<bottom-12)labelY+=12;used.push(labelY);
        return <text key={label} x={right+4} y={labelY+3} fill={color} fontSize="9">{label}:{value.toFixed(2)}</text>;
      });
    })()}
    {layers.channel && Number(last.ch_anchor_low_price)>0 && y(Number(last.ch_anchor_low_price))>=top && y(Number(last.ch_anchor_low_price))<=bottom && <line x1={x(start)} x2={right} y1={y(Number(last.ch_anchor_low_price))} y2={y(Number(last.ch_anchor_low_price))} stroke="#00ffff" strokeDasharray="6 3"/>}
    {layers.fib && data.levels.map(level => {
      const value = level.price;
      const field = String(level.ratio);
      return typeof value === 'number' && y(value) >= top && y(value) <= bottom ? <g key={field}><line x1={left} x2={right} y1={y(value)} y2={y(value)} stroke={fibColor(level.ratio)} strokeDasharray="3 3"/><text x={right-4} y={y(value)-3} textAnchor="end" fill={fibColor(level.ratio)}>{(level.ratio*100).toFixed(1)}% {value.toFixed(2)}</text></g> : null;
    })}
    {layers.td && points.map((point,i) => {
      const buy = Number(point.td_buy_count), sell = Number(point.td_sell_count), num=buy || sell;
      if(!Number.isFinite(num)||num<4)return null;
      const cy=buy?Math.min(bottom-9,y(Number(point.low))+17):Math.max(top+10,y(Number(point.high))-14);
      const previous=points[i-1]; const firstNine=num===9 && Number(previous?.[buy?'td_buy_count':'td_sell_count'])!==9;
      const color=buy?C.buy:C.sell;
      return <g key={point.time}>{firstNine&&<rect x={x(i)-9} y={cy-11} width={18} height={16} rx={4} fill={buy?'#003325':'#450018'} stroke={buy?'#00e5ff':'#ff0055'}/>}<text x={x(i)} y={cy} fill={firstNine?'#ffffff':color} textAnchor="middle" fontWeight="bold">{num}</text></g>;
    })}
    {layers.signals && plans.flatMap(({field,markers}) => [...markers].map(([i,marker])=>{
      const launch=field==='sig_launch',point=points[i],color=launch?C.launch:C.escape;
      const cx=Math.max(left+26,Math.min(right-26,x(i)));
      const cy=launch?Math.min(bottom-8,y(Number(point.low))+31):Math.max(top+12,y(Number(point.high))-29);
      if(marker.role==='middle')return <path key={field+i} d={launch?`M ${cx} ${cy-5} l -3 6 h 6 Z`:`M ${cx} ${cy+1} l -3 -6 h 6 Z`} fill={color}/>;
      const label=marker.role==='last'?`${launch?'🚀':'⚠'}${marker.sequence}/${marker.count}`:`${launch?'🚀启动':'⚠逃顶'}${marker.role==='first'?'×'+marker.count:''}`;
      const w=Math.max(46,label.length*8);
      return <g key={field+i}><rect x={cx-w/2} y={cy-12} width={w} height={17} rx={3} fill={launch?'#2a0015':'#2a1e00'} stroke={color}/><text x={cx} y={cy} textAnchor="middle" fill={launch?'#ff66aa':'#ffdd66'}>{label}</text></g>;
    }))}
    {layers.signals && points.map((point,i)=>{
      const isTop=Number(point.sig_top)===1,isBottom=Number(point.sig_bottom)===1;
      if(!isTop&&!isBottom)return null;
      const field=isTop?'high':'low',signal=isTop?'sig_top':'sig_bottom',value=Number(point[field]);
      const nearby=points.slice(Math.max(0,i-5),i+6).filter(other=>Number(other[signal])===1).map(other=>Number(other[field]));
      if(isTop?value<Math.max(...nearby):value>Math.min(...nearby))return null;
      return <text key={'extreme'+point.time} x={x(i)} y={isTop?Math.max(top+10,y(value)-7):Math.min(bottom-2,y(value)+12)} fill={isTop?'#ff7777':'#00ffff'} textAnchor="middle">{value.toFixed(2)}</text>;
    })}

    {layers.signals && strategy.is_matched && <>
      {(['entry_price','stop_loss','target_price_1','target_price_2'] as const).map(field => {
        const value = strategy[field];
        if (!value || y(value) < top || y(value) > bottom) return null;
        const label = {entry_price:'介入',stop_loss:'止损',target_price_1:'目标1',target_price_2:'目标2'}[field];
        const color=field==='stop_loss'?'#ff4444':field==='entry_price'?'#00ff88':'#00ffff';
        return <g key={field}><line x1={left} x2={right} y1={y(value)} y2={y(value)} stroke={color} strokeDasharray="6 3"/><text x={left+4} y={y(value)-3} fill={color}>{label}:{value.toFixed(2)}</text></g>;
      })}
      {strategy.pioneer_sig_indices?.filter(i=>i>=0&&i<points.length).map(i=><g key={i}><line x1={x(i)} x2={x(i)} y1={top} y2={bottom} stroke="#00ff88" strokeDasharray="5 3"/><text x={Math.min(right-60,x(i)+3)} y={top+12} fill="#00ff88">逆势先锋</text></g>)}
    </>}
  </g>;
}
