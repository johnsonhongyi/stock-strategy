import { describe, expect, it } from 'vitest';
import { preserveLeaderPreview } from './use-theme-constituents';
import type { ThemeScreenData } from './backend';
const empty = {map:{groups:[]},coverage:'full',complete:true} as unknown as ThemeScreenData;
const preview = {map:{groups:[{nodes:[{stocks:[{symbol:'300413.SZ'}]}]}]},coverage:'leaders',complete:false} as unknown as ThemeScreenData;
describe('theme constituent recovery',()=>{
  it('keeps known leaders when a full response unexpectedly has no members',()=>expect(preserveLeaderPreview(empty,preview)).toBe(true));
  it('accepts a populated full response and genuine empty filtered results',()=>{
    expect(preserveLeaderPreview(preview,preview)).toBe(false);
    expect(preserveLeaderPreview(empty,{...preview,coverage:'full',complete:true})).toBe(false);
    expect(preserveLeaderPreview(empty,null)).toBeFalsy();
  });
});
