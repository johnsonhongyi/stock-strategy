import { requestJSON, type BackendConfig } from './backend';
import type { LinkageTarget } from './linkage';
export const linkageDefaultEvent = 'watchlist-linkage-default-changed';
const defaults = new Map<string, string>();
export function linkageDefaultKey(config: BackendConfig) {
 return `watchlist-linkage:default:${config.backendUrl.replace(/\/+$/, '')}`;
}
export function readLinkageDefault(config: BackendConfig): string {
 const key = linkageDefaultKey(config);
 if (defaults.has(key)) return defaults.get(key)!;
 try { return localStorage.getItem(key) || ''; } catch { return ''; }
}
export function cacheLinkageDefault(config: BackendConfig, node: string) {
 defaults.set(linkageDefaultKey(config), node);
 try { localStorage.setItem(linkageDefaultKey(config), node); } catch { /* Server remains authoritative. */ }
 window.dispatchEvent(new Event(linkageDefaultEvent));
}
export async function saveLinkageDefault(config: BackendConfig, node: string) {
 await requestJSON(config, '/api/v1/linkage/default', { method:'PUT', body:JSON.stringify({node_id:node}) });
 cacheLinkageDefault(config, node);
}
export function resolveLinkageDefault(targets: LinkageTarget[], saved: string): string {
 if (saved) return targets.some(target => target.node_id === saved) ? saved : '';
 return targets.length === 1 ? targets[0].node_id : '';
}
