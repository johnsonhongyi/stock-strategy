import type { BackendConfig } from './backend';
import type { LinkageTarget } from './linkage';
export const linkageDefaultEvent = 'watchlist-linkage-default-changed';
export function linkageDefaultKey(config: BackendConfig) {
 return `watchlist-linkage:default:${config.backendUrl.replace(/\/+$/, '')}`;
}
export function readLinkageDefault(config: BackendConfig): string {
 try { return localStorage.getItem(linkageDefaultKey(config)) || ''; } catch { return ''; }
}
export function saveLinkageDefault(config: BackendConfig, node: string) {
 localStorage.setItem(linkageDefaultKey(config), node);
 window.dispatchEvent(new Event(linkageDefaultEvent));
}
export function resolveLinkageDefault(targets: LinkageTarget[], saved: string): string {
 if (saved) return targets.some(target => target.node_id === saved) ? saved : '';
 return targets.length === 1 ? targets[0].node_id : '';
}
