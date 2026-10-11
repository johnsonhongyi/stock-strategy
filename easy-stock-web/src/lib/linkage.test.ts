import { isLinkageOperationSettled, linkageContextTTL, linkageOperationRefreshDelay, normalizeLinkageStockSymbol, type LinkageOperation } from "./linkage";
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	canReplayWatchlistAddCommand, createLinkageAttentionIntent, createWatchlistAddCommand,
	getLatestLinkageEvents, getLinkageAttention, getLinkageContext, importLinkageSetup, isLinkageAttentionIntentSatisfied, parseLinkageSetupPackage,
	SBC_WATCHLIST_SOURCE, submitLinkageAttentionIntent,
} from './linkage';

afterEach(() => vi.unstubAllGlobals());

describe('TK desktop setup handoff', () => {
	const setupLine = 'SYSTEM_LINKAGE_SETUP_V1=' + JSON.stringify({
		schema_version: 1, kind: 'stock-standalone.system-linkage', node_id: 'tk-a1', workspace_id: 'ws-b2',
		label: 'TK desk', address: '192.168.31.42', port: 26676, hmac_key: 'a'.repeat(64),
	});

	it('extracts the setup package from copied console output', () => {
		expect(parseLinkageSetupPackage(`[info] starting\n${setupLine}\nready`)).toMatchObject({
			node_id: 'tk-a1', workspace_id: 'ws-b2', address: '192.168.31.42', port: 26676,
		});
	});

	it('rejects setup packages with public addresses or malformed keys', () => {
		expect(() => parseLinkageSetupPackage(setupLine.replace('192.168.31.42', '8.8.8.8'))).toThrow('内网地址');
		expect(() => parseLinkageSetupPackage(setupLine.replace('a'.repeat(64), 'bad'))).toThrow('HMAC 密钥');
	});

	it('registers the package through the authenticated Go API and returns connection status', async () => {
		const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
			node_id: 'tk-a1', workspace_id: 'ws-b2', target_registered: true, desktop_status: 'connected',
		})));
		vi.stubGlobal('fetch', fetchMock);
		vi.stubGlobal('window', { location: { origin: 'http://192.168.31.5:20073' } });
		const setup = parseLinkageSetupPackage(setupLine);
		const result = await importLinkageSetup({ backendUrl: 'http://192.168.31.5:20081/', token: 'session-token' }, setup);
		expect(String(fetchMock.mock.calls[0][0])).toBe('http://192.168.31.5:20073/api/v1/linkage/targets/import');
		const request = fetchMock.mock.calls[0][1] as RequestInit;
		expect(result.desktop_status).toBe('connected');
		expect(new Headers(request.headers).get('Authorization')).toBe('Bearer session-token');
		expect(JSON.parse(request.body as string)).toMatchObject({
			node_id: 'tk-a1', backend_base_url: 'http://192.168.31.5:20081',
		});
	});

	it('keeps the configured API when the page and backend are on different hosts', async () => {
		const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
			node_id: 'tk-a1', workspace_id: 'ws-b2', target_registered: true, desktop_status: 'connected',
		})));
		vi.stubGlobal('fetch', fetchMock);
		vi.stubGlobal('window', { location: { origin: 'http://192.168.31.9:20073' } });
		await importLinkageSetup({ backendUrl: 'http://192.168.31.5:20081', token: '' }, parseLinkageSetupPackage(setupLine));
		expect(String(fetchMock.mock.calls[0][0])).toBe('http://192.168.31.5:20081/api/v1/linkage/targets/import');
	});
});

