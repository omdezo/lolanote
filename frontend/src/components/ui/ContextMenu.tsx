// A single reusable right-click menu positioned at the pointer, closed on
// outside-click / Escape / scroll. Actions are contextual to what was
// clicked (an element, or empty canvas). Replaces the native menu.
import { useEffect, useRef } from 'react';
import { create } from 'zustand';

export interface MenuItem {
  label: string;
  icon?: JSX.Element;
  onClick: () => void;
  danger?: boolean;
  divider?: boolean; // render a separator ABOVE this item
  disabled?: boolean;
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

  useEffect(() => {
    if (!pos) return;
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

  return (
    <div ref={ref} className="context-menu" style={{ left: x, top: y }}>
      {items.map((item, i) => (
        <div key={i}>
          {item.divider && <div className="ctx-divider" />}
          <button
            className={`ctx-item${item.danger ? ' danger' : ''}`}
            disabled={item.disabled}
            onClick={() => { item.onClick(); close(); }}
          >
            {item.icon && <span className="ctx-icon">{item.icon}</span>}
            {item.label}
          </button>
        </div>
      ))}
    </div>
  );
}
