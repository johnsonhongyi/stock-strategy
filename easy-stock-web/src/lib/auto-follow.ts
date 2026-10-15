import type {AISettings,AIState,AIResult} from '../components/FollowAIWorkspace';
import type {FollowPlan,FollowResearch} from './follow-research';
export type FollowStock = {ai_decision?:{mode?:string;status?:string;job_id?:string;evidence_hash?:string;thread_id?:string};plan?:FollowPlan;symbol:string;name?:string;route:string;theme?:string;streak?:number;status?:string;reason?:string;price?:number;change_percent?:number;checked_at?:string;source?:string;trade_time?:string;shares?:number;buy_price?:number;buy_date?:string;last_price?:number;last_price_at?:string;sell_date?:string;sell_price?:number;pnl?:number};
export type FollowSettings = {limit_up:boolean;theme_trend:boolean;theme_ids:string[]};
export type FollowEvent = {at:string;action:string;symbol?:string;reason?:string;route?:string;price?:number;shares?:number;pnl?:number;command?:string;status?:string;result?:string;decision?:{status?:string;reason?:string;job_id?:string;would_admit?:boolean}};
export type FollowData = {available:boolean;online:boolean;can_control:boolean;heartbeat_age_seconds?:number;summary?:{realized_pnl:number;closed_count:number;winning_count:number};commands:Array<{id:string;action:string;created_at:string;status:string;applied_at:string;result:string}>;state:null|{mode:string;version:string;engine_revision?:string;ai_settings?:AISettings;ai?:AIState;ai_latest?:{id:string;as_of:string;evidence_hash:string;result:AIResult};ai_review?:{id:string;as_of:string;evidence_hash:string;workflow_stage?:string;result:AIResult};research?:FollowResearch;runtime_phase?:string;cash:number;initial_cash:number;enabled?:boolean;kill:boolean;last_heartbeat?:string;cycle_finished_at?:string;cycle_status?:string;last_scan_at?:string;settings?:FollowSettings;market?:{backend_status?:string;risk_score?:number;score?:number;checked_at?:string};risk_config?:{strategy_version:string;max_positions:number;amount_per_trade:number;max_day_chg:number;take_profit:number;stop_loss:number;daily_loss_limit:number};themes?:Array<{id:string;name:string;change_percent?:number;rising_nodes?:number;falling_nodes?:number;snapshot_at?:string;source?:string}>;candidates?:FollowStock[];positions:FollowStock[];closed:FollowStock[];events:FollowEvent[]}};

export const defaultFollowSettings:FollowSettings={limit_up:true,theme_trend:true,theme_ids:[]};
export const routeName=(route?:string)=>route==='limit_up'?'短线连板':route==='theme_trend'?'趋势题材':route||'—';
export const cycleLabels:Record<string,string>={outside_session:'等待交易时段',completed:'本轮扫描完成',paused:'已暂停新开仓',kill_switch:'风控停止开仓',market_gate:'市场门禁拦截',degraded:'数据异常，已降级',kill_updated:'风控开关已更新'};
const reasons:Record<string,string>={already_held_or_traded_today:'已有持仓或今日已交易',max_positions:'已达持仓上限',insufficient_cash:'可用资金不足',pending_stop_command:'停止指令待生效，已阻止买入',P8_P25_confirmed:'P8/P25 确认通过',kill_switch:'硬风控停止开仓',market_gate:'市场情绪门禁拦截','entry_paused; exits remain active':'暂停新买，持仓退出继续监控','P4 change gate':'涨幅超出入场范围','P25 trend gate':'多日 VWAP / MA20 趋势未通过','P8 VWAP gate':'VWAP 站位或偏离未通过','P8 volume-price gate':'量价齐升未确认','intraday pullback veto':'冲高回落，不追买','opening trap':'高开低走，诱多否决','intraday bars unavailable':'当日分钟数据不足或过期','source unavailable or stale':'来源不可用或行情陈旧','previous completed trading day unavailable':'上一交易日日线未补齐','exact VWAP unavailable':'无法验证精确 VWAP','volume unit unavailable':'成交量单位不可验证','quote trade_time expired':'报价时间已过期','take_profit':'止盈','stop_loss':'止损','vwap_stop':'VWAP 止损','break_ma10':'十日 VWAP 破位','time_stop':'持仓时间止损','limit-down liquidity unverified':'跌停流动性无法确认，未假定成交'};
export const reasonName=(reason?:string)=>reason?(reasons[reason]||reason):'等待结构确认';
export function accountMetrics(data:FollowData|null,now=Date.now()){
 const state=data?.state;if(!state)return null;
 let value=0,floating=0,stale=false;
 for(const p of state.positions){
  const price=Number.isFinite(p.last_price)&&Number(p.last_price)>0?Number(p.last_price):Number(p.buy_price);
  if(!Number.isFinite(price)||!Number.isFinite(p.shares)||!Number.isFinite(p.buy_price)){stale=true;continue}
  value+=price*Number(p.shares);floating+=(price-Number(p.buy_price))*Number(p.shares);
  const age=now-Date.parse(p.last_price_at||'');if(!Number.isFinite(age)||age<0||age>120000)stale=true;
 }
 return {value,floating,equity:state.cash+value,stale,realized:data?.summary?.realized_pnl||0,
  winRate:data?.summary?.closed_count?100*data.summary.winning_count/data.summary.closed_count:null};
}

export function followCommandID():string {
 const crypto=globalThis.crypto;
 if(typeof crypto?.randomUUID==='function')return crypto.randomUUID();
 if(typeof crypto?.getRandomValues==='function')return Array.from(crypto.getRandomValues(new Uint8Array(16)),v=>v.toString(16).padStart(2,'0')).join('');
 return `follow-${Date.now().toString(36)}-${Math.random().toString(36).slice(2,14)}`;
}
