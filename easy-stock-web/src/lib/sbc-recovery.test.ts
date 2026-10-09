import { afterEach,describe,expect,it,vi } from 'vitest';
import { SBCRecoveryGate,recoverSBCBars } from './sbc-recovery';
vi.mock('./backend',()=>({requestJSON:vi.fn()}));
import { requestJSON } from './backend';
afterEach(()=>vi.clearAllMocks());
describe('invalid cached bars recovery',()=>{
  it('allows only one automatic repair per security and period',()=>{const gate=new SBCRecoveryGate();expect(gate.claim('CN:000951:day')).toBe(true);expect(gate.claim('CN:000951:day')).toBe(false);expect(gate.claim('CN:000951:2d')).toBe(true);});
  it('uses the validated persistent refresh for daily bars and propagates failure',async()=>{
    vi.mocked(requestJSON).mockResolvedValueOnce({data:[]});const config={backendUrl:'http://localhost',token:'test'};const signal=new AbortController().signal;
    await recoverSBCBars(config,'000951','CN','day',signal);
    expect(requestJSON).toHaveBeenCalledWith(config,'/api/v1/market-data/stocks/CN/000951/refresh?limit=240',{method:'POST',signal});
    vi.mocked(requestJSON).mockRejectedValueOnce(Error('upstream stale'));
    await expect(recoverSBCBars(config,'000951','CN','day',signal)).rejects.toThrow('upstream stale');
    expect(requestJSON).toHaveBeenCalledTimes(2);
  });
});
