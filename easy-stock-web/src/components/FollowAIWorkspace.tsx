import {AutomaticResearchSettingsPanel} from './AutomaticResearchSettingsPanel';
import {AIReviewPanel} from './AIReviewPanel';
import {useState} from 'react';
import {BackendConfig,requestJSON} from '../lib/backend';
import {FollowData,followCommandID} from '../lib/auto-follow';

export type AIPlan={symbol:string;action:string;thesis:string;entry_low:number|null;entry_high:number|null;invalidation:string;exit_response:string;evidence_refs:string[];missing_data:string[]};
export type AIResult={proposal?:{market_view:string;rotation_view:string;answer:string;plans:AIPlan[]};thread_id?:string;model?:string;provider?:string;error_code?:string;reasoning_effort?:string};
export type AISettings={enabled:boolean;mode:'shadow'|'enforce';node_id:string;workspace_id:string;model?:string;reasoning_effort?:string};
export type AIModel={id:string;name:string;efforts:string[];default_effort:string};
export type AIState={desktop_detail?:{models?:AIModel[];effective_model?:string;effective_effort?:string;elapsed_seconds?:number;error_code?:string;retry_after_seconds?:number;desktop_runtime?:{status?:string;data_root?:string;ats_pid?:number;tk_pid?:number}};desktop_online?:boolean;desktop_status?:string;desktop_heartbeat?:string;jobs:Array<{id:string;status:string;created_at:string;question:string;evidence_hash:string;result?:AIResult}>};
const time=(s?:string)=>s?new Date(s).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'尚无记录';
const labels:Record<string,string>={queued:'等待桌面领取',running:'Codex 分析中',completed:'提案已回传',failed:'分析失败',expired:'任务已过期',invalid_result:'提案未通过校验'};
export function FollowAIWorkspace({data,config,onChanged}:{data:FollowData|null;config:BackendConfig|null;onChanged:()=>void}){
 const [automaticEnabled,setAutomaticEnabled]=useState(false),[draft,setDraft]=useState<AISettings|null>(null),[question,setQuestion]=useState(''),[busy,setBusy]=useState(false),[notice,setNotice]=useState(''),[pendingID,setPendingID]=useState('');
 const state=data?.state,settings=draft||state?.ai_settings||{enabled:false,mode:'shadow' as const,node_id:'',workspace_id:''},ai=state?.ai;
 const latest=state?.ai_latest,result=latest?.result?.proposal;
 const selectedModel=settings.model||'gpt-6-luna',selectedEffort=settings.reasoning_effort||'medium';
 const receipt=data?.commands.find(c=>c.id===pendingID),jobID=receipt?.result?.match(/ai_job=([a-f0-9]{32})/)?.[1],job=ai?.jobs?.find(j=>j.id===jobID);
 const receiptText=pendingID?commandNotice(receipt,job):notice;

 const submit=async(action:string,extra:object)=>{if(!config)return;setBusy(true);try{const id=followCommandID();await requestJSON(config,'/api/v1/auto-follow/commands',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({id,action,...extra})});setPendingID(id);setNotice('');setDraft(null);onChanged()}catch(e){setPendingID('');setNotice(e instanceof Error?e.message:'AI 指令失败')}finally{setBusy(false)}};
 const disabled=busy||!data?.can_control;
 return <div className="follow-research"><AutomaticResearchSettingsPanel config={config} open onEnabledChange={setAutomaticEnabled}/><section className="follow-card"><h3>模拟策略提案采纳</h3><p>自动投研开关、频率、预算和系统模型在“系统设置 → 自动 AI 投研”统一管理，上方为同一设置入口。桌面代理：{ai?.desktop_online?'在线':'未连接'} · {ai?.desktop_status||'尚无心跳'}。此处仅保留既有模拟提案采纳模式。</p><label>提案采纳<select aria-label="AI提案采纳模式" value={settings.mode} onChange={e=>setDraft({...settings,mode:e.target.value as AISettings['mode']})}><option value="shadow">Shadow：记录提案与原策略对照</option><option value="enforce">Enforce：新开仓还需有效 AI 提案</option></select></label><button disabled={disabled||!draft||!settings.node_id} onClick={()=>void submit('ai_settings',{ai_settings:commandAISettings(settings,selectedModel,selectedEffort)})}>保存提案采纳模式</button><p>此模式不因自动研究的默认关闭而改变；AI 不直接下单，持仓退出继续使用原风控。</p>{receiptText&&<p role="status" className="follow-notice">{receiptText}</p>}</section>
 <section className="follow-card"><h3>与策略 AI 互动</h3><p>提问发送当前池、市场/资金证据与模拟账户摘要，代理可按需查询限定的 ATS IPC 证据；结果保留任务 ID、证据哈希和 Codex thread ID，每轮使用独立研究会话并保留任务关联，避免重复累积历史市场上下文。</p><textarea aria-label="策略AI问题" value={question} maxLength={4000} onChange={e=>setQuestion(e.target.value)} placeholder="例如：资金热点是否扩散？逐股比较板块地位、VWAP结构和失效条件，给出观察优先级。" rows={4} style={{width:'100%',boxSizing:'border-box'}}/><button disabled={disabled||!automaticEnabled||!question.trim()} onClick={()=>void submit('ai_ask',{question})}>发送给系统模型</button><small>此策略问题遵守自动投研开关、系统模型、频率及预算；普通手动 AI 对话不受自动开关影响。失败不伪装成功，回答不直接转成订单。</small></section>
 <section className="follow-card"><h3>AI 策略提案与采纳证据</h3>{result?<><p>证据时间 {time(latest?.as_of)} · 模式 {state?.ai_settings?.mode||'shadow'} · {latest?.result?.model} / {latest?.result?.reasoning_effort||'未记录强度'} / {latest?.result?.provider}</p><small>任务 {latest?.id} · Codex 会话 {latest?.result?.thread_id} · 证据 {latest?.evidence_hash}</small><h4>市场与资金轮动判断</h4><p style={{whiteSpace:'pre-wrap'}}>{result.market_view}</p><p style={{whiteSpace:'pre-wrap'}}>{result.rotation_view}</p><h4>互动回答</h4><p style={{whiteSpace:'pre-wrap'}}>{result.answer}</p>{result.plans.map(p=><details className="follow-plan" key={p.symbol}><summary><strong>{state?.candidates?.find(c=>c.symbol===p.symbol)?.name||p.symbol} · {p.symbol}</strong><span>{p.action==='follow'?'条件跟随提案':p.action==='avoid'?'回避提案':'观察提案'}</span></summary><div className="follow-plan-body"><p>{p.thesis}</p><p>AI 条件区间：{p.entry_low==null?'未提供':`${p.entry_low} ～ ${p.entry_high}`}</p><p>失效条件：{p.invalidation}</p><p>持仓应对意见：{p.exit_response}</p><p>引用证据：{p.evidence_refs.join('、')||'无有效引用'}</p><p>数据缺口：{p.missing_data.join('；')||'模型未列出；仍由执行层独立校验'}</p><p>采纳：{state?.ai_settings?.mode==='enforce'?'等待原结构、市场与风控复核；通过后在成交记录绑定任务 ID':'Shadow 对照记录，不改变原开仓决定'}</p></div></details>)}</>:<p>尚无通过校验的 AI 提案。固定条件计划与 AI 研究明确分开，不以模板内容冒充模型结果。</p>}</section>
 <AIReviewPanel data={data}/><section className="follow-card"><h3>分析任务与回传</h3>{ai?.jobs?.map(j=><article className="follow-plan" key={j.id}><strong>{labels[j.status]||j.status}</strong><p>{j.question||'自动市场 / 题材 / 个股策略研究'}</p><small>{time(j.created_at)} · {j.id} · {j.result?.error_code||j.result?.thread_id||'等待回传'}</small>{j.status==='completed'&&j.result?.proposal&&<details><summary>查看本轮回答</summary><p style={{whiteSpace:'pre-wrap'}}>{j.result.proposal.answer}</p></details>}</article>)}{!ai?.jobs?.length&&<p>暂无分析任务。选择已登记桌面、启用研究并运行桌面代理后开始。</p>}</section></div>
}

export function commandNotice(receipt?:FollowData['commands'][number],job?:AIState['jobs'][number]):string{
 if(!receipt||receipt.status==='pending')return '指令已入队，等待后台接收。';
 if(receipt.status!=='applied')return `指令处理失败：${receipt.result||receipt.status}`;
 if(receipt.action!=='ai_ask')return '设置已由后台应用。';
 if(!job)return '后台已接收提问，等待任务状态刷新。';
 if(job.status==='completed')return '分析完成，提案已回传；交易采纳由执行层独立复核。';
 if(job.status==='failed')return `分析失败：${job.result?.error_code||'未知错误'}。可在桌面恢复就绪后重新提交。`;
 if(job.status==='expired')return '分析任务已过期，未用于交易。请重新提交以获取新证据。';
 if(job.status==='running')return '桌面 Codex 正在分析，完成或失败后会更新回执。';
 return labels[job.status]||job.status;
}

export function commandAISettings(settings:AISettings,model:string,effort:string):AISettings{
 return {enabled:settings.enabled,mode:settings.mode,node_id:settings.node_id,workspace_id:settings.workspace_id,model,reasoning_effort:effort};
}
