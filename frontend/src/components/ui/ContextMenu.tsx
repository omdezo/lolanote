// A single reusable right-click menu positioned at the pointer, closed on
// outside-click / Escape / scroll. Actions are contextual to what was
// clicked (an element, or empty canvas). Items may carry a `sub` list that
// opens as a nested flyout on hover (e.g. Text direction ▸ Auto/LTR/RTL).
// Replaces the native menu.
import { useEffect, useRef, useState } from 'react';
import { create } from 'zustand';
import { ChevronIcon } from '../Icons';

export interface MenuItem {
  label: string;
  icon?: JSX.Element;
  onClick?: () => void;
  danger?: boolean;
  divider?: boolean; // render a separator ABOVE this item
  disabled?: boolean;
  checked?: boolean; // render a leading ✓ (used inside submenus)
  sub?: MenuItem[];  // nested flyout — opens on hover; onClick is ignored
}

interface MenuState {
  pos: { x: number; y: number } | null;
  items: MenuItem[];
  open(x: number, y: number, items: MenuItem[]): void;
  close(): void;
}

export const useContextMenu = create<MenuState>((set) => ({
  pos: null,
  items: [],
  open: (x, y, items) => set({ pos: { x, y }, items }),
  close: () => set({ pos: null, items: [] }),
}));

export function ContextMenuHost() {
  const { pos, items, close } = useContextMenu();
  const ref = useRef<HTMLDivElement>(null);
  const [subOpen, setSubOpen] = useState<number | null>(null);

  useEffect(() => {
    if (!pos) return;
    setSubOpen(null);
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) close();
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close();
    window.addEventListener('pointerdown', onDown, true);
    window.addEventListener('keydown', onKey);
    window.addEventListener('wheel', close);
    return () => {
      window.removeEventListener('pointerdown', onDown, true);
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('wheel', close);
    };
  }, [pos, close]);

  if (!pos) return null;
  // Keep the menu on-screen.
  const x = Math.min(pos.x, window.innerWidth - 220);
  const y = Math.min(pos.y, window.innerHeight - items.length * 34 - 16);
  // A submenu opens to the right unless that would clip off-screen.
  const subFlipped = x > window.innerWidth - 420;

  return (
    <div ref={ref} className="context-menu" style={{ left: x, top: y }}>
      {items.map((item, i) => (
        <div
          key={i}
          className="ctx-item-wrap"
          onMouseEnter={() => setSubOpen(item.sub ? i : null)}
        >
          {item.divider && <div className="ctx-divider" />}
          <button
            className={`ctx-item${item.danger ? ' danger' : ''}`}
            disabled={item.disabled}
            onClick={() => {
              if (item.sub) return; // parent rows only open their flyout
              item.onClick?.();
              close();
            }}
          >
            {item.icon && <span className="ctx-icon">{item.icon}</span>}
            {item.label}
            {item.sub && <span className="ctx-sub-arrow"><ChevronIcon size={12} /></span>}
          </button>
          {item.sub && subOpen === i && (
            <div className={`context-menu ctx-submenu${subFlipped ? ' flipped' : ''}`}>
              {item.sub.map((s, j) => (
                <button
                  key={j}
                  className={`ctx-item${s.danger ? ' danger' : ''}${s.checked ? ' checked' : ''}`}
                  disabled={s.disabled}
                  onClick={() => { s.onClick?.(); close(); }}
                >
                  {s.icon && <span className="ctx-icon">{s.icon}</span>}
                  {s.label}
                  {s.checked && <span className="ctx-check">✓</span>}
                </button>
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
