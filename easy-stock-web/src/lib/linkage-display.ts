export type BridgeTargetDisplay = {
	label?: string | null;
	node_id?: string | null;
	provider?: string | null;
	workspace_id?: string | null;
};

export function shortBridgeID(value?: string | null): string {
	const raw = String(value || '').trim();
	const compact = raw.replace(/^(?:codex|agv|ws|tk)-/i, '');
	return compact ? compact.slice(0, 8) : '—';
}

export function shortBridgeIdentity(nodeID?: string | null, workspaceID?: string | null): string {
	return shortBridgeID(nodeID) + ' / ' + shortBridgeID(workspaceID);
}

export function bridgeTargetLabel(target: BridgeTargetDisplay): string {
	const rawLabel = String(target.label || '').trim();
	const antigravity = String(target.provider || '').toLowerCase().includes('antigravity')
		|| String(target.node_id || '').toLowerCase().startsWith('agv-');
	const name = antigravity
		? 'Antigravity'
		: !rawLabel || /^desktop codex(?:\s*\/\s*ats ai bridge)?/i.test(rawLabel)
			? 'Codex'
		: rawLabel;
	return name + ' · ' + shortBridgeIdentity(target.node_id, target.workspace_id);
}
