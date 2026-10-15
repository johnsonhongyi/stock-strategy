import {describe,it,expect,vi,afterEach} from 'vitest';
import {accountMetrics,reasonName,FollowData,followCommandID} from './auto-follow';
const fixture=():FollowData=>({available:true,online:true,can_control:true,commands:[],summary:{realized_pnl:300,closed_count:2,winning_count:1},state:{mode:'paper',version:'paper-follow-v1',cash:1000,initial_cash:500000,kill:false,events:[],closed:[],positions:[{symbol:'600001.SH',route:'limit_up',shares:100,buy_price:10,last_price:11,last_price_at:'2026-10-15T10:00:00+08:00'}]}});
describe('paper follower account evidence',()=>{
 it('never invents an account when the worker is missing',()=>expect(accountMetrics(null)).toBeNull());
 it('calculates equity and flags old quote estimates separately',()=>{const m=accountMetrics(fixture(),Date.parse('2026-10-15T10:10:00+08:00'));expect(m).toMatchObject({equity:2100,floating:100,realized:300,stale:true,winRate:50})});
 it('does not label fresh quotes as stale',()=>expect(accountMetrics(fixture(),Date.parse('2026-10-15T10:00:30+08:00'))?.stale).toBe(false));
 it('keeps unknown rejection evidence visible',()=>expect(reasonName('provider_custom_error')).toBe('provider_custom_error'));
});

describe('HTTP command IDs',()=>{
 afterEach(()=>vi.unstubAllGlobals());
 it('works on LAN HTTP without secure-context randomUUID',()=>{vi.stubGlobal('crypto',{getRandomValues:(a:Uint8Array)=>a.fill(7)});expect(followCommandID()).toBe('07'.repeat(16))});
 it('uses randomUUID when a secure context provides it',()=>{vi.stubGlobal('crypto',{randomUUID:()=> 'secure-uuid'});expect(followCommandID()).toBe('secure-uuid')});
});
