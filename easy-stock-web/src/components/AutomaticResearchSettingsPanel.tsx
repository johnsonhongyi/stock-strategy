import { useCallback, useEffect, useRef, useState } from 'react';
import { requestJSON, type BackendConfig } from '../lib/backend';
import { desktopChatProviderLabel, type ChatRuntimeConfig } from '../lib/desktop-chat';

type Policy = { enabled: boolean; interval_seconds: number; daily_max_runs: number; daily_token_budget: number; revision: number };
type RecordItem = { id: string; created_at: string; started_at: string; status: string; model: string; reasoning_effort: string; stage: string; total_tokens: number; usage_known: boolean; error_code: string };
type Snapshot = {
  policy: Policy;
  runtime: ChatRuntimeConfig;
  status: string;
  usage: { runs: number; total_tokens: number; unsettled_runs: number };
  records: RecordItem[];
  records_total: number;
  records_page: number;
  records_page_size: number;
};
type Props = {
  config: BackendConfig | null;
  open: boolean;
  view?: 'settings' | 'summary';
  onOpenSettings?: () => void;
  onRegisterSave?: (save: (() => Promise<boolean>) | null) => void;
  onBusyChange?: (busy: boolean) => void;
  onEnabledChange?: (enabled: boolean) => void;
};

const RECORD_PAGE_SIZE = 20;
const labels: Record<string, string> = {
  disabled: '已关闭',
  ready: '可运行',
  runtime_unavailable: '已暂停：系统运行时暂不支持自动投研',
  model_unavailable: '已暂停：所选桥、模型或推理强度不可用',
  usage_unsettled: '已暂停：存在未结算或用量未知的任务',
  budget_exhausted: '已暂停：今日预算已达到',
  interval_wait: '等待下一次调度',
  policy_unavailable: '已暂停：设置或用量不可读',
};
const compactLabels: Record<string, string> = {
  disabled: '已关闭',
  ready: '可运行',
  runtime_unavailable: '运行时不可用 · 已暂停',
  model_unavailable: '模型不可用 · 已暂停',
  usage_unsettled: '用量未结算 · 已暂停',
  budget_exhausted: '预算已达上限 · 已暂停',
  interval_wait: '等待调度间隔',
  policy_unavailable: '状态不可读 · 已暂停',
};
const states: Record<string, string> = { queued: '排队', running: '运行中', completed: '完成', failed: '失败', cancelled: '已取消', expired: '已过期' };

function runtimeLabel(runtime: ChatRuntimeConfig) {
  const provider = runtime.provider === 'hermes' ? 'Hermes' : desktopChatProviderLabel(runtime.provider);
  return `${provider} · ${runtime.model || '未配置'} / ${runtime.reasoning_effort || '未配置'}`;
}

