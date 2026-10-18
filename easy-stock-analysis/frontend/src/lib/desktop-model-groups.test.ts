import { describe, expect, it } from 'vitest';
import { desktopModelGroups, resolveDesktopModelSelection } from './desktop-model-groups';

describe('Antigravity desktop model selection', () => {
  const models = [
    { id: 'gemini-3.8-flash-high', name: 'Gemini 3.8 Flash (High)', efforts: ['high'] },
    { id: 'gemini-3.8-flash-medium', name: 'Gemini 3.8 Flash (Medium)', efforts: ['medium'] },
    { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6 (Thinking)', efforts: ['auto'] },
  ];

  it('groups concrete effort variants under one model family', () => {
    const groups = desktopModelGroups(models, 'antigravity_desktop');
    expect(groups.map(({ id, name, efforts }) => ({ id, name, efforts }))).toEqual([
      { id: 'gemini-3.8-flash', name: 'Gemini 3.8 Flash', efforts: ['medium', 'high'] },
      { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', efforts: ['auto'] },
    ]);
    expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'gemini-3.8-flash', 'medium'))
      .toEqual({ model: 'gemini-3.8-flash-medium', reasoning_effort: 'medium' });
  });

  it('uses the CLI default for a model with no advertised effort variants', () => {
    expect(resolveDesktopModelSelection(models, 'antigravity_desktop', 'claude-sonnet-4-6', 'high'))
      .toEqual({ model: 'claude-sonnet-4-6', reasoning_effort: 'auto' });
  });
});
