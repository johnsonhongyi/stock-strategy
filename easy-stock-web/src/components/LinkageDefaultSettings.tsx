import { useEffect, useState } from 'react';
import type { BackendConfig } from '../lib/backend';
import { listLinkageTargets, type LinkageTarget } from '../lib/linkage';
import { readLinkageDefault, saveLinkageDefault } from '../lib/linkage-default';
export function LinkageDefaultSettings({ config, open }: { config: BackendConfig | null; open: boolean }) {
 const [targets, setTargets] = useState<LinkageTarget[]>([]);
 const [selected, setSelected] = useState('');
 const [message, setMessage] = useState('');
 useEffect(() => {
  let active = true;
  if (config && open) {
   setSelected(readLinkageDefault(config));
   listLinkageTargets(config).then(result => { if (active) { setTargets(result.targets || []); setSelected(result.default_node_id || ''); } })
    .catch(error => { if (active) setMessage(error instanceof Error ? error.message : '桌面列表读取失败'); });
  }
  return () => { active = false; };
 }, [config, open]);
 return <section className="settings-section"><h3>默认重点关注联动桌面</h3>
  <p>选择实际运行 TK / ATS 的节点。Codex 桥用于 AI 研究；关注保存成功与桌面消费回执分别确认。</p>
  <select aria-label="默认重点关注联动桌面" value={selected} disabled={!config} onChange={async event => {
   if (!config) return;
   try { const node = event.target.value; await saveLinkageDefault(config, node); setSelected(node); setMessage('默认桌面已保存，同一后端的所有浏览器和股票共用。'); }
   catch { setMessage('默认设置保存失败，请检查管理员权限与后端连接。'); }
  }}><option value="">不指定默认桌面</option>
   {selected && !targets.some(target => target.node_id === selected) && <option value={selected}>原默认节点不可用，请重新配置</option>}
   {targets.map(target => <option key={target.node_id} value={target.node_id}>{target.label || target.node_id} · {target.workspace_id}</option>)}
  </select><p role="status">{message}</p>
 </section>;
}
