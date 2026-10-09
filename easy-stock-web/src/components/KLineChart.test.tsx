import {afterEach,describe,expect,it,vi} from 'vitest';
import {renderToStaticMarkup} from 'react-dom/server';
import {KLineChart} from './KLineChart';
import type {KLine} from '../lib/backend';
vi.mock('../lib/sbc',()=>({useSBC:()=>({status:'',data:{points:Array.from({length:10},(_,i)=>({time:`2026-10-09T02:${String(i).padStart(2,'0')}:00Z`,ma5:10,ma20:10,close:10,high:11,low:9,ch_upper:11,ch_mid:10,ch_lower:9,ch_supp_price:9.5,reversal_line:10.5,td_buy_count:9,sig_launch:1})),levels:[],strategy:{is_matched:false},trade_date:'2026-10-09'}})}));
const lines=Array.from({length:10},(_,i)=>({symbol:'600733.SH',time:`2026-10-09T02:${String(i).padStart(2,'0')}:00Z`,open:10,close:10,high:11,low:9,volume:100,amount:1000,vwap:10,meta:{source:'test'}})) as KLine[];
afterEach(()=>vi.unstubAllGlobals());
describe('SBC period layer isolation',()=>{
 it('shows only price/VWAP and reference levels in intraday mode',()=>{
  vi.stubGlobal('window',{innerWidth:1600,innerHeight:1000});
  const html=renderToStaticMarkup(<KLineChart lines={lines} symbol="600733.SH" mode="intraday" periodLabel="分时"/>);
  expect(html).toContain('data-sbc-layer="intraday"');expect(html).not.toContain('data-sbc-layer="daily"');expect(html).toContain('kline-average-line');
 });
 it('starts intraday at the complete available session',()=>{
  vi.stubGlobal('window',{innerWidth:1600,innerHeight:1000});
  const session=Array.from({length:238},(_,i)=>({...lines[0],time:new Date(Date.UTC(2026,9,9,1,30+i)).toISOString()}));
  const html=renderToStaticMarkup(<KLineChart lines={session} symbol="600733.SH" mode="intraday" periodLabel="分时"/>);
  expect(html).toContain('238 / 238');
 });
 it('renders all five sessions without recreating per-bar locale formatters',()=>{
  vi.stubGlobal('window',{innerWidth:1600,innerHeight:1000});
  const formatter=vi.spyOn(Date.prototype,'toLocaleString');
  const session=Array.from({length:1200},(_,i)=>({...lines[0],time:new Date(Date.UTC(2026,9,5+Math.floor(i/240),1,31+i%240)).toISOString()}));
  const html=renderToStaticMarkup(<KLineChart lines={session} symbol="600733.SH" mode="intraday" periodLabel="5日分时"/>);
  expect(html).toContain('1200 / 1200');expect(formatter).not.toHaveBeenCalled();formatter.mockRestore();
 });
 it('keeps candle indicator overlays in daily mode',()=>{
  vi.stubGlobal('window',{innerWidth:1600,innerHeight:1000});
  const html=renderToStaticMarkup(<KLineChart lines={lines} symbol="600733.SH" mode="daily" periodLabel="日K"/>);
  expect(html).toContain('data-sbc-layer="daily"');expect(html).not.toContain('data-sbc-layer="intraday"');
 });
});
