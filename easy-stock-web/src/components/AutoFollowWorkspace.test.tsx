import {describe,it,expect} from 'vitest';
import {renderToString} from 'react-dom/server';
import {AutoFollowWorkspace} from './AutoFollowWorkspace';
import type {FollowData} from '../lib/auto-follow';
describe('follow console evidence states',()=>{
 it('shows the entry and missing account explicitly instead of sample trades',()=>{const html=renderToString(<AutoFollowWorkspace config={null} refreshKey={0}/>);expect(html).toContain('自动跟单');expect(html).toContain('强势股跟踪');expect(html).toContain('控制指令回执');expect(html).toContain('不会使用示例股票或虚构成交');expect(html).not.toContain('500,000.00')});
 it('shows a paused real-account state without claiming it is trading',()=>{const data:FollowData={available:true,online:true,can_control:false,commands:[],state:{mode:'paper',version:'paper-follow-v1',cash:123456,initial_cash:500000,enabled:false,kill:false,cycle_status:'outside_session',positions:[],closed:[],events:[]}};const html=renderToString(<AutoFollowWorkspace config={null} refreshKey={0} initialData={data}/>);expect(html).toContain('恢复开仓');expect(html).toContain('新开仓已暂停');expect(html).toContain('等待交易时段');expect(html).toContain('只读访问');expect(html).toContain('123,456.00')});
});
