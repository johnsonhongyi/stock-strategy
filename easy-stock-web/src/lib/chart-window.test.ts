import { afterEach, describe, expect, it, vi } from 'vitest';
import { clampChartWindowSize, readChartWindowSize, saveChartWindowSize } from './chart-window';
afterEach(()=>vi.unstubAllGlobals());
describe('SBC window dimensions',()=>{
  it('clamps saved size to the current viewport',()=>{
    expect(clampChartWindowSize({width:1600,height:1200},900,600)).toEqual({width:876,height:576});
    expect(clampChartWindowSize({width:NaN,height:NaN},900,600)).toEqual({width:876,height:576});
  });
  it('restores resized dimensions across reopening and tolerates blocked storage',()=>{
    vi.stubGlobal('window',{innerWidth:1440,innerHeight:1000});
    const values=new Map<string,string>();
    vi.stubGlobal('localStorage',{getItem:(key:string)=>values.get(key),setItem:(key:string,value:string)=>values.set(key,value)});
    saveChartWindowSize({width:1100,height:720});
    expect(readChartWindowSize()).toEqual({width:1100,height:720});
    vi.stubGlobal('localStorage',{getItem:()=>{throw Error('blocked');},setItem:()=>{throw Error('blocked');}});
    expect(()=>saveChartWindowSize({width:1100,height:720})).not.toThrow();
    expect(readChartWindowSize()).toEqual({width:1200,height:800});
  });
});