export function AutomaticResearchSettingsPanel({ config, open, view = 'settings', onOpenSettings, onRegisterSave, onBusyChange, onEnabledChange }: Props) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [draft, setDraft] = useState<Policy | null>(null);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [recordPage, setRecordPage] = useState(1);
  const dirty = useRef(false);
  const draftRef = useRef(draft);
  draftRef.current = draft;

  useEffect(() => { onEnabledChange?.(snapshot?.policy.enabled || false); }, [snapshot, onEnabledChange]);

  const loadPage = useCallback(async (page: number, isActive: () => boolean = () => true) => {
    if (!config) return;
    const { data } = await requestJSON<{ data: Snapshot }>(config, `/api/v1/ai/auto-research?page=${page}`);
    if (!isActive()) return;
    setSnapshot(data);
    setRecordPage(data.records_page);
    if (!dirty.current) setDraft(data.policy);
  }, [config]);

  useEffect(() => {
    if (!open || !config) return;
    let live = true;
    const update = () => { void loadPage(recordPage, () => live).catch(error => { if (live) setNotice(String(error)); }); };
    update();
    const timer = window.setInterval(update, 15000);
    window.addEventListener('ai-runtime-changed', update);
    return () => { live = false; clearInterval(timer); window.removeEventListener('ai-runtime-changed', update); };
  }, [config, open, loadPage, recordPage]);

  const refresh = useCallback(() => loadPage(recordPage).catch(error => setNotice(String(error))), [loadPage, recordPage]);
  const save = useCallback(async () => {
    const current = draftRef.current;
    if (!config || !current || !dirty.current) return false;
    setBusy(true);
    onBusyChange?.(true);
    try {
      const { revision: _, ...policy } = current;
      const { data } = await requestJSON<{ data: Snapshot }>(config, `/api/v1/ai/auto-research?page=${recordPage}`, { method: 'PUT', body: JSON.stringify(policy) });
      setSnapshot(data);
      setRecordPage(data.records_page);
      setDraft(data.policy);
      dirty.current = false;
      setNotice('设置已保存；新任务继承系统模型，在途任务按原配置结束。');
      return true;
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
      throw error;
    } finally {
      setBusy(false);
      onBusyChange?.(false);
    }
  }, [config, onBusyChange, recordPage]);

  useEffect(() => { onRegisterSave?.(save); return () => onRegisterSave?.(null); }, [onRegisterSave, save]);

  function change<K extends keyof Policy>(key: K, value: Policy[K]) {
    if (!draft) return;
    dirty.current = true;
    setDraft({ ...draft, [key]: value });
  }

  if (view === 'summary') {
    const status = snapshot ? compactLabels[snapshot.status] || snapshot.status : '正在读取状态';
    const statusKind = !snapshot ? 'loading' : !snapshot.policy.enabled ? 'off' : snapshot.status === 'ready' ? 'ready' : 'paused';
    return (
      <section className="automatic-research-summary" aria-label="自动 AI 投研状态">
        <div className="automatic-research-summary-heading">
          <div><strong>自动 AI 投研</strong><span className={`automatic-research-badge ${statusKind}`}>{status}</span></div>
          {onOpenSettings && <button type="button" onClick={onOpenSettings}>系统设置</button>}
        </div>
        <div className="automatic-research-summary-meta">
          <span title={snapshot ? runtimeLabel(snapshot.runtime) : undefined}>{snapshot ? `系统模型 · ${runtimeLabel(snapshot.runtime)}` : '系统模型读取中'}</span>
          <span>{snapshot ? `今日 ${snapshot.usage.runs}/${snapshot.policy.daily_max_runs} 次 · ${snapshot.usage.total_tokens.toLocaleString()}/${snapshot.policy.daily_token_budget.toLocaleString()} Token · 未结算 ${snapshot.usage.unsettled_runs}` : '运行状态读取中'}</span>
        </div>
        <p>继承系统设置的统一 AI 运行时；参数统一在系统设置调整。自动投研默认关闭，只生成模拟策略研究提案。</p>
        {notice && <small role="status">{notice}</small>}
      </section>
    );
  }

  const recordTotal = snapshot?.records_total || 0;
  const pageSize = snapshot?.records_page_size || RECORD_PAGE_SIZE;
  const pageCount = Math.max(1, Math.ceil(recordTotal / pageSize));
  const usageSummary = snapshot
    ? `今日 ${snapshot.usage.runs}/${snapshot.policy.daily_max_runs} 次 · ${snapshot.usage.total_tokens.toLocaleString()}/${snapshot.policy.daily_token_budget.toLocaleString()} Token · 未结算 ${snapshot.usage.unsettled_runs}`
    : '用量读取中';

  return (
    <section className="settings-section automatic-research-settings" aria-label="自动 AI 投研设置">
      <div className="automatic-research-heading">
        <div><h3>自动 AI 投研</h3><p>仅生成模拟策略研究提案，不改变实盘交易或风控。任务继承统一 AI 运行时；模型不可用时暂停，不自动换桥或降档。</p></div>
        {snapshot && <span className={`automatic-research-badge ${!snapshot.policy.enabled ? 'off' : snapshot.status === 'ready' ? 'ready' : 'paused'}`}>{labels[snapshot.status] || snapshot.status}</span>}
      </div>
      {snapshot && <p className="automatic-research-runtime">系统模型：{runtimeLabel(snapshot.runtime)}</p>}
      {draft && <>
        <div className="automatic-research-controls">
          <label className="automatic-research-toggle"><input type="checkbox" aria-label="开启自动 AI 投研" checked={draft.enabled} disabled={busy} onChange={event => change('enabled', event.target.checked)} /><span>开启自动投研</span></label>
          <label>最短间隔（秒）<input type="number" aria-label="自动投研最短间隔" min={300} max={86400} step={60} value={draft.interval_seconds} disabled={busy} onChange={event => change('interval_seconds', Number(event.target.value))} /></label>
          <label>每日最多调用次数<input type="number" aria-label="自动投研每日次数" min={1} max={100} value={draft.daily_max_runs} disabled={busy} onChange={event => change('daily_max_runs', Number(event.target.value))} /></label>
          <label>每日 Token 预算<input type="number" aria-label="自动投研每日 Token 预算" min={1000} max={10000000} step={1000} value={draft.daily_token_budget} disabled={busy} onChange={event => change('daily_token_budget', Number(event.target.value))} /></label>
        </div>
        <div className="automatic-research-save-row"><small>失败尝试计入次数；用量未知或未结算时会暂停后续任务，不自动重试。</small><button className="automatic-research-save" type="button" disabled={busy} onClick={() => void save().catch(() => undefined)}>保存</button></div>
      </>}
      {snapshot && <details className="automatic-research-history">
        <summary><strong>历史用量与运行记录</strong><span>{usageSummary}</span></summary>
        <div className="automatic-research-history-body">
          <div className="automatic-research-usage">
            <div><span>今日调用</span><strong>{snapshot.usage.runs} / {snapshot.policy.daily_max_runs}</strong></div>
            <div><span>已报告 Token</span><strong>{snapshot.usage.total_tokens.toLocaleString()} / {snapshot.policy.daily_token_budget.toLocaleString()}</strong></div>
            <div><span>未结算</span><strong>{snapshot.usage.unsettled_runs}</strong></div>
          </div>
          <div className="automatic-research-records-heading"><h4>最近运行记录 <small>{recordTotal} 条</small></h4><button type="button" disabled={busy} onClick={() => void refresh()}>刷新</button></div>
          <div className="automatic-research-record-list" aria-label="自动投研最近运行记录">
            {snapshot.records.length ? snapshot.records.map(record => (
              <article className="automatic-research-record" key={record.id}>
                <div className="automatic-research-record-heading"><strong>{states[record.status] || record.status}</strong><time>{new Date(record.created_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false })}</time></div>
                <div className="automatic-research-record-meta"><span>{record.model || '历史模型未记录'} / {record.reasoning_effort || '未记录强度'}</span><span>{record.usage_known ? `${record.total_tokens.toLocaleString()} Token` : record.started_at ? '用量未确认' : '未调用或历史未计量'}{record.error_code ? ` · ${record.error_code}` : ''}</span></div>
              </article>
            )) : <p>尚无自动投研记录。</p>}
          </div>
          <div className="automatic-research-pagination">
            <button type="button" disabled={snapshot.records_page <= 1} onClick={() => setRecordPage(page => Math.max(1, page - 1))}>上一页</button>
            <span>第 {snapshot.records_page} / {pageCount} 页 · {recordTotal} 条</span>
            <button type="button" disabled={snapshot.records_page >= pageCount} onClick={() => setRecordPage(page => Math.min(pageCount, page + 1))}>下一页</button>
          </div>
          <small>按北京时间统计；新版本启用前的历史记录可能未计量。</small>
        </div>
      </details>}
      {notice && <p role="status">{notice}</p>}
    </section>
  );
}
