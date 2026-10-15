import {requestJSON,type BackendConfig} from './backend';
import type {HermesStreamRequest,HermesStreamResult} from './hermes';

export type ChatRuntimeConfig={provider:'hermes'|'codex_desktop';node_id:string;workspace_id:string;model:string;reasoning_effort:string};
export type ChatRuntime={config:ChatRuntimeConfig;online:boolean;models:Array<{id:string;name:string;efforts:string[]}>;message:string};
export async function loadChatRuntime(config:BackendConfig,node_id='',workspace_id=''){
 const query=node_id?'?'+new URLSearchParams({node_id,workspace_id}):'';
 return (await requestJSON<{data:ChatRuntime}>(config,'/api/v1/ai/runtime'+query)).data;
}
export async function prepareDesktopChatRuntime(config: BackendConfig, current: ChatRuntimeConfig): Promise<ChatRuntimeConfig> {
 const result = await requestJSON<{targets:Array<{node_id:string;workspace_id:string}>}>(config, '/api/v1/linkage/targets');
 const candidates = [...result.targets].sort((a,b) => Number(b.node_id === current.node_id) - Number(a.node_id === current.node_id));
 for (const target of candidates) {
  const runtime = await loadChatRuntime(config,target.node_id,target.workspace_id);
  if (!runtime.online || !runtime.models?.length) continue;
  const model = runtime.models.find(model => model.id === current.model) || runtime.models[0];
  if (!model.efforts.length) continue;
  return {...current,provider:'codex_desktop',node_id:target.node_id,workspace_id:target.workspace_id,model:model.id,reasoning_effort:model.efforts.includes(current.reasoning_effort)?current.reasoning_effort:model.efforts[0]};
 }
 throw new Error('没有支持 AI 对话的在线 Codex 桌面，请在 AI 接入设置检查桌面桥。');
}
export async function saveChatRuntime(config:BackendConfig,value:ChatRuntimeConfig){
 return (await requestJSON<{data:ChatRuntime}>(config,'/api/v1/ai/runtime',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(value)})).data;
}
type ChatJob={id:string;session_id:string;status:string;result?:{content?:string;thread_id?:string;error_code?:string;model?:string;reasoning_effort?:string}};
export async function streamDesktopChat(request:HermesStreamRequest):Promise<HermesStreamResult>{
 let job:ChatJob|undefined;let previous='';
 const cancel=()=>{if(job)void requestJSON(request.config,'/api/v1/ai/desktop/jobs/'+job.id,{method:'DELETE'}).catch(()=>{});};
 request.signal?.addEventListener('abort',cancel,{once:true});
 try{
  job=(await requestJSON<{data:ChatJob}>(request.config,'/api/v1/ai/desktop/jobs',{method:'POST',headers:{'Content-Type':'application/json'},signal:request.signal,body:JSON.stringify({prompt:request.prompt,session_id:request.hermesSessionID||'',analysis_id:request.analysisID||'',messages:request.seedMessages||[]})})).data;
  request.onSession?.(job.session_id);const deadline=Date.now()+650000;
  while(Date.now()<deadline){
   if(request.signal?.aborted)throw new DOMException('AI对话已停止','AbortError');
   job=(await requestJSON<{data:ChatJob}>(request.config,'/api/v1/ai/desktop/jobs/'+job.id,{signal:request.signal})).data;
   const content=job.result?.content||'';
   if(content!==previous){previous=content;request.onDelta?.(content);}
   request.onStatus?.({kind:'process',text:job.status==='queued'?'等待桌面 Codex 领取…':'桌面 Codex 正在回复…'});
   if(job.status==='completed'){
    if(!content.trim())throw new Error('Codex未返回有效内容');
    return {content,hermesSessionID:job.session_id};
   }
   if(['failed','expired','cancelled'].includes(job.status))throw new Error(job.result?.error_code||`Codex对话${job.status}`);
   await new Promise<void>((resolve,reject)=>{const stop=()=>{clearTimeout(timer);request.signal?.removeEventListener('abort',stop);reject(new DOMException('AI对话已停止','AbortError'));};const timer=setTimeout(()=>{request.signal?.removeEventListener('abort',stop);resolve();},1000);request.signal?.addEventListener('abort',stop,{once:true});});
  }
  cancel();throw new Error('Codex对话等待超时');
 }finally{request.signal?.removeEventListener('abort',cancel);}
}
