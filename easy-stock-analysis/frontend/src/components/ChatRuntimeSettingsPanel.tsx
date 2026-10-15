import {useEffect,useState} from 'react';
import {requestJSON,type BackendConfig} from '../lib/backend';
import {loadChatRuntime,saveChatRuntime,type ChatRuntime,type ChatRuntimeConfig} from '../lib/desktop-chat';

export function ChatRuntimeSettingsPanel({config,open}:{config:BackendConfig|null;open:boolean}){
 const [data,setData]=useState<ChatRuntime|null>(null),[draft,setDraft]=useState<ChatRuntimeConfig|null>(null),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false);
 const [targets,setTargets]=useState<Array<{node_id:string;workspace_id:string;label:string}>>([]);
 useEffect(()=>{if(!config||!open)return;let live=true;
  Promise.all([loadChatRuntime(config),requestJSON<{targets:typeof targets}>(config,'/api/v1/linkage/targets')]).then(([d,t])=>{if(live){setData(d);setDraft(d.config);setTargets(t.targets);}}).catch(e=>{if(live)setNotice(String(e));});return()=>{live=false;};
 },[config,open]);
 async function selectNode(node:string){const target=targets.find(t=>t.node_id===node);if(!config||!draft||!target)return;setBusy(true);try{const d=await loadChatRuntime(config,target.node_id,target.workspace_id);setData(d);const model=d.models?.find(m=>m.id===draft.model)||d.models?.[0];setDraft({...draft,node_id:target.node_id,workspace_id:target.workspace_id,model:model?.id||'',reasoning_effort:model?.efforts.includes(draft.reasoning_effort)?draft.reasoning_effort:model?.efforts[0]||''});}catch(e){setNotice(String(e));}finally{setBusy(false);}}
 async function save(){if(!config||!draft)return;setBusy(true);try{const d=await saveChatRuntime(config,draft);setData(d);setDraft(d.config);setNotice('AI对话运行时已保存，新消息生效；在途消息保持原配置。');window.dispatchEvent(new Event('ai-runtime-changed'));}catch(e){setNotice(String(e));}finally{setBusy(false);}}
 const model=data?.models?.find(m=>m.id===draft?.model);
 return <section className="settings-section"><h3>AI 对话接入方式</h3><p>Hermes 与桌面 Codex 均可选；此设置只影响 AI 对话，不改变模拟策略或交易配置。</p>
 {draft&&<><label>运行时<select aria-label="AI对话接入方式" disabled={busy} value={draft.provider} onChange={e=>setDraft({...draft,provider:e.target.value as ChatRuntimeConfig['provider']})}><option value="hermes">Hermes 本地运行时</option><option value="codex_desktop">桌面 Codex · 现有登录账户</option></select></label>
 {draft.provider==='codex_desktop'&&<><p>登录凭据保留在桌面，无需填写 API Key。仅启用本系统提供的只读证据工具；Hermes Skills/MCP 不会自动转交。</p><label>桌面节点<select aria-label="Codex对话桌面" disabled={busy} value={draft.node_id} onChange={e=>void selectNode(e.target.value)}><option value="">选择已登记桌面</option>{targets.map(t=><option key={t.node_id} value={t.node_id}>{t.label||t.node_id}</option>)}</select></label><p>{data?.online?'桌面在线':'桌面未就绪'} · 模型目录沿用桌面每日刷新与升级切换。</p><label>模型<select aria-label="Codex对话模型" value={draft.model} disabled={busy} onChange={e=>{const m=data?.models?.find(m=>m.id===e.target.value);if(m)setDraft({...draft,model:m.id,reasoning_effort:m.efforts.includes(draft.reasoning_effort)?draft.reasoning_effort:m.efforts[0]});}}>{!model&&<option value={draft.model}>{draft.model||'请选择可用模型'}</option>}{data?.models?.map(m=><option key={m.id} value={m.id}>{m.name||m.id}</option>)}</select></label><label>推理强度<select aria-label="Codex对话推理强度" disabled={busy} value={draft.reasoning_effort} onChange={e=>setDraft({...draft,reasoning_effort:e.target.value})}>{model?.efforts.map(e=><option key={e}>{e}</option>)}</select></label></>}
 <button type="button" disabled={busy||(draft.provider==='codex_desktop'&&(!data?.online||!model))} onClick={()=>void save()}>保存 AI 对话接入方式</button></>}{notice&&<p role="status">{notice}</p>}</section>;
}