describe('SBC desktop watchlist command contract', () => {
	it('accepts only correctly exchanged Shanghai and Shenzhen A-share symbols', () => {
		expect(normalizeLinkageStockSymbol('600000')).toBe('600000.SH');
		expect(normalizeLinkageStockSymbol('000001.sz')).toBe('000001.SZ');
		expect(normalizeLinkageStockSymbol('1')).toBe('000001.SZ');
		for (const symbol of ['510300.SH', '399001.SZ', '900901.SH', '200001.SZ', '600000.SZ', '600000.BJ']) {
			expect(normalizeLinkageStockSymbol(symbol)).toBe('');
		}
	});

	it('uses the source namespace accepted by the Go command validator', () => {
		const command = createWatchlistAddCommand({
			workspaceID: 'workspace', targetNodeID: 'tk-1', symbol: '600000.SH',
			source: SBC_WATCHLIST_SOURCE, reason: 'SBC chart', contextRefs: [],
		});

		expect(command.source).toBe('stock-strategy.sbc');
		expect(command.source.startsWith('stock-strategy.')).toBe(true);
	});

	it('reuses an unexpired command after an operation lookup miss', () => {
		const now = Date.parse('2026-10-10T00:00:00Z');
		const command = createWatchlistAddCommand({
			workspaceID: 'workspace', targetNodeID: 'tk-1', symbol: '600000.SH',
			source: SBC_WATCHLIST_SOURCE, reason: 'SBC chart', contextRefs: [],
		});
		command.created_at = new Date(now).toISOString();
		command.expires_at = new Date(now + 60_000).toISOString();

		expect(canReplayWatchlistAddCommand(command, now)).toBe(true);
		expect(canReplayWatchlistAddCommand({ ...command, expires_at: new Date(now).toISOString() }, now)).toBe(false);
		expect(canReplayWatchlistAddCommand({ ...command, source: 'sbc' }, now)).toBe(false);
	});

	it('creates a revisioned source-level attention intent', () => {
		const intent = createLinkageAttentionIntent({
			workspace_id: 'desk', node_id: 'tk-1', symbol: '600000.SH',
			source: SBC_WATCHLIST_SOURCE, action: 'remove_source', base_revision: 12,
		});

		expect(intent.intent_id).toBeTruthy();
		expect(intent).toMatchObject({
			workspace_id: 'desk', node_id: 'tk-1', symbol: '600000.SH',
			source: 'stock-strategy.sbc', action: 'remove_source', base_revision: 12,
		});
	});

	it('reads a workspace snapshot and posts the source intent to Go', async () => {
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify({ schema_version: 1, workspace_id: 'desk', node_id: 'tk-1', revision: 12, sources: [], tombstones: [] })))
			.mockResolvedValueOnce(new Response(JSON.stringify({ intent_id: 'intent-1', status: 'applied', revision: 13, replayed: false }), { status: 201 }));
		vi.stubGlobal('fetch', fetchMock);
		const config = { backendUrl: 'http://127.0.0.1:8080', token: 'test-token' };
		const intent = createLinkageAttentionIntent({
			workspace_id: 'desk', node_id: 'tk-1', symbol: '600000.SH',
			source: SBC_WATCHLIST_SOURCE, action: 'add', base_revision: 12,
		});

		const snapshot = await getLinkageAttention(config, 'desk', 'tk-1');
		const result = await submitLinkageAttentionIntent(config, intent);

		expect(snapshot.revision).toBe(12);
		expect(result.revision).toBe(13);
		expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/linkage/attention?workspace_id=desk&node_id=tk-1');
		expect(String(fetchMock.mock.calls[1][0])).toContain('/api/v1/linkage/attention/intents');
		expect(JSON.parse((fetchMock.mock.calls[1][1] as RequestInit).body as string)).toMatchObject({
			intent_id: intent.intent_id, action: 'add', base_revision: 12,
		});
	});

	it('distinguishes source removal from clearing all sources', () => {
		const snapshot = {
			schema_version: 1, workspace_id: 'desk', node_id: 'tk-1', revision: 13,
			sources: [
				{ symbol: '600000.SH', source: 'manual', active: true, revision: 8, intent_id: 'manual-1', updated_at: '' },
				{ symbol: '600000.SH', source: SBC_WATCHLIST_SOURCE, active: false, revision: 13, intent_id: 'remove-1', updated_at: '' },
			],
			tombstones: [],
		};

		expect(isLinkageAttentionIntentSatisfied(snapshot, '600000.SH', SBC_WATCHLIST_SOURCE, 'remove_source')).toBe(true);
		expect(isLinkageAttentionIntentSatisfied(snapshot, '600000.SH', 'manual', 'remove_all')).toBe(false);
	});
});


describe('watchlist receipt lifecycle', () => {
    const operation = (status: string, tk = 'synced', quote = 'market_closed'): LinkageOperation => ({
        operation_id: 'op', request_id: 'req', status,
        deliveries: [{ node_id: 'tk', workspace_id: 'ws', status, result: { status, result: {
            saved: { persisted: true }, consumers: {
                TK: { status: tk, quote_status: quote }, ATS: { status: 'synced', quote_status: 'market_closed' },
            },
        } } }],
    });
    it('keeps polling saved commands when a consumer starts late or quotes are pending', () => {
        expect(linkageOperationRefreshDelay(operation('applied', 'not_running'))).toBe(30000);
        expect(linkageOperationRefreshDelay(operation('already_present', 'synced', 'pending_fetch'))).toBe(5000);
        expect(linkageOperationRefreshDelay(operation('applied', 'pending'))).toBe(2000);
    });
    it('continues low-rate refresh after successful consumption and stops rejected commands', () => {
        expect(linkageOperationRefreshDelay(operation('applied'))).toBe(30000);
        expect(linkageOperationRefreshDelay(operation('rejected'))).toBe(0);
    });
    it('does not settle while a consumer is offline or quotes are still pending', () => {
        expect(isLinkageOperationSettled(operation('applied', 'not_running'))).toBe(false);
        expect(isLinkageOperationSettled(operation('applied', 'synced', 'pending_fetch'))).toBe(false);
        expect(isLinkageOperationSettled(operation('applied'))).toBe(true);
        expect(isLinkageOperationSettled(operation('rejected'))).toBe(true);
    });
});


describe('linkage context freshness', () => {
	it('uses the server issued TTL and rejects invalid expiry metadata', () => {
		expect(linkageContextTTL({
			received_at: '2026-10-10T01:00:00Z', expires_at: '2026-10-10T01:00:30Z',
		})).toBe(30_000);
		expect(linkageContextTTL({
			received_at: '2026-10-10T01:00:00Z', expires_at: '2026-10-10T01:00:00Z',
		})).toBe(0);
		expect(linkageContextTTL({ received_at: 'invalid', expires_at: 'invalid' })).toBe(0);
	});
});

describe('workspace scoped context reads', () => {
	it('includes the selected workspace on context and event requests', async () => {
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(new Response(JSON.stringify({ schema_version: 1, workspace_id: 'ws-b2' })))
			.mockResolvedValueOnce(new Response(JSON.stringify({ schema_version: 1, events: [] })));
		vi.stubGlobal('fetch', fetchMock);
		const config = { backendUrl: 'http://192.168.31.5:20081', token: 'session-token' };
		await getLinkageContext(config, 'ws-b2');
		await getLatestLinkageEvents(config, 12, 'ws-b2');
		expect(new URL(String(fetchMock.mock.calls[0][0])).searchParams.get('workspace_id')).toBe('ws-b2');
		expect(new URL(String(fetchMock.mock.calls[1][0])).searchParams.get('workspace_id')).toBe('ws-b2');
	});
});
