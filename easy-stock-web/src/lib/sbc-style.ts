export const SBC_COLORS = {
  up:'#ff4444', down:'#00ffff', intradayDown:'#00ff88', price:'#00b4d8', vwap:'#ffd700',
  ma5:'#ffd700', ma20:'#ff00ff', upper:'#ffffff', middle:'#c0c0d0', lower:'#ffffff',
  support:'#9debff', reversal:'#f06ad7', klineReversal:'#ffcc00', channelSupport:'#ffd700',
  launch:'#ff007f', escape:'#ffaa00', buy:'#00ff88', sell:'#ff77aa',
} as const;
export function fibColor(ratio:number) {
 return ratio===.809?'#ff7700':ratio===.5?'#00e5ff':ratio===.191?'#00ff88':'#ffd700';
}
// ATS groups nearby launch/escape signals with a maximum gap of four bars.
export function clusterSignalMarkers(indices:number[]) {
 const result=new Map<number,{role:'single'|'first'|'middle'|'last';count:number;sequence:number}>();
 let start=0;
 for(let end=0;end<indices.length;end++) {
  if(end+1<indices.length&&indices[end+1]-indices[end]<=4)continue;
  const count=end-start+1;
  for(let i=start;i<=end;i++)result.set(indices[i],{role:count===1?'single':i===start?'first':i===end?'last':'middle',count,sequence:i-start+1});
  start=end+1;
 }
 return result;
}
