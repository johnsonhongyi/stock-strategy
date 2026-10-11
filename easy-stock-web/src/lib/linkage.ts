import { requestJSON, type BackendConfig } from './backend';

export const SBC_WATCHLIST_SOURCE = 'stock-strategy.sbc' as const;

export type LinkageTarget = { node_id: string; workspace_id: string; label?: string; status: string };
export type LinkageSetupPackage = {
	schema_version: 1;
	kind: 'stock-standalone.system-linkage';
	node_id: string;
	workspace_id: string;
	label: string;
	address: string;
	port: number;
	hmac_key: string;
};

export type LinkageSetupResult = {
	node_id: string;
	workspace_id: string;
	target_registered: boolean;
	desktop_status: 'connected' | 'pending';
	desktop_error?: string;
};
export type WatchlistAddCommand = {
	schema_version: 1;
	type: 'WATCHLIST_ADD';
	request_id: string;
	operation_id: string;
	workspace_id: string;
	target_node_ids: string[];
	symbol: string;
	instrument_type: 'stock';
	source: string;
	reason: string;
	context_refs: string[];
	observed_trade_date?: string;
	created_at: string;
	expires_at: string;
	sources?: Array<{ id: string; name: string; status: string; freshness: string; error?: string }>;
	coverage?: Array<{ scope: string; observed_count: number; total_expected?: number; coverage?: number; complete: boolean; is_sample: boolean; source: string }>;
};

export type LinkageOperation = {
	operation_id: string;
	request_id: string;
	status: string;
	deliveries: Array<{
		node_id: string;
		workspace_id: string;
		status: string;
		result?: {
			status?: string;
			result?: {
				saved?: { status?: string; persisted?: boolean };
				consumers?: Record<string, { status?: string; quote_status?: string }>;
			};
		};
	}>;
};

export type LinkageContextSnapshot = {
	schema_version: number;
	snapshot_id: string;
	trade_date?: string;
	session_phase?: string;
	freshness: string;
	expires_at: string;
	sources?: Array<{ id: string; name: string; status: string; freshness: string; error?: string }>;
	coverage?: Array<{ scope: string; observed_count: number; total_expected?: number; coverage?: number; complete: boolean; is_sample: boolean; source: string }>;
	themes: Array<{ theme_id: string; name: string; change_percent?: number; trend_stage?: string; trend_score?: number; daily_strength_score?: number; five_day_strength_score?: number; active_days?: number; members: Array<{ symbol: string }> }>;
	heat: { stale?: boolean; updated_at?: string; stocks: Array<{ symbol: string; ranks: Record<string, number> }> };
	ladder?: { current: { levels: Array<{ label: string; stocks: Array<{ symbol: string; streak: number }> }> } };
	evidence?: Array<{ source_id: string; source_name: string; snapshot_id: string; trade_date: string; as_of: string; freshness: string; payload: { state?: string; source_freshness?: string; candidate_count?: number; candidate_codes?: string[]; candidate_codes_complete?: boolean; candidates?: Array<{ symbol: string; name?: string; grade?: string; score?: number; classification?: string }>; coverage?: { scope?: string; observed_count?: number; total_count?: number; complete?: boolean; is_sample?: boolean; source?: string }; roles?: Array<{ symbol: string; name?: string; role: string; sector?: string; classification?: string }> } }>;
};

export type LinkageContextEvent = {
	event_id: string;
	event_type: string;
	trade_date: string;
	occurred_at: string;
	payload: Record<string, unknown>;
};

export type LinkageAttentionIntent = {
	intent_id: string;
	workspace_id: string;
	node_id: string;
	symbol: string;
	source: string;
	action: 'add' | 'remove_source' | 'remove_all';
	base_revision: number;
};

export type LinkageAttentionSnapshot = {
	schema_version: number;
	workspace_id: string;
	node_id: string;
	revision: number;
	sources: Array<{
		symbol: string;
		source: string;
		active: boolean;
		revision: number;
		intent_id: string;
		updated_at: string;
	}>;
	tombstones: Array<{ symbol: string; revision: number; intent_id: string; updated_at: string }>;
};

export type LinkageAttentionIntentResult = {
	intent_id: string;
	status: string;
	revision: number;
	replayed: boolean;
};

export function isLinkageAttentionIntentSatisfied(
	snapshot: LinkageAttentionSnapshot, symbol: string, source: string, action: LinkageAttentionIntent['action'],
) {
	const active = snapshot.sources.filter((item) => item.symbol === symbol && item.active);
	if (action === 'add') return active.some((item) => item.source === source);
	if (action === 'remove_source') return !active.some((item) => item.source === source);
	return active.length === 0;
}

function requestID() {
	if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
	const bytes = crypto.getRandomValues(new Uint8Array(16));
	return Array.from(bytes, (value: number) => value.toString(16).padStart(2, '0')).join('');
}

