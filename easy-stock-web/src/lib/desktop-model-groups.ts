import type { ChatRuntime } from './desktop-chat';

type Model = ChatRuntime['models'][number];
export type DesktopModelGroup = Model & { variants: Model[] };
const effortOrder = ['auto', 'low', 'medium', 'high', 'xhigh', 'max'];

export function desktopModelGroups(models: Model[], provider: string): DesktopModelGroup[] {
  const groups = new Map<string, DesktopModelGroup>();
  for (const model of models) {
    const agv = provider === 'antigravity_desktop';
    const suffix = agv ? model.id.match(/-(low|medium|high|xhigh|max)$/) : null;
    const fixed = suffix && model.efforts.length === 1 && model.efforts[0] === suffix[1];
    const id = fixed ? model.id.slice(0, -suffix[0].length) : model.id;
    const name = agv ? (model.name || model.id).replace(/\s*\((low|medium|high|xhigh|max|thinking)\)\s*$/i, '') : model.name;
    const group = groups.get(id);
    if (group) {
      group.variants.push(model);
      group.efforts = [...new Set([...group.efforts, ...model.efforts])];
    } else {
      groups.set(id, { id, name, efforts: [...model.efforts], variants: [model] });
    }
  }
  for (const group of groups.values()) {
    if (provider === 'antigravity_desktop') group.efforts.sort((a, b) => effortOrder.indexOf(a) - effortOrder.indexOf(b));
  }
  return [...groups.values()];
}

export function selectedDesktopModelGroup(groups: DesktopModelGroup[], model: string) {
  return groups.find(group => group.id === model || group.variants.some(variant => variant.id === model));
}

export function resolveDesktopModelSelection(models: Model[], provider: string, model: string, effort: string) {
  const groups = desktopModelGroups(models, provider);
  const family = provider === 'antigravity_desktop' ? model.replace(/-(low|medium|high|xhigh|max)$/, '') : model;
  const group = selectedDesktopModelGroup(groups, model) || groups.find(item => item.id === family);
  if (!group || !group.efforts.length) return null;
  const selectedEffort = group.efforts.includes(effort) ? effort : group.efforts[0];
  const variant = group.variants.find(item => item.efforts.includes(selectedEffort));
  return variant ? { model: variant.id, reasoning_effort: selectedEffort } : null;
}

export function resolveAvailableDesktopModelSelection(models: Model[], provider: string, model: string, effort: string) {
  return resolveDesktopModelSelection(models, provider, model, effort)
    || resolveDesktopModelSelection(models, provider, models[0]?.id || '', effort);
}
