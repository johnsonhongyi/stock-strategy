import { useSyncExternalStore } from 'react';

const key = 'easy-stock:sbc-white-theme';
const event = 'sbc-theme-change';
let memoryValue = false;
export function readSBCTheme() {
  try { return window.localStorage.getItem(key) === 'true'; }
  catch { return memoryValue; }
}
export function setSBCTheme(value: boolean) {
  memoryValue = value;
  try { window.localStorage.setItem(key, String(value)); } catch { /* Keep this session usable when storage is unavailable. */ }
  window.dispatchEvent(new Event(event));
}
function subscribe(listener: () => void) {
  window.addEventListener(event, listener);
  const onStorage = (change: StorageEvent) => { if (change.key === key || change.key === null) listener(); };
  window.addEventListener('storage', onStorage);
  return () => { window.removeEventListener(event, listener); window.removeEventListener('storage', onStorage); };
}
export function useSBCTheme(): [boolean, (value: boolean) => void] {
  return [useSyncExternalStore(subscribe, readSBCTheme, () => false), setSBCTheme];
}