export function createWatchlistAddCommand(input: {
	workspaceID: string;
	targetNodeID: string;
	symbol: string;
	source: string;
	reason: string;
	contextRefs: string[];
	observedTradeDate?: string;
}): WatchlistAddCommand {
	const now = new Date();
	return {
		schema_version: 1,
		type: 'WATCHLIST_ADD',
		request_id: requestID(),
		operation_id: requestID(),
		workspace_id: input.workspaceID,
		target_node_ids: [input.targetNodeID],
		symbol: input.symbol.toUpperCase(),
		instrument_type: 'stock',
		source: input.source,
		reason: input.reason.slice(0, 280),
		context_refs: input.contextRefs.slice(0, 10).map((value) => value.slice(0, 256)),
		observed_trade_date: input.observedTradeDate || undefined,
		created_at: now.toISOString(),
		expires_at: new Date(now.getTime() + 4 * 60_000).toISOString(),
	};
}

export function canReplayWatchlistAddCommand(command: WatchlistAddCommand, now = Date.now()) {
	const expiresAt = Date.parse(command.expires_at);
	return command.source === SBC_WATCHLIST_SOURCE && Number.isFinite(expiresAt) && expiresAt > now;
}

export function listLinkageTargets(config: BackendConfig) {
	return requestJSON<{ schema_version: number; targets: LinkageTarget[] }>(config, '/api/v1/linkage/targets');
}

export function parseLinkageSetupPackage(consoleText: string): LinkageSetupPackage {
	const marker = 'SYSTEM_LINKAGE_SETUP_V1=';
	const lines = consoleText.split(/\r?\n/).filter((line) => line.includes(marker));
	if (!lines.length) throw new Error('未找到 TK 联动配置行');
	const raw = lines[lines.length - 1].slice(lines[lines.length - 1].indexOf(marker) + marker.length).trim();
	let value: unknown;
	try { value = JSON.parse(raw); } catch { throw new Error('TK 联动配置行不是有效 JSON'); }
	if (!value || typeof value !== 'object') throw new Error('TK 联动配置格式无效');
	const setup = value as Record<string, unknown>;
	const ipv4 = typeof setup.address === 'string' ? setup.address.split('.').map(Number) : [];
	const isIPv4 = ipv4.length === 4 && ipv4.every((part) => Number.isInteger(part) && part >= 0 && part <= 255);
	const isPrivateIPv4 = isIPv4 && (ipv4[0] === 10 || (ipv4[0] === 172 && ipv4[1] >= 16 && ipv4[1] <= 31) ||
		(ipv4[0] === 192 && ipv4[1] === 168) || ipv4[0] === 127);
	if (setup.schema_version !== 1 || setup.kind !== 'stock-standalone.system-linkage' ||
		typeof setup.node_id !== 'string' || !/^[A-Za-z0-9_.-]{1,96}$/.test(setup.node_id) ||
		typeof setup.workspace_id !== 'string' || !/^[A-Za-z0-9_.-]{1,96}$/.test(setup.workspace_id) ||
		typeof setup.label !== 'string' || !isPrivateIPv4 || !Number.isInteger(setup.port) ||
		Number(setup.port) < 1 || Number(setup.port) > 65535 || typeof setup.hmac_key !== 'string' ||
		!/^[0-9a-fA-F]{64}$/.test(setup.hmac_key)) {
		throw new Error('TK 联动配置缺少有效身份、内网地址、端口或 HMAC 密钥');
	}
	return setup as unknown as LinkageSetupPackage;
}

export function importLinkageSetup(config: BackendConfig, setup: LinkageSetupPackage) {
	return requestJSON<LinkageSetupResult>(config, '/api/v1/linkage/targets/import', {
		method: 'POST', headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ ...setup, backend_base_url: config.backendUrl.replace(/\/+$/, '') }),
	});
}

export function submitWatchlistAdd(config: BackendConfig, command: WatchlistAddCommand) {
	return requestJSON<{ operation: LinkageOperation; replayed: boolean }>(config, '/api/v1/linkage/commands', {
		method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(command),
	});
}

export function getLinkageOperation(config: BackendConfig, operationID: string) {
	return requestJSON<LinkageOperation>(config, `/api/v1/linkage/operations/${encodeURIComponent(operationID)}`);
}

export function getLinkageContext(config: BackendConfig) {
	return requestJSON<LinkageContextSnapshot>(config, '/api/v1/linkage/context');
}

export function getLatestLinkageEvents(config: BackendConfig, limit = 12) {
	return requestJSON<{ schema_version: number; events: LinkageContextEvent[] }>(
		config, `/api/v1/linkage/events?latest=true&limit=${Math.max(1, Math.min(50, limit))}`,
	);
}

export function createLinkageAttentionIntent(input: Omit<LinkageAttentionIntent, 'intent_id'>): LinkageAttentionIntent {
	return { ...input, intent_id: requestID() };
}

export function getLinkageAttention(config: BackendConfig, workspaceID: string, nodeID: string) {
	const query = new URLSearchParams({ workspace_id: workspaceID, node_id: nodeID });
	return requestJSON<LinkageAttentionSnapshot>(config, `/api/v1/linkage/attention?${query.toString()}`);
}

export function submitLinkageAttentionIntent(config: BackendConfig, intent: LinkageAttentionIntent) {
	return requestJSON<LinkageAttentionIntentResult>(config, '/api/v1/linkage/attention/intents', {
		method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(intent),
	});
}
