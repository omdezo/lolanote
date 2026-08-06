// A single reusable right-click menu positioned at the pointer, closed on
// outside-click / Escape / scroll. Actions are contextual to what was
// clicked (an element, or empty canvas). Items may carry a `sub` list.
// Replaces the native menu.
//
// This menu is the only home in the product for Lock, Add label, Text
// direction, Group into column, Colour & icon, Rename, Create shortcut,
// Convert to template, Delete, and both "Ask Qomra about this" entry points —
// and until now every one of them was reachable by a hovering mouse and by
// nothing else: bare <div>/<button> with no menu semantics, no focus on open,
// no arrow keys, and a submenu that opened on `onMouseEnter` alone while its
// parent row's click was deliberately inert. On a bilingual product the
// sharpest casualty was Text direction, which also decides whether numbers in
// an Arabic card render as ٥ or 5.
import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { create } from 'zustand';
import { ChevronIcon } from '../Icons';
import { ownsEscape, useView } from '../../store/viewStore';

export interface MenuItem {
  label: string;
  icon?: JSX.Element;
  onClick?: () => void;
  danger?: boolean;
  divider?: boolean; // render a separator ABOVE this item
  disabled?: boolean;
  checked?: boolean; // renders as a menuitemcheckbox
  sub?: MenuItem[];  // nested group — a flyout on fine pointers, inline on touch
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

const MENU_OVERLAY = 'context-menu';

/**
 * Whether this device can hover at all.
 *
 * A hover-only submenu is not a degraded experience on a phone, it is an
 * absent one — `onMouseEnter` never fires, so the three text-direction options
 * simply do not exist. On a coarse pointer the group renders inline instead.
 */
function coarsePointer(): boolean {
  return typeof window !== 'undefined'
    && typeof window.matchMedia === 'function'
    && window.matchMedia('(hover: none), (pointer: coarse)').matches;
}

export function ContextMenuHost() {
  const { pos, items, close } = useContextMenu();
  const ref = useRef<HTMLDivElement>(null);
  const [subOpen, setSubOpen] = useState<number | null>(null);
  /**
   * The menu's real height, measured after it renders.
   *
   * It used to be guessed as `items.length * 34 + 16` and clamped with a bare
   * `Math.min` — no `Math.max`. A selected BOARD builds a 12–14 row menu, so on
   * a 667x375 landscape phone the clamp produced y = MINUS 117 and the first
   * rows (Duplicate, Make synced copy) were drawn above the top of the screen
   * with no way to scroll to them. The guess was optimistic even in portrait,
   * because three dividers add ~11px each and it counted none of them.
   */
  const [height, setHeight] = useState(0);
  // Where focus was when the menu opened. Right-click from a card leaves focus
  // on the card; without this it lands on <body>, which for a screen-reader
  // user is the top of a document with no landmarks and no way back.
  const returnFocus = useRef<HTMLElement | null>(null);
  const inline = coarsePointer();

  useEffect(() => {
    if (!pos) return;
    setSubOpen(null);
    returnFocus.current = document.activeElement as HTMLElement | null;
    useView.getState().pushOverlay(MENU_OVERLAY);
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) close();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && ownsEscape(MENU_OVERLAY)) { e.stopPropagation(); close(); }
    };
    window.addEventListener('pointerdown', onDown, true);
    window.addEventListener('keydown', onKey);
    window.addEventListener('wheel', close);
    return () => {
      window.removeEventListener('pointerdown', onDown, true);
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('wheel', close);
      useView.getState().popOverlay(MENU_OVERLAY);
      // Only when the menu is really gone. A right-click somewhere else while
      // this one is open re-runs the effect, and the layout effect below has
      // already focused the new menu's first row by the time this cleanup
      // fires — restoring here unconditionally would snatch it straight back.
      if (useContextMenu.getState().pos === null) returnFocus.current?.focus?.();
    };
  }, [pos, close]);

  // Focus the first enabled item, so the menu is operable the moment it opens.
  // Measure in the same pass: one layout effect, one reflow, and the position
  // below is corrected before the browser paints.
  useLayoutEffect(() => {
    if (!pos) { setHeight(0); return; }
    const node = ref.current;
    if (node) setHeight(node.offsetHeight);
    node?.querySelector<HTMLButtonElement>('button.ctx-item:not(:disabled)')?.focus();
  }, [pos, items]);

  if (!pos) return null;
  // Keep the menu on-screen — at BOTH ends. The upper clamp is the one that
  // was missing, and it is the one landscape needs.
  const x = Math.min(pos.x, window.innerWidth - 220);
  const menuH = height || items.length * 34 + 16;
  const y = Math.max(8, Math.min(pos.y, window.innerHeight - menuH - 8));
  // A submenu opens to the right unless that would clip off-screen.
  const subFlipped = x > window.innerWidth - 420;

  /**
   * Roving focus over the rendered rows.
   *
   * Read off the DOM rather than tracked in state: the row list is flat on a
   * fine pointer and interleaved with inline sub-groups on a coarse one, and
   * one query is honest about both.
   */
  const move = (from: HTMLElement, delta: number | 'first' | 'last') => {
    const rows = [...(ref.current?.querySelectorAll<HTMLButtonElement>('button.ctx-item:not(:disabled)') ?? [])];
    if (rows.length === 0) return;
    const at = rows.indexOf(from as HTMLButtonElement);
    const next = delta === 'first' ? 0
      : delta === 'last' ? rows.length - 1
        : (at + delta + rows.length) % rows.length;
    rows[next]?.focus();
  };

  const onRowKey = (e: React.KeyboardEvent<HTMLButtonElement>, item: MenuItem, i: number) => {
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); move(e.currentTarget, 1); break;
      case 'ArrowUp': e.preventDefault(); move(e.currentTarget, -1); break;
      case 'Home': e.preventDefault(); move(e.currentTarget, 'first'); break;
      case 'End': e.preventDefault(); move(e.currentTarget, 'last'); break;
      case 'ArrowRight':
        if (item.sub && !inline) { e.preventDefault(); setSubOpen(i); }
        break;
      case 'ArrowLeft':
        if (subOpen !== null && !inline) { e.preventDefault(); setSubOpen(null); }
        break;
      default: break;
    }
  };

  const renderRow = (item: MenuItem, key: string, i: number, isSub: boolean) => (
    <button
      key={key}
      className={`ctx-item${item.danger ? ' danger' : ''}${item.checked ? ' checked' : ''}${isSub ? ' ctx-sub-item' : ''}`}
      // A row that toggles a value is a checkbox, not a command — without this
      // the three text-direction options announce identically and the current
      // one is conveyed by a ✓ glyph nothing reads.
      role={item.checked !== undefined ? 'menuitemcheckbox' : 'menuitem'}
      aria-checked={item.checked !== undefined ? !!item.checked : undefined}
      aria-haspopup={item.sub && !inline ? 'menu' : undefined}
      aria-expanded={item.sub && !inline ? subOpen === i : undefined}
      disabled={item.disabled}
      onKeyDown={(e) => onRowKey(e, item, i)}
      onClick={() => {
        // A parent row used to `return` on click: on touch, where hover never
        // fires, that made the submenu unreachable by any input at all.
        if (item.sub) { setSubOpen(subOpen === i ? null : i); return; }
        item.onClick?.();
        close();
      }}
    >
      {item.icon && <span className="ctx-icon">{item.icon}</span>}
      {item.label}
      {item.sub && !inline && <span className="ctx-sub-arrow"><ChevronIcon size={12} /></span>}
      {item.checked && <span className="ctx-check" aria-hidden="true">✓</span>}
    </button>
  );

  return (
    <div
      ref={ref}
      className="context-menu"
      role="menu"
      aria-orientation="vertical"
      style={{ left: x, top: y }}
    >
      {items.map((item, i) => (
        <div
          key={i}
          className="ctx-item-wrap"
          onMouseEnter={() => { if (!inline) setSubOpen(item.sub ? i : null); }}
        >
          {item.divider && <div className="ctx-divider" />}
          {renderRow(item, `row-${i}`, i, false)}
          {/* On a coarse pointer the group is drawn inline and indented rather
              than as a flyout, because a flyout that opens on hover is not a
              control on a device with no hover. */}
          {item.sub && inline && (
            <div className="ctx-inline-group" role="group" aria-label={item.label}>
              {item.sub.map((s, j) => renderRow(s, `inline-${i}-${j}`, i, true))}
            </div>
          )}
          {item.sub && !inline && subOpen === i && (
            <div
              className={`context-menu ctx-submenu${subFlipped ? ' flipped' : ''}`}
              role="menu"
              aria-label={item.label}
            >
              {item.sub.map((s, j) => renderRow(s, `sub-${i}-${j}`, i, true))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}
