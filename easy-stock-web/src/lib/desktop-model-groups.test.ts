import { describe, expect, it } from 'vitest';
import { desktopModelGroups, resolveDesktopModelSelection, resolveAvailableDesktopModelSelection, selectedDesktopModelGroup } from './desktop-model-groups';

const models = [
  ...['high', 'medium', 'low'].map(effort => ({ id: `gemini-3.8-flash-${effort}`, name: `Gemini 3.8 Flash (${effort})`, efforts: [effort] })),
  { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6 (Thinking)', efforts: ['auto'] },
  { id: 'claude-opus-4-6-thinking', name: 'Claude Opus 4.6 (Thinking)', efforts: ['auto'] },
];

describe('desktop model display and execution mapping', () => {
  it('shows three model families and discovers available Flash efforts', () => {
    const groups = desktopModelGroups(models, 'antigravity_desktop');
    expect(groups.map(group => group.name)).toEqual(['Gemini 3.8 Flash', 'Claude Sonnet 4.6', 'Claude Opus 4.6']);
    expect(groups[0].efforts).toEqual(['low', 'medium', 'high']);
    expect(selectedDesktopModelGroup(groups, 'gemini-3.8-flash-medium')?.id).toBe('gemini-3.8-flash');
  });
  it('maps every effort to its real CLI ID including restored preferences', () => {
    for (const effort of ['low', 'medium', 'high']) {
      expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'gemini-3.8-flash-medium', effort))
        .toEqual({ model: `gemini-3.8-flash-${effort}`, reasoning_effort: effort });
    }
    expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'gemini-3.8-flash', 'high')?.model).toBe('gemini-3.8-flash-high');
  });
  it('does not invent absent effort variants or silently select another family', () => {
    const limited = models.filter(model => model.id !== 'gemini-3.8-flash-high');
    expect(desktopModelGroups(limited, 'antigravity_desktop')[0].efforts).toEqual(['low', 'medium']);
    expect(resolveDesktopModelSelection(limited, 'antigravity_desktop', 'gemini-3.8-flash-medium', 'high'))
      .toEqual({ model: 'gemini-3.8-flash-low', reasoning_effort: 'low' });
    expect(resolveDesktopModelSelection([], 'antigravity_desktop', 'missing', 'low')).toBeNull();
  });
  it('uses the model default when the live catalog declares no effort tiers', () => {
    expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'claude-sonnet-4-6', 'high'))
      .toEqual({ model: 'claude-sonnet-4-6', reasoning_effort: 'auto' });
    expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'claude-opus-4-6-thinking', 'max'))
      .toEqual({ model: 'claude-opus-4-6-thinking', reasoning_effort: 'auto' });
  });
  it('preserves a model family after a previously selected tier is removed', () => {
    const limited = models.filter(model => model.id !== 'gemini-3.8-flash-high');
    expect(resolveAvailableDesktopModelSelection(limited, 'antigravity_desktop', 'gemini-3.8-flash-high', 'high'))
      .toEqual({ model: 'gemini-3.8-flash-low', reasoning_effort: 'low' });
    expect(resolveAvailableDesktopModelSelection([], 'antigravity_desktop', 'removed', 'high')).toBeNull();
  });
  it('preserves Codex names, IDs, and effort ordering without mutating the catalog', () => {
    const codex = [{ id: 'gpt-6-luna', name: 'Luna', efforts: ['medium', 'low', 'high'] }];
    expect(desktopModelGroups(codex, 'codex_desktop')[0]).toMatchObject(codex[0]);
    expect(resolveDesktopModelSelection(codex, 'codex_desktop', 'gpt-6-luna', 'high')).toEqual({ model: 'gpt-6-luna', reasoning_effort: 'high' });
    expect(models[0].efforts).toEqual(['high']);
  });
});
