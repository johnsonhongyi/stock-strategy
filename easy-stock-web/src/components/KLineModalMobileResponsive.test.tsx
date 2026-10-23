import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

describe('Mobile KLine modal responsive and anti-overlap layout', () => {
	const stylesCss = readFileSync(new URL('../styles.css', import.meta.url), 'utf8');
	const sbcCss = readFileSync(new URL('./sbc.css', import.meta.url), 'utf8');

	it('ensures .kline-modal uses flex column layout instead of fragile grid row allocation', () => {
		// Checks that kline-modal is refactored from grid-template-rows to flex column,
		// preventing footer from collapsing onto toolbar buttons when height is constrained.
		expect(stylesCss).toMatch(/\.kline-modal\s*\{[^}]*display:\s*flex;/);
		expect(stylesCss).toMatch(/\.kline-modal\s*\{[^}]*flex-direction:\s*column;/);
		expect(stylesCss).not.toMatch(/\.kline-modal\s*\{[^}]*grid-template-rows:\s*auto\s+auto/);
	});

	it('ensures SVG chart width is not locked to min-width: 720px on mobile viewports', () => {
		// When screen is narrow (mobile portrait <= 900px / 620px),
		// min-width must be 0 and width: 100% so it fits within the viewport.
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-width:\s*900px[^)]*\)[\s\S]*?\.kline-chart\s*\{[^}]*min-width:\s*0;/);
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-width:\s*620px[^)]*\)[\s\S]*?\.kline-chart\s*\{[^}]*min-width:\s*0;/);
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-width:\s*900px[^)]*\)[\s\S]*?\.kline-chart-wrap\s*\{[^}]*overflow-x:\s*hidden;/);
	});

	it('ensures .kline-period-controls enables single-row horizontal scrolling on mobile', () => {
		// Period buttons must not wrap into 4 chaotic rows on narrow mobile screens.
		expect(sbcCss).toMatch(/@media\s*\([^)]*max-width:\s*768px[^)]*\)[\s\S]*?\.kline-period-controls\s*\{[^}]*flex-wrap:\s*nowrap;/);
		expect(sbcCss).toMatch(/@media\s*\([^)]*max-width:\s*768px[^)]*\)[\s\S]*?\.kline-period-controls\s*\{[^}]*overflow-x:\s*auto;/);
	});

	it('provides dedicated landscape / constrained height layout for mobile phones', () => {
		// In mobile landscape mode (max-height: 520px), compact spacing must be applied.
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-height:\s*520px[^)]*\)/);
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-height:\s*520px[^)]*\)[\s\S]*?\.kline-modal-quote\s*\{[^}]*grid-template-columns:\s*repeat\(8/);
		expect(sbcCss).toMatch(/@media\s*\([^)]*max-height:\s*520px[^)]*\)/);
	});

	it('ensures quote metrics adapt cleanly to 4 columns on mobile portrait', () => {
		// 8 quote metrics are distributed into 4 columns x 2 rows, preventing screen overflow.
		expect(stylesCss).toMatch(/@media\s*\([^)]*max-width:\s*620px[^)]*\)[\s\S]*?\.kline-modal-quote\s*\{[^}]*grid-template-columns:\s*repeat\(4/);
	});
});
