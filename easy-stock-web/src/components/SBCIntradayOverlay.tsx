import type { KLine } from '../lib/backend';
import type { SBCAnalysis } from '../lib/sbc';
import type { SBCLayers } from './SBCOverlay';
import { SBC_COLORS as C } from '../lib/sbc-style';

export function SBCIntradayOverlay({lines,reference,layers,y,left,right,top,bottom}:{lines:KLine[];reference?:SBCAnalysis;layers:SBCLayers;y:(price:number)=>number;left:number;right:number;top:number;bottom:number}) {
 const first=lines[0],last=lines.at(-1);if(!first||!last)return null;
 const point=reference?.points.at(-1);
 const entries:[string,number,string,string][]=[
  ['开盘',first.open,C.up,'6 3'],['现价',last.close,last.close>=(first.previous_close||first.open)?C.up:C.intradayDown,'6 3'],
  ['VWAP'+(last.vwap_estimated?'（估算）':''),last.vwap||0,C.vwap,'6 3'],
 ];
 if(layers.channel&&point) entries.push(['上轨',Number(point.ch_upper),'#ffb454','6 3'],['反转',Number(point.reversal_line),C.reversal,'8 3 2 3'],['支撑',Number(point.ch_supp_price),C.support,'6 3']);
 if(layers.signals&&reference?.strategy.is_matched) entries.push(['目标',reference.strategy.target_price_1||0,'#00ff88','8 3 2 3']);
 const occupied:number[]=[];
 return <g pointerEvents="none" fontSize="11">{entries.map(([label,value,color,dash])=>{
  const cy=y(value);if(!Number.isFinite(cy)||value<=0||cy<top||cy>bottom)return null;
  let labelY=cy;while(occupied.some(other=>Math.abs(other-labelY)<15)&&labelY<bottom-15)labelY+=15;
  if(occupied.some(other=>Math.abs(other-labelY)<15)){labelY=cy;while(occupied.some(other=>Math.abs(other-labelY)<15)&&labelY>top+15)labelY-=15;}
  occupied.push(labelY);
  return <g key={label}><line x1={left} x2={right} y1={cy} y2={cy} stroke={color} strokeDasharray={dash} strokeWidth={label==='支撑'?1.6:1}/><text x={right+5} y={labelY+4} fill={color}>{label}:{value.toFixed(2)}</text></g>;
 })}</g>;
}
