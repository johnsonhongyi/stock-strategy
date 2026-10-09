import {describe,expect,it} from 'vitest';
import {clusterSignalMarkers,fibColor} from './sbc-style';
describe('ATS marker presentation',()=>{
 it('keeps group endpoints and every intermediate marker',()=>{
  const markers=clusterSignalMarkers([1,3,7,15]);
  expect(markers.get(1)).toEqual({role:'first',count:3,sequence:1});
  expect(markers.get(3)?.role).toBe('middle');expect(markers.get(7)?.role).toBe('last');expect(markers.get(15)?.role).toBe('single');expect(markers.size).toBe(4);
 });
 it('uses the ATS Fibonacci level colors',()=>{expect(fibColor(.809)).toBe('#ff7700');expect(fibColor(.5)).toBe('#00e5ff');expect(fibColor(.191)).toBe('#00ff88');});
});
