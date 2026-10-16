import {requestJSON,type BackendConfig} from './backend';
import type {HermesStreamRequest,HermesStreamResult} from './hermes';
import {bridgeTargetLabel} from './linkage-display';
import {streamTaskSnapshots} from './task-events';

export type ChatRuntimeConfig={provider:'hermes'|'codex_desktop';node_id:string;workspace_id:string;model:string;reasoning_effort:string};
export type DesktopChatCapability='ai_chat_v1'|'stock_research_v1';
export type ChatRuntime={config:ChatRuntimeConfig;online:boolean;models:Array<{id:string;name:string;efforts:string[]}>;message:string};
export type AvailableDesktopChatTarget={node_id:string;workspace_id:string;label:string;runtime:ChatRuntime};
export async function loadChatRuntime(config:BackendConfig,node_id='',workspace_id='',capability:DesktopChatCapability='ai_chat_v1'){
 const params=new URLSearchParams();if(node_id){params.set('node_id',node_id);params.set('workspace_id',workspace_id);}if(capability!=='ai_chat_v1')params.set('capability',capability);
 const query=params.toString();
 return (await requestChatJSON<{data:ChatRuntime}>(config,'/api/v1/ai/runtime'+(query?'?'+query:''))).data;
}
export async function loadAvailableDesktopChatTargets(config:BackendConfig,capability:DesktopChatCapability='ai_chat_v1'){
 const result=await requestChatJSON<{targets:Array<{node_id:string;workspace_id:string;label:string}>;default_node_id?:string;strategy_target?:{node_id:string;workspace_id:string}}>(config,'/api/v1/linkage/targets');
 const preferredWorkspaceID=result.targets.find(target=>target.node_id===result.default_node_id)?.workspace_id||'';
 const probed=await Promise.all(result.targets.map(async target=>{
  try{
   const runtime=await loadChatRuntime(config,target.node_id,target.workspace_id,capability);
   const usable=runtime.online&&runtime.models.some(model=>model.id&&model.efforts.length>0);
   return {target,runtime,usable};
  }catch{return {target,runtime:null,usable:false};}
 }));
 const ready=probed.flatMap(entry=>entry.usable&&entry.runtime?[{...entry.target,runtime:entry.runtime}]:[]);
 const targets=[...ready].sort((a,b)=>Number(b.workspace_id===preferredWorkspaceID)-Number(a.workspace_id===preferredWorkspaceID));
 return {targets,preferredWorkspaceID,strategyTarget:result.strategy_target||null,otherAvailableCount:0};
}
export async function prepareDesktopChatRuntime(config: BackendConfig, current: ChatRuntimeConfig): Promise<ChatRuntimeConfig> {
 const available=await loadAvailableDesktopChatTargets(config);
 const candidates=[...available.targets].sort((a,b)=>
  Number(b.node_id===current.node_id&&b.workspace_id===current.workspace_id)-Number(a.node_id===current.node_id&&a.workspace_id===current.workspace_id)
  || Number(b.workspace_id===available.preferredWorkspaceID)-Number(a.workspace_id===available.preferredWorkspaceID));
 const target=candidates[0];
 if(target){
  const model=target.runtime.models.find(model=>model.id===current.model)||target.runtime.models[0];
  return {...current,provider:'codex_desktop',node_id:target.node_id,workspace_id:target.workspace_id,model:model.id,reasoning_effort:model.efforts.includes(current.reasoning_effort)?current.reasoning_effort:model.efforts[0]};
 }
 if(available.preferredWorkspaceID){
  throw new Error('默认 ATS 工作区 '+available.preferredWorkspaceID+' 没有在线且有可用模型的 Codex 桥'+(available.otherAvailableCount?'；其他工作区的桥已忽略':'')+'。');
 }
 throw new Error('没有支持 AI 对话的在线 Codex 桌面，请检查桌面桥状态。');
}
export async function saveChatRuntime(config:BackendConfig,value:ChatRuntimeConfig){
 return (await requestChatJSON<{data:ChatRuntime}>(config,'/api/v1/ai/runtime',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(value)})).data;
}
type ChatJob={id:string;session_id:string;status:string;stream_events?:boolean;result?:{content?:string;thread_id?:string;error_code?:string;model?:string;reasoning_effort?:string;stage?:string;elapsed_seconds?:number}};
const bridgeStages:Record<string,string>={app_server_start:'正在启动本机 Codex 引擎…',project_setup:'正在准备 Codex 工作区…',thread_resume:'正在恢复 Codex 会话…',thread_start:'正在创建 Codex 会话…',thread_label:'正在标记检测会话…',turn_start:'Codex 已就绪，正在提交检测请求…',codex_reply:'Codex 正在生成检测回执…',result_cache:'Codex 已回复，正在保存本机回执…',result_writeback:'本机已保存回执，正在写回 Web 后端…'};
export async function testDesktopChatBridge(config:BackendConfig,target:AvailableDesktopChatTarget,modelID:string,effort:string,onStatus:(message:string)=>void){
 let job:ChatJob|undefined;
 const startedAt=Date.now();let lastStatus='';
 let cancelPromise:Promise<boolean>|undefined;const cancel=()=>{if(!job)return Promise.resolve(false);if(!cancelPromise)cancelPromise=requestChatJSON<unknown>(config,'/api/v1/ai/desktop/jobs/'+job.id,{method:'DELETE'}).then(()=>true).catch(error=>typeof error==='object'&&error!==null&&'status'in error&&Number((error as {status?:unknown}).status)===404);return cancelPromise;};
 const cleanup=async()=>{if(job)await requestChatJSON<unknown>(config,'/api/v1/ai/desktop/jobs/'+job.id,{method:'DELETE'}).catch(()=>undefined);};
 try{
  onStatus('正在复核所选桥的心跳与模型目录…');
  const runtime=await loadChatRuntime(config,target.node_id,target.workspace_id);
  const model=runtime.models.find(item=>item.id===modelID);
  if(!runtime.online||!model?.id||!model.efforts.includes(effort))throw new Error('所选桥、模型或推理强度当前不可用。');
  onStatus('桥在线，正在提交一次轻量 Codex 往返检测…');
  job=(await requestChatJSON<{data:ChatJob}>(config,'/api/v1/ai/desktop/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({bridge_test:true,node_id:target.node_id,workspace_id:target.workspace_id,model:model.id,reasoning_effort:effort})})).data;
  onStatus('Web 后端已接收检测任务，等待该桥领取…');
  const deadline=Date.now()+180000,queuedDeadline=Date.now()+90000;
  if(job.stream_events){
   const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),Math.max(1,deadline-Date.now()));
   try{await streamTaskSnapshots<ChatJob>(config,'/api/v1/ai/desktop/jobs/'+job.id+'/events',value=>{job=value;onStatus(value.status==='queued'?'等待桌面桥领取检测任务…':(bridgeStages[value.result?.stage||'']||'桌面桥正在处理检测任务…'));return ['completed','failed','expired','cancelled'].includes(value.status);},controller.signal);}
   catch(error){if(typeof error==='object'&&error!==null&&'status'in error&&Number((error as {status:unknown}).status)<500)throw error;}
   finally{clearTimeout(timer);}
  }
  while(Date.now()<deadline){
   if(!['completed','failed','expired','cancelled'].includes(job.status))job=(await requestChatJSON<{data:ChatJob}>(config,'/api/v1/ai/desktop/jobs/'+job.id)).data;
   if(job.status==='completed'){
    const content=job.result?.content||'';
    if(!content.trim())throw new Error('桥已回传空内容，检测未通过。');
    if(!content.includes('CODEX_BRIDGE_OK'))throw new Error('请求已往返并收到 Codex 回执，但检测标记不匹配；请查看桥的任务日志。');
    await cleanup();
    return '全链路通过：Web提交 → 后端排队 → 桥领取 → Codex回复 → 回执写回。桥 '+bridgeTargetLabel(target)+'。';
   }
   if(['failed','expired','cancelled'].includes(job.status))throw new Error(job.result?.error_code||('检测任务'+job.status));
   if(job.status==='queued'){
    onStatus('后端已排队，桌面桥尚未领取任务…');
    if(Date.now()>=queuedDeadline)throw new Error('Web已提交，但桌面桥90秒内未领取；请核对桥进程与后台任务状态。');
   }else if(job.status==='running'){
    const stages=bridgeStages;
    const stage=job.result?.stage||'bridge_claimed';
    const message=(stages[stage]||'桌面桥已领取，正在处理检测任务…')+'（已等待 '+Math.floor((Date.now()-startedAt)/1000)+' 秒）';
    if(message!==lastStatus){lastStatus=message;onStatus(message);}
   }
   await new Promise<void>(resolve=>window.setTimeout(resolve,1500));
  }
  throw new Error('检测等待超过3分钟；请查看桌面桥任务状态。');
 }catch(error){
  const terminal=!!job&&['completed','failed','expired','cancelled'].includes(job.status);
  if(terminal)await cleanup();
  else{
   const cancelled=await cancel();
   if(error instanceof Error&&error.message==='检测等待超过3分钟；请查看桌面桥任务状态。')
    throw new Error(cancelled?'检测等待超过3分钟；后端已确认任务已取消或终态。':'检测等待超过3分钟；取消请求未确认，管理器可能暂时仍显示运行，请刷新查看。');
  }
  throw error;
 }
}
export async function streamDesktopChat(request:HermesStreamRequest):Promise<HermesStreamResult>{
 if(request.signal?.aborted)throw new DOMException('AI对话已停止','AbortError');
 let job:ChatJob|undefined;let previous='';
 let cancelPromise:Promise<boolean>|undefined;const cancel=()=>{if(!job)return Promise.resolve(false);if(!cancelPromise)cancelPromise=requestChatJSON<unknown>(request.config,'/api/v1/ai/desktop/jobs/'+job.id,{method:'DELETE'}).then(()=>true).catch(()=>false);return cancelPromise;};
 const stop=()=>{void cancel();};request.signal?.addEventListener('abort',stop,{once:true});
 try{
  job=(await requestChatJSON<{data:ChatJob}>(request.config,'/api/v1/ai/desktop/jobs',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({prompt:request.prompt,session_id:request.hermesSessionID||'',analysis_id:request.analysisID||'',messages:request.seedMessages||[]})},request.signal)).data;
  request.onSession?.(job.session_id);const deadline=Date.now()+420000,queuedDeadline=Date.now()+90000;
  if(request.signal?.aborted)throw new DOMException('AI对话已停止','AbortError');
  if((job as ChatJob&{stream_events?:boolean}).stream_events){
   const streamController=new AbortController();const abortStream=()=>streamController.abort();
   request.signal?.addEventListener('abort',abortStream,{once:true});
   const streamTimer=setTimeout(abortStream,Math.max(1,deadline-Date.now()));
   const queueTimer=setTimeout(()=>{if(job?.status==='queued')abortStream();},Math.max(1,queuedDeadline-Date.now()));
   try{
    await streamTaskSnapshots<ChatJob>(request.config,'/api/v1/ai/desktop/jobs/'+job.id+'/events',value=>{
     job=value;const content=value.result?.content||'';
     if(content!==previous){previous=content;request.onDelta?.(content);}
     request.onStatus?.({kind:'process',text:value.status==='queued'?'等待工作节点领取桌面 Codex 任务…':'桌面 Codex 正在回复…'});
     return ['completed','failed','expired','cancelled'].includes(value.status);
    },streamController.signal);
   }catch(error){if(request.signal?.aborted)throw error;if(typeof error==='object'&&error!==null&&'status'in error&&Number((error as {status:unknown}).status)<500)throw error;}
   finally{clearTimeout(streamTimer);clearTimeout(queueTimer);request.signal?.removeEventListener('abort',abortStream);}
   if(job.status==='completed'&&job.result?.content?.trim())return {content:job.result.content,hermesSessionID:job.session_id};
  }
  while(Date.now()<deadline){
   if(request.signal?.aborted)throw new DOMException('AI对话已停止','AbortError');
   job=(await requestChatJSON<{data:ChatJob}>(request.config,'/api/v1/ai/desktop/jobs/'+job.id,{},request.signal)).data;
   const content=job.result?.content||'';
   if(content!==previous){previous=content;request.onDelta?.(content);}
   request.onStatus?.({kind:'process',text:job.status==='queued'?'等待工作节点领取桌面 Codex 任务…':'桌面 Codex 正在回复…'});
   if(job.status==='completed'){
    if(!content.trim())throw new Error('Codex未返回有效内容');
    return {content,hermesSessionID:job.session_id};
   }
   if(job.status==='queued'&&Date.now()>=queuedDeadline)throw new Error('桌面桥未领取任务。请确认 Web 与管理器中的节点 ID、工作区一致，且该桥在线。');
   if(['failed','expired','cancelled'].includes(job.status))throw new Error(job.result?.error_code||(job.status==='expired'?'桌面桥任务已过期，请检查工作节点与工作区是否一致。':'Codex对话'+job.status));
   await new Promise<void>((resolve,reject)=>{const stop=()=>{clearTimeout(timer);request.signal?.removeEventListener('abort',stop);reject(new DOMException('AI对话已停止','AbortError'));};const timer=setTimeout(()=>{request.signal?.removeEventListener('abort',stop);resolve();},2000);if(request.signal?.aborted)stop();else request.signal?.addEventListener('abort',stop,{once:true});});
  }
  throw new Error('Codex对话等待超时');
 }catch(error){
  const terminal=!!job&&['completed','failed','expired','cancelled'].includes(job.status);
  if(!terminal){
   const cancelled=await cancel();
   if(!cancelled&&!(error instanceof DOMException&&error.name==='AbortError'))
    throw new Error((error instanceof Error?error.message:String(error))+'；后台取消未确认，管理器可能暂时仍显示运行。');
  }
  throw error;
 }finally{request.signal?.removeEventListener('abort',stop);}
}
async function requestChatJSON<T>(config:BackendConfig,path:string,init:RequestInit={},signal?:AbortSignal):Promise<T>{
 const method=(init.method||'GET').toUpperCase(),attempts=method==='GET'?3:1;
 for(let attempt=0;attempt<attempts;attempt++){
  const controller=new AbortController();let timedOut=false;const stop=()=>controller.abort();
  if(signal?.aborted)controller.abort();else signal?.addEventListener('abort',stop,{once:true});
  const timer=setTimeout(()=>{timedOut=true;controller.abort();},10000);
  try{return await requestJSON<T>(config,path,{...init,signal:controller.signal});}
  catch(error){
   if(signal?.aborted)throw error;
   const status=typeof error==='object'&&error!==null&&'status'in error?Number((error as {status?:unknown}).status):0;
   const retryable=timedOut||status===0||status===408||status===429||status>=500;
   if(!retryable||attempt+1>=attempts){if(timedOut)throw new Error('连接 Web 后端超时，请检查桌面桥与工作区连接。');throw error;}
  }finally{clearTimeout(timer);signal?.removeEventListener('abort',stop);}
  await new Promise<void>(resolve=>window.setTimeout(resolve,250*(attempt+1)));
 }
 throw new Error('连接 Web 后端超时，请检查桌面桥与工作区连接。');
}
