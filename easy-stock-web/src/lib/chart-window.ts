export type ChartWindowSize = {width:number; height:number};
const key='sbc-chart-window-size-v1';
export function clampChartWindowSize(size:ChartWindowSize, width=window.innerWidth,height=window.innerHeight):ChartWindowSize {
  return {width:Math.min(Math.max(360,Number.isFinite(size.width)?size.width:1200),Math.max(240,width-24)),height:Math.min(Math.max(360,Number.isFinite(size.height)?size.height:800),Math.max(240,height-24))};
}
export function readChartWindowSize():ChartWindowSize {
  try {return clampChartWindowSize(JSON.parse(localStorage.getItem(key)||'{"width":1200,"height":800}'));}
  catch {return clampChartWindowSize({width:1200,height:800});}
}
export function saveChartWindowSize(size:ChartWindowSize) {
  try {localStorage.setItem(key,JSON.stringify(clampChartWindowSize(size)));} catch { /* Storage may be disabled; resizing still works. */ }
}
