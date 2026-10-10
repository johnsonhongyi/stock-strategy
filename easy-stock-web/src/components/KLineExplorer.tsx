import { useEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { requestJSON, type KLine } from '../lib/backend';
import type { KLineChartProps } from './KLineChart';
import { latestTradingDayKLines } from '../lib/kline';
import { SBCRecoveryGate, recoverSBCBars } from '../lib/sbc-recovery';
import { readChartWindowSize, saveChartWindowSize } from '../lib/chart-window';

const periods = [
  ['分时','1','intraday'], ['3日分时','1','intraday'], ['5日分时','1','intraday'], ['5分','5','daily'], ['15分','15','daily'], ['30分','30','daily'], ['60分','60','daily'],
  ['日K','day','daily'], ['2D','2d','daily'], ['3D','3d','daily'], ['周K','week','daily'], ['月K','month','daily'],
] as const;

export function KLineExplorer(props: KLineChartProps & {render: (props: KLineChartProps) => ReactNode}) {
  const [open,setOpen] = useState(false);
  const [maximized,setMaximized] = useState(false);
  const [size,setSize] = useState(readChartWindowSize);
  const [reference,setReference] = useState<{key:string; lines:KLine[]}|null>(null);
  const [period,setPeriod] = useState('');
  const [retry,setRetry] = useState(0);
  const [recovering,setRecovering] = useState('');
  const recovery = useRef(new SBCRecoveryGate());
  const [loaded,setLoaded] = useState<{key:string; lines:KLine[]; state:'ready'|'error'; error?:string} | null>(null);
  const panel = useRef<HTMLElement>(null);
  const navigate = useRef(props.onNavigate); navigate.current=props.onNavigate;
  const measured = useRef(size);
  const opener = useRef<HTMLButtonElement>(null);
  const symbol = props.symbol || props.lines[0]?.symbol || '';
  const label = period || props.periodLabel || '日K';
  const active = periods.find(item=>item[0]===label);
  const key = JSON.stringify([props.config?.backendUrl,props.config?.token,symbol,label,props.market,retry]);
  const recoveryKey=JSON.stringify([props.config?.backendUrl,symbol,label,props.market]);
  const needsFetch = Boolean((period || recovering===recoveryKey) && symbol && props.config);
  const recover=()=>{if(props.config && symbol && recovery.current.claim(recoveryKey)){setRecovering(recoveryKey);setRetry(value=>value+1);}};
  useEffect(()=>{if(props.state==='ready'&&!props.lines.length)recover();},[recoveryKey,props.state,props.lines.length]);
  useEffect(()=>{
    if(!needsFetch || !props.config || !active)return;
    const abort = new AbortController();
    const multiDays=label==='3日分时'?3:label==='5日分时'?5:0;
    const request=multiDays
      ? requestJSON<{data:KLine[]}>(props.config,`/api/v1/quotes/tdx/bars?symbol=${encodeURIComponent(symbol)}&period=1&limit=${multiDays*240+80}&days=${multiDays}`,{signal:abort.signal})
      : recovering===recoveryKey
      ? recoverSBCBars(props.config,symbol,props.market,active[1],abort.signal)
      : requestJSON<{data:KLine[]}>(props.config,`/api/v1/quotes/kline?symbol=${encodeURIComponent(symbol)}&period=${active[1]}&limit=240${props.market?`&market=${encodeURIComponent(props.market)}`:''}`,{signal:abort.signal});
    request.then(result=>{
      if(abort.signal.aborted)return;
      const dates=[...new Set((result.data||[]).map(line=>line.time.slice(0,10)))].sort().slice(-multiDays);
      const lines=multiDays?(result.data||[]).filter(line=>dates.includes(line.time.slice(0,10))):active[1]==='1'?latestTradingDayKLines(result.data || []):result.data || [];
      if(recovering===recoveryKey && active[1]==='day')props.onRecovered?.(lines);
      setLoaded({key,lines,state:lines.length?'ready':'error',error:lines.length?undefined:'数据源未返回该周期行情，请重试或切换周期'});
    },error=>{if(!abort.signal.aborted)setLoaded({key,lines:[],state:'error',error:error instanceof Error?error.message:'行情加载失败'});});
    return ()=>abort.abort();
  },[key,needsFetch]);
  const referenceKey=JSON.stringify([props.config?.backendUrl,props.config?.token,symbol,props.market]);
  const intraday=(active?.[2] || props.mode)==='intraday';
  useEffect(()=>{
    if(!intraday || !props.config || !symbol)return;
    const abort=new AbortController();
    requestJSON<{data:KLine[]}>(props.config,`/api/v1/quotes/kline?symbol=${encodeURIComponent(symbol)}&period=day&limit=240${props.market?`&market=${encodeURIComponent(props.market)}`:''}`,{signal:abort.signal}).then(result=>{if(!abort.signal.aborted)setReference({key:referenceKey,lines:result.data||[]});},()=>{if(!abort.signal.aborted)setReference({key:referenceKey,lines:[]});});
    return ()=>abort.abort();
  },[referenceKey,intraday]);
  useEffect(()=>{
    if(!open)return;
    const previous=document.body.style.overflow; document.body.style.overflow='hidden';
    panel.current?.focus();
    const keyboard=(event:KeyboardEvent)=>{
      if(event.key==='Escape'){setOpen(false);return;}
      if((event.target as HTMLElement)?.matches?.('input,select,textarea'))return;
      if(event.key==='ArrowUp'||event.key==='ArrowDown'){event.preventDefault();navigate.current?.(event.key==='ArrowUp'?-1:1);}
      if(event.key==='Tab' && panel.current){const nodes=Array.from(panel.current.querySelectorAll<HTMLElement>('button:not(:disabled),input,select,[tabindex="0"]'));const first=nodes[0],last=nodes.at(-1);if(event.shiftKey&&(document.activeElement===first||document.activeElement===panel.current)){event.preventDefault();last?.focus();}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus();}}
    };
    window.addEventListener('keydown',keyboard);
    return ()=>{document.body.style.overflow=previous;window.removeEventListener('keydown',keyboard);opener.current?.focus();};
  },[open]);
  useEffect(()=>{
    const element=panel.current;if(!open||!element||maximized)return;
    const observer=new ResizeObserver(entries=>{const rect=entries[0].target.getBoundingClientRect();measured.current={width:rect.width,height:rect.height};saveChartWindowSize(measured.current);});
    observer.observe(element);return ()=>{setSize(measured.current);observer.disconnect();};
  },[open,maximized]);
  const current=loaded?.key===key?loaded:null;
  const chartProps={...props,referenceLines:intraday?(reference?.key===referenceKey?reference.lines:[]):undefined,onInvalidData:recover,lines:needsFetch?current?.lines || []:props.lines,state:needsFetch?current?.state || 'loading' as const:props.state,periodLabel:label,mode:active?.[2] || props.mode,compact:open?false:props.compact};
  const openChart=()=>{setSize(readChartWindowSize());setOpen(true);};
  const controls=<div className="kline-explorer-toolbar">
    <div className="kline-period-controls">{periods.map(item=><button type="button" disabled={!symbol||!props.config} key={item[0]} className={label===item[0]?'active':''} onClick={()=>setPeriod(item[0])}>{item[0]}</button>)}</div>
    <div className="kline-window-actions">{props.onNavigate&&<><button type="button" onClick={()=>props.onNavigate?.(-1)} aria-label="上一只股票">↑</button><button type="button" onClick={()=>props.onNavigate?.(1)} aria-label="下一只股票">↓</button></>}{open?<><button type="button" onClick={()=>setMaximized(value=>!value)}>{maximized?'还原':'最大化'}</button><button type="button" onClick={()=>setOpen(false)} aria-label="关闭放大窗口">关闭 ✕</button></>:<button ref={opener} type="button" onClick={openChart} aria-label="放大 K 线图">⛶ 放大</button>}</div>
  </div>;
  const body=<>{controls}{recovering===recoveryKey && !current && <p className="load-notice" role="status">检测到缓存行情异常，正在自动更新一次…</p>}{!needsFetch && props.state==='error' && <p className="load-notice">行情加载失败 <button type="button" onClick={()=>{setPeriod(label);setRetry(value=>value+1);}}>重试行情</button></p>}{current?.error&&<p className="load-notice" role="alert">{current.error} <button type="button" onClick={()=>setRetry(value=>value+1)}>重试</button></p>}{props.render(chartProps)}</>;
  return <div className="kline-explorer" tabIndex={0} onDoubleClick={event=>{if(!open && (event.target as Element).closest('.kline-chart')){event.preventDefault();openChart();}}} onMouseDown={event=>{if(open)return;if(!(event.target as HTMLElement).closest('button,input,select'))event.currentTarget.focus({preventScroll:true});}} onKeyDown={event=>{if(open || (event.target as HTMLElement).matches('input,select,textarea'))return;if(event.key==='ArrowUp'||event.key==='ArrowDown'){event.preventDefault();navigate.current?.(event.key==='ArrowUp'?-1:1);}}}>
    {!open?body:<><div className="kline-window-launcher"><button ref={opener} type="button" onClick={()=>panel.current?.focus()}>⛶ 放大窗口已打开</button></div>{createPortal(<div className="sbc-window-backdrop" onMouseDown={event=>{if(event.target===event.currentTarget)setOpen(false);}}><section ref={panel} tabIndex={-1} className={`sbc-chart-window${maximized?' maximized':''}`} style={maximized?undefined:{width:size.width,height:size.height}} role="dialog" aria-modal="true" aria-label={`${props.title || symbol} SBC 放大图`}><header><strong>{props.title || symbol || 'K线'} · {symbol} · {label}</strong><span>拖动右下角调整大小 · 自动保存 · ↑↓切股 · Esc关闭</span></header>{body}</section></div>,document.body)}</>}
  </div>;
}
