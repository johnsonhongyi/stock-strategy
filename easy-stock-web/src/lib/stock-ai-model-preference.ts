export type StockAIProvider = 'hermes' | 'codex_desktop' | 'antigravity_desktop';
export type StockAIModelPreference = {
	provider?: StockAIProvider;
	model?: string;
	reasoning_effort?: string;
	node_id?: string;
	workspace_id?: string;
};

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

const activePreferenceKey = 'easy-stock.stock-ai-model.v1';
const providerPreferenceKeys: Partial<Record<StockAIProvider, string>> = {
	codex_desktop: 'easy-stock.stock-ai-model.codex.v1',
	antigravity_desktop: 'easy-stock.stock-ai-model.antigravity.v1',
};

function parsePreference(raw: string | null): StockAIModelPreference {
	if (!raw) return {};
	try {
		const value = JSON.parse(raw);
		return value && typeof value === 'object' && !Array.isArray(value) ? value as StockAIModelPreference : {};
	} catch {
		return {};
	}
}

export function readStockAIModelPreference(storage: StorageLike, provider?: StockAIProvider): StockAIModelPreference {
	const active = parsePreference(storage.getItem(activePreferenceKey));
	if (!provider || provider === 'hermes') return active;
	const key = providerPreferenceKeys[provider];
	const saved = key ? parsePreference(storage.getItem(key)) : {};
	if (saved.provider === provider || saved.model || saved.reasoning_effort || saved.node_id || saved.workspace_id) {
		return { ...saved, provider };
	}
	// Migrate a legacy v1 selection only when it belongs to this bridge.
	return active.provider === provider ? active : { provider };
}

export function saveStockAIModelPreference(storage: StorageLike, provider: StockAIProvider, model: string,
	effort: string, target: { node_id: string; workspace_id: string } | null) {
	const preference: StockAIModelPreference = {
		provider,
		model,
		reasoning_effort: effort,
		node_id: target?.node_id || '',
		workspace_id: target?.workspace_id || '',
	};
	const serialized = JSON.stringify(preference);
	const providerKey = providerPreferenceKeys[provider];
	if (providerKey && (model || effort || target)) storage.setItem(providerKey, serialized);
	storage.setItem(activePreferenceKey, serialized);
}
