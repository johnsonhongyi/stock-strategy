import { requestJSON, type BackendConfig, type KLine } from './backend';
export class SBCRecoveryGate {
  private attempted=new Set<string>();
  claim(key:string) {if(this.attempted.has(key))return false;this.attempted.add(key);return true;}
}
export async function recoverSBCBars(config:BackendConfig,symbol:string,market:string|undefined,period:string,signal:AbortSignal):Promise<{data:KLine[]}> {
  const selectedMarket=market || (/^(?:sh|sz|bj)?\d{5,6}(?:\.(?:SH|SZ|BJ))?$/i.test(symbol)?'CN':undefined);
  if(['day','2d','3d','week','month'].includes(period) && selectedMarket) {
    const refreshed=await requestJSON<{data:KLine[]}>(config,`/api/v1/market-data/stocks/${encodeURIComponent(selectedMarket)}/${encodeURIComponent(symbol)}/refresh?limit=240`,{method:'POST',signal});
    if(period==='day')return refreshed;
  }
  return requestJSON<{data:KLine[]}>(config,`/api/v1/quotes/kline?symbol=${encodeURIComponent(symbol)}&period=${encodeURIComponent(period)}&limit=240${selectedMarket?`&market=${encodeURIComponent(selectedMarket)}`:''}`,{signal,headers:{'X-Stock-Cache-Refresh':'1'}});
}
