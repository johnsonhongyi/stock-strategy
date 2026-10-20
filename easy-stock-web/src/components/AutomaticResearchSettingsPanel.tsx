import {useCallback,useEffect,useRef,useState} from 'react';
import {requestJSON,type BackendConfig} from '../lib/backend';
import type {ChatRuntimeConfig} from '../lib/desktop-chat';

type Policy={enabled:boolean;interval_seconds:number;daily_max_runs:number;daily_token_budget:number;revision:number};
type RecordItem={id:string;created_at:string;started_at:string;status:string;model:string;reasoning_effort:string;stage:string;total_tokens:number;usage_known:boolean;error_code:string};
type Snapshot={policy:Policy;runtime:ChatRuntimeConfig;status:string;usage:{runs:number;total_tokens:number;unsettled_runs:number};records:RecordItem[]};
type Props={config:BackendConfig|null;open:boolean;onRegisterSave?:(save:(()=>Promise<boolean>)|null)=>void;onBusyChange?:(busy:boolean)=>void;onEnabledChange?:(enabled:boolean)=>void};
const labels:Record<string,string>={disabled:'已关闭',ready:'可运行',runtime_unavailable:'已暂停：系统运行时暂不支持自动投研',model_unavailable:'已暂停：所选桥、模型或推理强度不可用',usage_unsettled:'已暂停：存在未结算或用量未知的任务',budget_exhausted:'已暂停：今日预算已达到',interval_wait:'等待下一次调度',policy_unavailable:'已暂停：设置或用量不可读'};
const states:Record<string,string>={queued:'排队',running:'运行中',completed:'完成',failed:'失败',cancelled:'已取消',expired:'已过期'};

export function AutomaticResearchSettingsPanel({config,open,onRegisterSave,onBusyChange,onEnabledChange}:Props){
 const [snapshot,setSnapshot]=useState<Snapshot|null>(null),[draft,setDraft]=useState<Policy|null>(null),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false);
 useEffect(()=>{onEnabledChange?.(snapshot?.policy.enabled||false);},[snapshot,onEnabledChange]);
 const dirty=useRef(false),draftRef=useRef(draft);draftRef.current=draft;
 const refresh=useCallback(async()=>{if(!config)return;const {data}=await requestJSON<{data:Snapshot}>(config,'/api/v1/ai/auto-research');setSnapshot(data);if(!dirty.current)setDraft(data.policy);},[config]);
 useEffect(()=>{if(!open||!config)return;let live=true;const load=()=>{void requestJSON<{data:Snapshot}>(config,'/api/v1/ai/auto-research').then(({data})=>{if(live){setSnapshot(data);if(!dirty.current)setDraft(data.policy);}}).catch(e=>{if(live)setNotice(String(e));});};load();const timer=window.setInterval(load,15000);window.addEventListener('ai-runtime-changed',load);return()=>{live=false;clearInterval(timer);window.removeEventListener('ai-runtime-changed',load);};},[config,open]);
 const save=useCallback(async()=>{const current=draftRef.current;if(!config||!current||!dirty.current)return false;setBusy(true);onBusyChange?.(true);try{const {revision:_,...policy}=current;const {data}=await requestJSON<{data:Snapshot}>(config,'/api/v1/ai/auto-research',{method:'PUT',body:JSON.stringify(policy)});setSnapshot(data);setDraft(data.policy);dirty.current=false;setNotice('自动投研设置已保存；在途任务按原配置结束，新任务使用系统模型。');return true;}catch(e){setNotice(String(e));throw e;}finally{setBusy(false);onBusyChange?.(false);}},[config,onBusyChange]);
 useEffect(()=>{onRegisterSave?.(save);return()=>onRegisterSave?.(null);},[onRegisterSave,save]);
 function change<K extends keyof Policy>(key:K,value:Policy[K]){if(draft){dirty.current=true;setDraft({...draft,[key]:value});}}
 return <section className="settings-section" aria-label="自动 AI 投研设置">
  <h3>自动 AI 投研</h3><p>默认关闭。仅生成模拟策略研究提案，不改变实盘交易或风控。所有自动任务继承“统一 AI 运行时”，模型不可用时暂停，禁止自动换桥或降档。</p>
  {snapshot&&<><p role="status">{labels[snapshot.status]||snapshot.status}</p><p>系统模型：{snapshot.runtime.provider} · {snapshot.runtime.node_id||'未选桥'} · {snapshot.runtime.model||'未配置'} / {snapshot.runtime.reasoning_effort||'未配置'}</p></>}
  {draft&&<><label><input type="checkbox" aria-label="开启自动 AI 投研" checked={draft.enabled} disabled={busy} onChange={e=>change('enabled',e.target.checked)}/> 开启自动 AI 投研</label>
   <label>最短间隔（秒）<input type="number" aria-label="自动投研最短间隔" min={300} max={86400} step={60} value={draft.interval_seconds} disabled={busy} onChange={e=>change('interval_seconds',Number(e.target.value))}/></label>
   <label>每日最多调用次数<input type="number" aria-label="自动投研每日次数" min={1} max={100} value={draft.daily_max_runs} disabled={busy} onChange={e=>change('daily_max_runs',Number(e.target.value))}/></label>
   <label>每日 Token 预算<input type="number" aria-label="自动投研每日Token预算" min={1000} max={10000000} step={1000} value={draft.daily_token_budget} disabled={busy} onChange={e=>change('daily_token_budget',Number(e.target.value))}/></label>
   <p>次数包含失败尝试；复盘和事件触发也遵守最短间隔。Token 按提供方上报统计（含缓存输入），在途请求可能超额；用量未知或未结算时暂停后续任务，不自动重试。</p>
   <button type="button" disabled={busy} onClick={()=>void save().catch(()=>undefined)}>保存自动投研设置</button></>}
  {snapshot&&<><p>今日调用 {snapshot.usage.runs} / {snapshot.policy.daily_max_runs}；已报告 {snapshot.usage.total_tokens.toLocaleString()} / {snapshot.policy.daily_token_budget.toLocaleString()} Token；未结算 {snapshot.usage.unsettled_runs} 次。按北京时间每日统计，新版启用前的历史记录可能未计量。</p>
   <h4>最近运行记录</h4><button type="button" disabled={busy} onClick={()=>void refresh().catch(e=>setNotice(String(e)))}>刷新用量与记录</button>
   {snapshot.records.length?<ul>{snapshot.records.map(record=><li key={record.id}><strong>{states[record.status]||record.status}</strong> · <small title={record.id}>{record.id.slice(0,8)}</small> · {new Date(record.created_at).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai'})}<br/>{record.model||'历史模型未记录'} / {record.reasoning_effort||'未记录'} · {record.usage_known?`${record.total_tokens.toLocaleString()} Token`:record.started_at?'用量未确认':'未调用或历史未计量'}{record.error_code?` · ${record.error_code}`:''}</li>)}</ul>:<p>尚无自动投研记录。</p>}</>}
  {notice&&<p role="status">{notice}</p>}
 </section>;
}
