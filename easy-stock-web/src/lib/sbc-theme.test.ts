import { afterEach, describe, expect, it, vi } from 'vitest';
import { readSBCTheme, setSBCTheme } from './sbc-theme';

afterEach(() => { vi.unstubAllGlobals(); });
describe('SBC persisted theme', () => {
  it('defaults to dark and persists both checkbox states', () => {
    const values = new Map<string, string>();
    const dispatchEvent = vi.fn();
    vi.stubGlobal('window', {localStorage: {getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value)}, dispatchEvent});
    expect(readSBCTheme()).toBe(false);
    setSBCTheme(true); expect(readSBCTheme()).toBe(true);
    expect(values.get('easy-stock:sbc-white-theme')).toBe('true');
    setSBCTheme(false); expect(readSBCTheme()).toBe(false);
    expect(dispatchEvent).toHaveBeenCalledTimes(2);
  });
  it('keeps working when browser storage is blocked', () => {
    vi.stubGlobal('window', {localStorage: {getItem: () => {throw new Error('blocked');}, setItem: () => {throw new Error('blocked');}}, dispatchEvent: vi.fn()});
    setSBCTheme(true); expect(readSBCTheme()).toBe(true);
    setSBCTheme(false); expect(readSBCTheme()).toBe(false);
  });
});
