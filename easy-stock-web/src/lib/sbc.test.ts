import { describe, it, expect, vi, afterEach } from 'vitest';
import { httpSBCProvider } from './sbc';
import type { KLine } from './backend';

vi.mock('./backend', () => ({requestJSON: vi.fn()}));
import { requestJSON } from './backend';

const bars = [{symbol:'600733.SH', time:'2026-10-08T00:00:00Z'}] as KLine[];
const input = {symbol:'600733.SH', period:'day', bars};
const config = {backendUrl:'http://localhost',token:'test'};
const valid = {schema_version:1, symbol:input.symbol, period:'day', points:[{time:bars[0].time}]};
afterEach(() => vi.clearAllMocks());

describe('SBC snapshot contract', () => {
  it('posts the displayed bars and abort signal', async () => {
    vi.mocked(requestJSON).mockResolvedValue({data:valid});
    const signal = new AbortController().signal;
    expect(await httpSBCProvider(config,input,signal)).toEqual(valid);
    expect(requestJSON).toHaveBeenCalledWith(config,'/api/v1/quotes/sbc/v1/analyze',expect.objectContaining({method:'POST',body:JSON.stringify(input),signal}));
  });
  it.each([
    {...valid,schema_version:2}, {...valid,symbol:'000001.SZ'},
    {...valid,period:'week'}, {...valid,points:[]},
    {...valid,points:[{time:'2026-10-07T00:00:00Z'}]},
  ])('rejects incompatible or mismatched snapshots', async data => {
    vi.mocked(requestJSON).mockResolvedValue({data});
    await expect(httpSBCProvider(config,input,new AbortController().signal)).rejects.toThrow('SBC 结果与当前行情不一致');
  });
});
