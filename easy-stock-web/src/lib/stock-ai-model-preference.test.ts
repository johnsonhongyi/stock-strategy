import { describe, expect, it } from 'vitest';
import { readStockAIModelPreference, saveStockAIModelPreference } from './stock-ai-model-preference';

function memoryStorage() {
	const values = new Map<string, string>();
	return {
		getItem: (key: string) => values.get(key) ?? null,
		setItem: (key: string, value: string) => { values.set(key, value); },
	};
}

describe('desktop bridge model preferences', () => {
	it('keeps Codex and Antigravity model and reasoning choices independent', () => {
		const storage = memoryStorage();
		saveStockAIModelPreference(storage, 'codex_desktop', 'gpt-6.1-sol', 'high', { node_id: 'codex-node', workspace_id: 'codex-ws' });
		saveStockAIModelPreference(storage, 'antigravity_desktop', 'gemini-3.8-pro', 'max', { node_id: 'agv-node', workspace_id: 'agv-ws' });

		expect(readStockAIModelPreference(storage, 'codex_desktop')).toMatchObject({
			model: 'gpt-6.1-sol', reasoning_effort: 'high', node_id: 'codex-node', workspace_id: 'codex-ws',
		});
		expect(readStockAIModelPreference(storage, 'antigravity_desktop')).toMatchObject({
			model: 'gemini-3.8-pro', reasoning_effort: 'max', node_id: 'agv-node', workspace_id: 'agv-ws',
		});
	});

	it('migrates a legacy choice only to its matching bridge', () => {
		const storage = memoryStorage();
		storage.setItem('easy-stock.stock-ai-model.v1', JSON.stringify({
			provider: 'codex_desktop', model: 'gpt-6-luna', reasoning_effort: 'low', node_id: 'node', workspace_id: 'workspace',
		}));

		expect(readStockAIModelPreference(storage, 'codex_desktop').model).toBe('gpt-6-luna');
		expect(readStockAIModelPreference(storage, 'antigravity_desktop')).toMatchObject({ provider: 'antigravity_desktop' });
	});
});
