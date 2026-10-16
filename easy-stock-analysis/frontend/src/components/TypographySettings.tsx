import { RotateCcw, Type } from 'lucide-react';
import { useState } from 'react';
import {
	defaultFontSettings,
	FONT_SIZE_SPECS,
	readFontSettings,
	saveFontSettings,
	type FontSettings,
	type FontSizeSpec,
} from '../lib/font-settings';

export function TypographySettings() {
	const [sizes, setSizes] = useState<FontSettings>(readFontSettings);
	const [saveState, setSaveState] = useState('更改后自动保存在当前浏览器');

	const updateSize = (spec: FontSizeSpec, value: number) => {
		const next = { ...sizes, [spec]: Math.min(48, Math.max(8, Math.round(value))) };
		setSizes(next);
		setSaveState(saveFontSettings(next) ? '已自动保存' : '当前浏览器存储不可用，本次预览仍生效');
	};

	const resetAll = () => {
		const next = defaultFontSettings();
		setSizes(next);
		setSaveState(saveFontSettings(next) ? '已恢复推荐默认值并保存' : '当前浏览器存储不可用，本次预览仍生效');
	};

	return (
		<section className="typography-settings" aria-labelledby="typography-settings-title">
			<header className="typography-settings-header">
				<div>
					<span className="system-status-kicker"><Type size={15} aria-hidden="true" /> 页面显示</span>
					<h2 id="typography-settings-title">字体规格</h2>
					<p>每种原始字号独立调整，拖动后页面即时预览并自动保存。</p>
				</div>
				<button type="button" className="typography-reset" onClick={resetAll}>
					<RotateCcw size={15} aria-hidden="true" /> 恢复默认
				</button>
			</header>

			<div className="typography-preview" aria-live="polite">
				<div>
					<strong className="typography-preview-title">页面标题预览</strong>
					<p className="typography-preview-body">正文预览：字号变化会立即应用到当前页面及其他工作区。</p>
					<small className="typography-preview-small">辅助说明预览 · 当前最小字号 {sizes[7]}px</small>
				</div>
				<span className="typography-save-state">{saveState}</span>
			</div>

			<div className="typography-size-grid">
				{FONT_SIZE_SPECS.map((spec) => (
					<div className="typography-size-row" key={spec}>
						<label htmlFor={`font-size-${spec}`}>
							<span>{spec}px 规格{spec === 7 ? ' · 最小字号' : ''}</span>
							<strong>{sizes[spec]}px</strong>
						</label>
						<input
							id={`font-size-${spec}`}
							type="range"
							min={8}
							max={48}
							step={1}
							value={sizes[spec]}
							aria-label={`调整原 ${spec}px 字体规格`}
							onChange={(event) => updateSize(spec, Number(event.currentTarget.value))}
						/>
					</div>
				))}
			</div>
		</section>
	);
}
