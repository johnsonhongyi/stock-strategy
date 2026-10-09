import { useRef, useState, type ReactNode } from 'react';

type Position = { x: number; y: number };
export function KLineDetails({ compact, children }: { compact: boolean; children: ReactNode }) {
  const key = `sbc-detail-position-v1-${compact ? 'compact' : 'expanded'}`;
  const [position, setPosition] = useState<Position | null>(() => {
    try { const value = JSON.parse(localStorage.getItem(key) || 'null'); return value && Number.isFinite(value.x) && Number.isFinite(value.y) ? { x: Math.max(0, Math.min(1, value.x)), y: Math.max(0, Math.min(1, value.y)) } : null; } catch { return null; }
  });
  const card = useRef<HTMLDivElement>(null);
  const drag = useRef<{ x: number; y: number } | null>(null);
  const latest = useRef(position);
  const save = (next: Position | null) => { try { if (next) localStorage.setItem(key, JSON.stringify(next)); else localStorage.removeItem(key); } catch { /* Storage can be disabled. */ } };
  return <div className="kline-details-slot"><div ref={card} className={`kline-hover-card kline-details${position ? ' floating' : ''}`} style={position ? { left: `${position.x * 100}%`, top: `${position.y * 100}%`, transform: `translate(-${position.x * 100}%, -${position.y * 100}%)` } : undefined}>
    <header>
      <button type="button" className="kline-details-handle" aria-label="拖动行情明细" onPointerDown={event => {
        event.preventDefault(); const bounds = card.current!.getBoundingClientRect(); drag.current = { x: event.clientX - bounds.left, y: event.clientY - bounds.top }; event.currentTarget.setPointerCapture(event.pointerId);
      }} onPointerMove={event => {
        if (!drag.current || !card.current?.parentElement) return;
        const bounds = card.current.closest(".kline-chart-wrap")!.getBoundingClientRect();
        const width = card.current.offsetWidth, height = card.current.offsetHeight;
        const next = { x: Math.max(0, Math.min(1, (event.clientX - bounds.left - drag.current.x) / Math.max(1, bounds.width - width))), y: Math.max(0, Math.min(1, (event.clientY - bounds.top - drag.current.y) / Math.max(1, bounds.height - height))) };
        latest.current = next; setPosition(next);
      }} onPointerUp={() => { if (drag.current) save(latest.current); drag.current = null; }} onPointerCancel={() => { drag.current = null; save(latest.current); }}>⠿ 行情明细</button>
      <button type="button" onClick={() => { drag.current = null; latest.current = null; setPosition(null); save(null); }}>移到图下</button>
    </header>
    {children}
  </div></div>;
}
