import {describe,it,expect} from 'vitest';
import {renderToString} from 'react-dom/server';
import {FollowResearchPanel} from './FollowResearchPanel';
import type {FollowData} from '../lib/auto-follow';
describe('research evidence',()=>{
 it('does not invent stock plans for missing evidence',()=>{const h=renderToString(<FollowResearchPanel data={null}/>);expect(h).toContain('候选数据缺失');expect(h).toContain('来源未提供');expect(h).toContain('资金轮动与个股作战计划')});
 it('renders invalid data as reference and preserves the conditional gate',()=>{const d:FollowData={available:true,online:true,can_control:false,commands:[],state:{mode:'paper',version:'paper-follow-v1',cash:10000,initial_cash:10000,kill:false,positions:[],closed:[],events:[],research:{pool_mode:'retained_reference',industry:{coverage:1,error:'unknown trade date',rows:[{code:'A',name:'sample sector',rank:1,net_flow:20000,flow_field:'net_inflow',change_percent:1}]}},candidates:[{symbol:'600001.SH',name:'sample stock',route:'theme_trend',plan:{fresh_quote:false,structure_ready:false,blocker:'P8 VWAP gate',metrics:{price:10},response:'waiting',checks:[{name:'P8',status:'unknown'}]}}]}};const h=renderToString(<FollowResearchPanel data={d}/>);expect(h).toContain('sample stock');expect(h).toContain('unknown trade date');expect(h).toContain('无可比快照');expect(h).toContain('待有效数据');expect(h).toContain('报价未验证 / 参考');expect(h).toContain('条件入场区间')});
});
