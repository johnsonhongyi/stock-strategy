import { describe, expect, it } from 'vitest';
import { linkageDefaultKey, resolveLinkageDefault } from './linkage-default';
const targets = [
 { node_id:'bridge', workspace_id:'research', label:'Codex', status:'configured' },
 { node_id:'tk', workspace_id:'desktop', label:'JohnsonTriton', status:'configured' },
];
describe('watchlist desktop defaults', () => {
 it('never treats list order as an implicit default', () => expect(resolveLinkageDefault(targets, '')).toBe(''));
 it('restores the explicitly configured desktop', () => expect(resolveLinkageDefault(targets, 'tk')).toBe('tk'));
 it('does not redirect a missing default to the remaining node', () => expect(resolveLinkageDefault([targets[0]], 'tk')).toBe(''));
 it('selects a single target only without an explicit default', () => expect(resolveLinkageDefault([targets[1]], '')).toBe('tk'));
 it('isolates different backends and normalizes trailing slashes', () => {
  expect(linkageDefaultKey({backendUrl:'http://one/',token:'a'})).toBe(linkageDefaultKey({backendUrl:'http://one',token:'b'}));
  expect(linkageDefaultKey({backendUrl:'http://one',token:'a'})).not.toBe(linkageDefaultKey({backendUrl:'http://two',token:'a'}));
 });
});
