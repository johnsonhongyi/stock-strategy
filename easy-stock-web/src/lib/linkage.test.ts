import { afterEach, describe, expect, it, vi } from 'vitest';
import {
	canReplayWatchlistAddCommand, createLinkageAttentionIntent, createWatchlistAddCommand,
	getLinkageAttention, isLinkageAttentionIntentSatisfied, SBC_WATCHLIST_SOURCE, submitLinkageAttentionIntent,
} from './linkage';

afterEach(() => vi.unstubAllGlobals());

describe('SBC desktop watchlist command contract', () => {
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
