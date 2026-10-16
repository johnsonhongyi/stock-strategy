export type BridgeTargetDisplay = {
	label?: string | null;
	node_id?: string | null;
	workspace_id?: string | null;
};

export function shortBridgeID(value?: string | null): string {
	const raw = String(value || '').trim();
	const compact = raw.replace(/^(?:codex|ws|tk)-/i, '');
	return compact ? compact.slice(0, 8) : '—';
}

export function shortBridgeIdentity(nodeID?: string | null, workspaceID?: string | null): string {
	return shortBridgeID(nodeID) + ' / ' + shortBridgeID(workspaceID);
}

export function bridgeTargetLabel(target: BridgeTargetDisplay): string {
	const rawLabel = String(target.label || '').trim();
	const name = !rawLabel || /^desktop codex(?:\s*\/\s*ats ai bridge)?/i.test(rawLabel)
		? 'Codex'
		: rawLabel;
	return name + ' · ' + shortBridgeIdentity(target.node_id, target.workspace_id);
}