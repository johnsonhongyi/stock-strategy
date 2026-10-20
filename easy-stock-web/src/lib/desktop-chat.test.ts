import {afterEach,describe,expect,it,vi} from 'vitest';
import {streamDesktopChat,saveChatRuntime,loadChatRuntime,prepareDesktopChatRuntime,testDesktopChatBridge,type AvailableDesktopChatTarget} from './desktop-chat';
const config={backendUrl:'http://127.0.0.1:20084',token:'test-only'};
const response=(data:unknown)=>new Response(JSON.stringify({data}),{status:200,headers:{'Content-Type':'application/json'}});
const rawResponse=(data:unknown)=>new Response(JSON.stringify(data),{status:200,headers:{'Content-Type':'application/json'}});
afterEach(()=>vi.unstubAllGlobals());
describe('desktop bridge conversation adapter',()=>{
 it('does not overwrite the system runtime from background alignment',async()=>{
  const current={provider:'codex_desktop',node_id:'chosen',workspace_id:'ws',model:'luna',reasoning_effort:'low'};
  const fetch=vi.fn().mockResolvedValue(response({config:current}));vi.stubGlobal('fetch',fetch);
  await expect(saveChatRuntime(config,{...current,provider:'codex_desktop',node_id:'another'})).rejects.toThrow('禁止后台自动改写');
  expect(fetch.mock.calls.every(call=>!call[1]?.method||call[1].method==='GET')).toBe(true);
 });
 it('does not silently lower an unavailable AGV effort or choose another node',async()=>{
  const fetch=vi.fn().mockImplementation((input:RequestInfo|URL)=>{
   if(String(input).includes('/api/v1/linkage/targets'))return rawResponse({targets:[{node_id:'agv-main',workspace_id:'ws-agv',label:'AGV'}]});
   return response({config:{provider:'codex_desktop',node_id:'codex-main',workspace_id:'ws-codex',model:'gpt-6-luna',reasoning_effort:'high'},online:true,models:[{id:'claude-sonnet-4-6',name:'Claude Sonnet 4.6 (Thinking)',efforts:['auto']}],message:''});
  });vi.stubGlobal('fetch',fetch);
  await expect(prepareDesktopChatRuntime(config,{provider:'antigravity_desktop',node_id:'agv-main',workspace_id:'ws-agv',model:'claude-sonnet-4-6',reasoning_effort:'high'})).rejects.toThrow('不会自动换桥');
 });
 it('queries the strategy capability without changing the saved chat runtime',async()=>{
  const fetch=vi.fn().mockResolvedValue(response({online:true,models:[]}));vi.stubGlobal('fetch',fetch);
  await loadChatRuntime(config,'desk','ws','strategy_research');
  expect(String(fetch.mock.calls[0][0])).toContain('capability=strategy_research');expect(fetch.mock.calls[0][1]?.method||'GET').toBe('GET');
 });
 it('returns the persisted logical session and completed text without Hermes',async()=>{
  const fetch=vi.fn().mockResolvedValueOnce(response({id:'job',session_id:'logical',status:'queued'})).mockResolvedValueOnce(response({id:'job',session_id:'logical',status:'completed',result:{content:'真实回传'}}));vi.stubGlobal('fetch',fetch);
  const onSession=vi.fn(),onDelta=vi.fn();
  const result=await streamDesktopChat({config,prompt:'问题',onSession,onDelta});
  expect(result).toEqual({content:'真实回传',hermesSessionID:'logical'});expect(onSession).toHaveBeenCalledWith('logical');expect(onDelta).toHaveBeenCalledWith('真实回传');
  expect(String(fetch.mock.calls[0][0])).toContain('/ai/desktop/jobs');
 });
 it('reports failure instead of pretending success',async()=>{
  vi.stubGlobal('fetch',vi.fn().mockResolvedValueOnce(response({id:'job',session_id:'logical'})).mockResolvedValueOnce(response({id:'job',session_id:'logical',status:'failed',result:{error_code:'CLI_USAGE_LIMIT'}})));
  await expect(streamDesktopChat({config,prompt:'问题'})).rejects.toThrow('CLI_USAGE_LIMIT');
 });
 it('cancels the server job when the user aborts',async()=>{
  const fetch=vi.fn().mockResolvedValue(response({id:'job',session_id:'logical',status:'queued'}));vi.stubGlobal('fetch',fetch);const controller=new AbortController();
  await expect(streamDesktopChat({config,prompt:'问题',signal:controller.signal,onSession:()=>controller.abort()})).rejects.toMatchObject({name:'AbortError'});
  expect(fetch.mock.calls.some(c=>c[1]?.method==='DELETE')).toBe(true);
 });
 it('saves runtime choice without any API key field',async()=>{
  const fetch=vi.fn().mockResolvedValue(response({}));vi.stubGlobal('fetch',fetch);
  await saveChatRuntime(config,{provider:'codex_desktop',node_id:'node',workspace_id:'workspace',model:'luna',reasoning_effort:'low'},true);
  const body=JSON.parse(fetch.mock.calls[0][1].body);expect(body.provider).toBe('codex_desktop');expect(body).not.toHaveProperty('api_key');
 });
 it('keeps Antigravity selection on an online AGV target even when Codex is the default workspace',async()=>{
  const fetch=vi.fn().mockImplementation((input:RequestInfo|URL)=>{
   const url=String(input);
   if(url.includes('/api/v1/linkage/targets'))return rawResponse({default_node_id:'codex-main',targets:[
    {node_id:'codex-main',workspace_id:'ws-codex',label:'Codex'},
    {node_id:'agv-main',workspace_id:'ws-agv',label:'Antigravity'},
   ]});
   const antigravity=url.includes('node_id=agv-main');
   const model=antigravity?'gemini-3.8-flash':'gpt-6-luna';
   return response({config:{provider:'codex_desktop',node_id:'codex-main',workspace_id:'ws-codex',model:'gpt-6-luna',reasoning_effort:'low'},online:true,models:[{id:model,name:model,efforts:['low','high']}],message:''});
  });vi.stubGlobal('fetch',fetch);
  const current={provider:'antigravity_desktop' as const,node_id:'agv-main',workspace_id:'ws-agv',model:'gemini-3.8-flash',reasoning_effort:'high'};
  const selected=await prepareDesktopChatRuntime(config,current,'antigravity_desktop');
  expect(selected).toMatchObject({provider:'antigravity_desktop',node_id:'agv-main',workspace_id:'ws-agv',model:'gemini-3.8-flash',reasoning_effort:'high'});
 });
 it('does not silently substitute Codex when no Antigravity bridge is available',async()=>{
  const fetch=vi.fn().mockImplementation((input:RequestInfo|URL)=>{
   if(String(input).includes('/api/v1/linkage/targets'))return rawResponse({default_node_id:'codex-main',targets:[{node_id:'codex-main',workspace_id:'ws-codex',label:'Codex'}]});
   return response({config:{provider:'codex_desktop',node_id:'codex-main',workspace_id:'ws-codex',model:'gpt-6-luna',reasoning_effort:'low'},online:true,models:[{id:'gpt-6-luna',name:'Codex',efforts:['low']}],message:''});
  });vi.stubGlobal('fetch',fetch);
  await expect(prepareDesktopChatRuntime(config,{provider:'antigravity_desktop',node_id:'',workspace_id:'',model:'gemini-3.8-flash',reasoning_effort:'low'},'antigravity_desktop')).rejects.toThrow('不会自动换桥');
 });
 it('checks for the Antigravity marker on an AGV bridge test',async()=>{
  const target:AvailableDesktopChatTarget={node_id:'agv-main',workspace_id:'ws-agv',label:'Antigravity',provider:'antigravity_desktop',runtime:{config:{provider:'antigravity_desktop',node_id:'agv-main',workspace_id:'ws-agv',model:'gemini-3.8-flash',reasoning_effort:'low'},online:true,models:[{id:'gemini-3.8-flash',name:'Gemini 3.8 Flash',efforts:['low']}],message:''}};
  const fetch=vi.fn().mockResolvedValueOnce(response(target.runtime)).mockResolvedValueOnce(response({id:'job',session_id:'test',status:'completed',result:{content:'ANTIGRAVITY_BRIDGE_OK'}})).mockResolvedValueOnce(response({}));vi.stubGlobal('fetch',fetch);
  await expect(testDesktopChatBridge(config,target,'gemini-3.8-flash','low',vi.fn())).resolves.toContain('Antigravity回复');
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toMatchObject({bridge_test:true,node_id:'agv-main',workspace_id:'ws-agv'});
 });
});
