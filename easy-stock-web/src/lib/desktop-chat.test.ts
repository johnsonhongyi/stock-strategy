import {afterEach,describe,expect,it,vi} from 'vitest';
import {streamDesktopChat,saveChatRuntime,loadChatRuntime} from './desktop-chat';
const config={backendUrl:'http://127.0.0.1:20084',token:'test-only'};
const response=(data:unknown)=>new Response(JSON.stringify({data}),{status:200,headers:{'Content-Type':'application/json'}});
afterEach(()=>vi.unstubAllGlobals());
describe('desktop Codex conversation adapter',()=>{
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
  await saveChatRuntime(config,{provider:'codex_desktop',node_id:'node',workspace_id:'workspace',model:'luna',reasoning_effort:'low'});
  const body=JSON.parse(fetch.mock.calls[0][1].body);expect(body.provider).toBe('codex_desktop');expect(body).not.toHaveProperty('api_key');
 });
});
