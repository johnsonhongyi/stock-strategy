import {describe,it,expect} from 'vitest';
import {renderToString} from 'react-dom/server';
import {FollowAIWorkspace,commandNotice,commandAISettings} from './FollowAIWorkspace';
import {AIReviewPanel} from './AIReviewPanel';
import type {FollowData} from '../lib/auto-follow';

describe('AI workflow evidence boundaries',()=>{
 it('does not fabricate a model proposal or fill when no result exists',()=>{
  const html=renderToString(<FollowAIWorkspace data={null} config={null} onChanged={()=>{}}/>);
  expect(html).toContain('尚无通过校验的 AI 提案');expect(html).toContain('暂无带AI提案引用的模拟成交');expect(html).toContain('未连接');
 });
 it('renders actual fill lineage and an independent review without calling it an entry plan',()=>{
  const proposal={market_view:'review market',rotation_view:'review rotation',answer:'review answer',plans:[]};
  const data:FollowData={available:true,online:true,can_control:false,commands:[],state:{mode:'paper',version:'paper-follow-v1',cash:500000,initial_cash:500000,kill:false,positions:[],events:[],closed:[{symbol:'600001.SH',name:'synthetic',route:'limit_up',sell_date:'2026-10-16',ai_decision:{mode:'enforce',job_id:'entry-job',evidence_hash:'entry-evidence',thread_id:'own-thread'}}],ai_review:{id:'review-job',as_of:'2026-10-15T10:00:00+08:00',evidence_hash:'review-evidence',workflow_stage:'execution_review',result:{proposal,thread_id:'own-thread'}}}};
  const html=renderToString(<AIReviewPanel data={data}/>);expect(html).toContain('entry-job');expect(html).toContain('review-job');expect(html).toContain('entry-evidence');expect(html).toContain('review answer');expect(html).toContain('已模拟平仓');
 });
});

describe('worker receipt and model failure feedback',()=>{
 it('replaces waiting with settings applied and explicit model timeout',()=>{
  const receipt={id:'command',action:'ai_settings',created_at:'',status:'applied',applied_at:'',result:'applied'};
  expect(commandNotice(receipt)).toContain('设置已由后台应用');
  const job={id:'job',status:'failed',created_at:'',question:'',evidence_hash:'',result:{error_code:'TimeoutError'}};
  expect(commandNotice({...receipt,action:'ai_ask'},job)).toContain('分析失败：TimeoutError');
  expect(commandNotice({...receipt,action:'ai_ask'},{...job,status:'completed'})).toContain('分析完成');
 });
 it('shows the shared automatic research settings instead of independent model controls',()=>{
  const html=renderToString(<FollowAIWorkspace data={null} config={null} onChanged={()=>{}}/>);
  expect(html).toContain('自动 AI 投研');expect(html).toContain('统一 AI 运行时');expect(html).toContain('默认关闭');expect(html).not.toContain('AI分析模型');expect(html).not.toContain('AI推理强度');
 });
});

it('does not send worker-only persisted fields to the strict settings API',()=>{
 const persisted={enabled:true,mode:'enforce' as const,node_id:'desktop',workspace_id:'workspace',interval_seconds:900};
 expect(commandAISettings(persisted,'gpt-6-luna','low')).toEqual({enabled:true,mode:'enforce',node_id:'desktop',workspace_id:'workspace',model:'gpt-6-luna',reasoning_effort:'low'});
});
