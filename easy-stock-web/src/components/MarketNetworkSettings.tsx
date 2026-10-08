import { useEffect, useRef, useState } from 'react';
import { BackendConfig, requestJSON } from '../lib/backend';

type Stock = { market: string; symbol: string; tracked: boolean };
const markets = [{ code: 'CN', name: 'A股' }, { code: 'US', name: '美股' }, { code: 'CRYPTO', name: '币圈' }];

export function MarketNetworkSettings({ config, onRetried }: { config: BackendConfig | null; onRetried: () => void }) {
 const [proxy, setProxy] = useState('');
 const [loaded, setLoaded] = useState(false);
 const [busy, setBusy] = useState('');
 const [message, setMessage] = useState('');
 const [errors, setErrors] = useState<string[]>([]);
 const controller = useRef<AbortController | null>(null);
 useEffect(() => {
  const abort = new AbortController(); controller.current = abort;
  setLoaded(false); setBusy(''); setMessage(''); setErrors([]);
  if (config) void requestJSON<{ proxy_url: string }>(config, '/api/v1/market-data/network', { signal: abort.signal })
   .then((result) => { if (!abort.signal.aborted) { setProxy(result.proxy_url); setLoaded(true); } })
   .catch((error) => { if (!abort.signal.aborted) setErrors([error instanceof Error ? error.message : '读取代理失败']); });
  return () => abort.abort();
 }, [config]);
 const save = async () => {
  if (!config || busy || !loaded) return;
  setBusy('save'); setMessage(''); setErrors([]);
  const signal = controller.current?.signal;
  try {
   const result = await requestJSON<{ proxy_url: string }>(config, '/api/v1/market-data/network', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ proxy_url: proxy.trim() }), signal });
   if (!signal?.aborted) { setProxy(result.proxy_url); setMessage('已保存；后续行情请求和盘后任务使用此配置。'); }
  } catch (error) { if (!signal?.aborted) setErrors([error instanceof Error ? error.message : '保存失败']); }
  finally { if (!signal?.aborted) setBusy(''); }
 };
 const retry = async (market: string) => {
  if (!config || busy || !loaded) return;
  setBusy(market); setMessage(''); setErrors([]);
  const signal = controller.current?.signal;
  let succeeded = 0;
  const failures: string[] = [];
  try {
   const result = await requestJSON<{ data: Stock[] }>(config, `/api/v1/market-data/stocks?market=${market}`, { signal });
   const stocks = result.data.filter((stock) => stock.tracked);
   if (!stocks.length) { if (!signal?.aborted) setMessage('该市场没有跟踪标的，请先在持久化行情中添加。'); return; }
   for (let index = 0; index < stocks.length; index += 1) {
    if (signal?.aborted) return;
    const stock = stocks[index]; setMessage(`重试 ${index + 1}/${stocks.length}：${stock.symbol}`);
    try {
     await requestJSON(config, `/api/v1/market-data/stocks/${market}/${encodeURIComponent(stock.symbol)}/refresh`, { method: 'POST', signal });
     succeeded += 1;
    } catch (error) {
     if (signal?.aborted) return;
     failures.push(`${stock.symbol}：${error instanceof Error ? error.message : '刷新失败'}`);
     setErrors([...failures]);
    }
   }
   if (!signal?.aborted) { setMessage(`重试结束：成功 ${succeeded}，失败 ${failures.length}；失败标的保留已有行情。`); onRetried(); }
  } catch (error) { if (!signal?.aborted) setErrors([error instanceof Error ? error.message : '读取跟踪名单失败']); }
  finally { if (!signal?.aborted) setBusy(''); }
 };
 return <div className="market-network-settings">
  <strong>行情网络与手动重试</strong>
  <label>自定义 HTTP / HTTPS 代理<input type="url" value={proxy} disabled={!loaded || !!busy} onChange={(event) => setProxy(event.target.value)} placeholder="http://192.168.50.10:7890" autoComplete="off" /></label>
  <p>填服务器与容器均可访问的代理地址；留空直连，保存后生效。支持 HTTP / HTTPS，无账号密码。重试会刷新该市场跟踪名单并校验落库；下方保留最近定时任务日志。</p>
  <div className="market-network-actions"><button type="button" disabled={!loaded || !!busy} onClick={() => void save()}>{busy === 'save' ? '保存中…' : '保存代理'}</button>{markets.map((market) => <button type="button" key={market.code} disabled={!loaded || !!busy} onClick={() => void retry(market.code)}>{busy === market.code ? '重试中…' : `重试${market.name}`}</button>)}</div>
  {message && <p role="status">{message}</p>}
  {errors.length > 0 && <ul className="system-status-inline-error" role="alert">{errors.map((error, index) => <li key={index}>{error}</li>)}</ul>}
 </div>;
}
