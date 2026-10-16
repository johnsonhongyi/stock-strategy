export const FONT_SIZE_SPECS = [7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 31, 32] as const;

export type FontSizeSpec = (typeof FONT_SIZE_SPECS)[number];
export type FontSettings = Record<FontSizeSpec, number>;

const STORAGE_KEY = 'easy-stock.font-sizes.v1';
const MIN_FONT_SIZE = 8;
const MAX_FONT_SIZE = 48;
const DEFAULT_FONT_SETTINGS = Object.fromEntries(
	FONT_SIZE_SPECS.map((size) => [size, size === 7 ? 8 : size]),
) as FontSettings;

export function defaultFontSettings(): FontSettings {
	return { ...DEFAULT_FONT_SETTINGS };
}

export function normalizeFontSettings(value: unknown): FontSettings {
	const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {};
	const result = {} as FontSettings;
	for (const size of FONT_SIZE_SPECS) {
		const candidate = saved[String(size)];
		const parsed = typeof candidate === 'number' ? candidate : Number(candidate);
		result[size] = Number.isFinite(parsed)
			? Math.min(MAX_FONT_SIZE, Math.max(MIN_FONT_SIZE, Math.round(parsed)))
			: DEFAULT_FONT_SETTINGS[size];
	}
	return result;
}

export function readFontSettings(): FontSettings {
	try {
		const raw = window.localStorage.getItem(STORAGE_KEY);
		return raw ? normalizeFontSettings(JSON.parse(raw)) : defaultFontSettings();
	} catch {
		return defaultFontSettings();
	}
}

export function applyFontSettings(settings: FontSettings) {
	if (typeof document === 'undefined') return;
	for (const size of FONT_SIZE_SPECS) {
		document.documentElement.style.setProperty(`--ui-font-${size}`, `${settings[size]}px`);
	}
}

export function saveFontSettings(settings: FontSettings): boolean {
	applyFontSettings(settings);
	try {
		window.localStorage.setItem(STORAGE_KEY, JSON.stringify(settings));
		return true;
	} catch {
		return false;
	}
}

export function initializeFontSettings() {
	applyFontSettings(readFontSettings());
}
