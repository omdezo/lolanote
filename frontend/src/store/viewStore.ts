// Ephemeral view state: pan/zoom, in-flight drags, line-drawing mode, and
// measured element sizes. Kept separate from boardStore so none of this
// pollutes the undo history or the persisted element graph.
import { create } from 'zustand';

export interface DragState {
  ids: string[];
  dx: number;
  dy: number;
}

// LineDraft is the ghost connection while dragging from a card's anchor.
export interface LineDraft {
  sourceId: string;
  x: number;
  y: number;
}

interface ViewState {
  panX: number;
  panY: number;
  scale: number;
  drag: DragState | null;
  lineDraft: LineDraft | null;
  drawMode: boolean;
  /**
   * Rubber-band select as a deliberate mode.
   *
   * On a fine pointer this is always false and nothing changes: dragging on
   * empty canvas marquees, as it always has. On a coarse pointer the same
   * one-finger drag is the universal PAN gesture, and binding it to marquee
   * meant that trying to look around selected thirty cards and popped a
   * floating action bar with a Delete button in it — an unintended destructive
   * multi-select every single time you tried to navigate. So on touch, marquee
   * is something you turn on, next to Draw, which is the precedent.
   */
  marqueeMode: boolean;
  // Label filter: when non-empty, cards without a selected label dim out.
  labelFilter: Set<string>;
  sizes: Record<string, { w: number; h: number }>;
  editingId: string | null;
  /**
   * The one card on the canvas that is in the tab order.
   *
   * Every shell used to carry `tabIndex={0}`, which made a 200-card board 200
   * tab stops and left arrow keys — the thing a 2D surface actually needs —
   * doing nothing. One tab stop in, arrows within: the roving index is which
   * card that stop currently is.
   */
  focusedId: string | null;
  setFocused(id: string | null): void;
  /**
   * Which overlays are open, innermost last.
   *
   * Six surfaces each bound their OWN window-level Escape listener — the panel
   * switcher, the context menu, the label popover, the board-style popover, the
   * prompt, and the agent shell — and none of them called stopPropagation. So
   * one Escape ran all of them. The live casualty: with an agent proposal
   * pending, opening Settings and pressing Escape to close it also discarded
   * the plan, terminally, with no confirmation and no undo, after the person
   * had already paid for it.
   *
   * A stack rather than a guard on one listener: a guard would be re-broken by
   * the next overlay somebody adds, and there have been six.
   */
  overlays: string[];

  setView(panX: number, panY: number, scale: number): void;
  setDrag(d: DragState | null): void;
  setLineDraft(d: LineDraft | null): void;
  toggleLabelFilter(labelId: string): void;
  clearLabelFilter(): void;
  setDrawMode(on: boolean): void;
  setMarqueeMode(on: boolean): void;
  /**
   * Record an element's measured size. Coalesced — see flushSizeReports.
   *
   * Reports are BUFFERED, so a caller cannot assume `sizes[id]` is populated on
   * the next line. Nothing reads it that way: the line layer and the marquee
   * both read it a frame or more after mount.
   */
  reportSize(id: string, w: number, h: number): void;
  setEditing(id: string | null): void;
  pushOverlay(id: string): void;
  popOverlay(id: string): void;
  toCanvas(clientX: number, clientY: number, viewport: HTMLElement): { x: number; y: number };
}

/**
 * The last canvas-space pointer position, for paste-here.
 *
 * A module variable, not store state: it was written on every pointermove while
 * the viewport subscribed to the whole store, so drifting the mouse across
 * empty canvas reconciled every card at 120 Hz — to update a value that is only
 * ever read imperatively, by the paste handler, once.
 */
let lastPointer = { x: 0, y: 0 };
export function setLastPointer(x: number, y: number): void { lastPointer = { x, y }; }
export function getLastPointer(): { x: number; y: number } { return lastPointer; }

/**
 * Whether `id` is the overlay Escape should currently close.
 *
 * Read from getState() inside the listener rather than subscribed to, so a
 * handler bound once at mount still sees the live stack.
 */
export function ownsEscape(id: string): boolean {
  const stack = useView.getState().overlays;
  return stack.length === 0 || stack[stack.length - 1] === id;
}

/**
 * Size reports waiting for the next frame.
 *
 * Every shell measured itself on mount and wrote straight through with a
 * `{...s.sizes}` spread, so opening a 1,000-element board issued 1,000
 * sequential `set` calls whose spreads copied 0, 1, 2 … 999 keys — about half a
 * million key copies and a thousand discarded objects, at exactly the moment
 * the person is waiting for the board to paint. Each also produced a fresh
 * `sizes` identity, so the line layer re-rendered a thousand times during board
 * open and recomputed every connector's geometry each time.
 *
 * Module-level rather than in the store: this is a write buffer, not state, and
 * nothing should be able to subscribe to it.
 */
const pendingSizes = new Map<string, { w: number; h: number }>();
let sizeFlushHandle: number | null = null;

/**
 * Apply every buffered size report as ONE store write.
 *
 * Exported so a test can be deterministic about frames rather than racing the
 * scheduler, and so a caller that genuinely needs the measurement now (fit-to-
 * screen right after a mount) can force it.
 */
export function flushSizeReports(): void {
  sizeFlushHandle = null;
  if (pendingSizes.size === 0) return;
  const batch = Array.from(pendingSizes.entries());
  pendingSizes.clear();
  useView.setState((s) => {
    const sizes = { ...s.sizes };
    for (const [id, box] of batch) sizes[id] = box;
    return { sizes };
  });
}

// rAF where there is one, a timer where there is not: this module is imported
// by node-environment tests that have no frames at all, and a scheduler that
// silently never fires would make sizes permanently empty there.
function scheduleSizeFlush(): void {
  if (sizeFlushHandle !== null) return;
  sizeFlushHandle = typeof requestAnimationFrame === 'function'
    ? requestAnimationFrame(flushSizeReports)
    : (setTimeout(flushSizeReports, 16) as unknown as number);
}

export const useView = create<ViewState>((set, get) => ({
  panX: 0,
  panY: 0,
  scale: 1,
  drag: null,
  lineDraft: null,
  drawMode: false,
  marqueeMode: false,
  labelFilter: new Set(),
  sizes: {},
  editingId: null,
  focusedId: null,
  overlays: [],

  setView: (panX, panY, scale) => set({ panX, panY, scale }),
  setDrag: (drag) => set({ drag }),
  setLineDraft: (lineDraft) => set({ lineDraft }),
  toggleLabelFilter: (labelId) =>
    set((s) => {
      const labelFilter = new Set(s.labelFilter);
      if (labelFilter.has(labelId)) labelFilter.delete(labelId);
      else labelFilter.add(labelId);
      return { labelFilter };
    }),
  clearLabelFilter: () => set({ labelFilter: new Set() }),
  // The two canvas modes are mutually exclusive: a finger cannot both draw a
  // stroke and drag a selection box.
  setDrawMode: (drawMode) => set({ drawMode, marqueeMode: drawMode ? false : useView.getState().marqueeMode }),
  setMarqueeMode: (marqueeMode) => set({ marqueeMode, drawMode: marqueeMode ? false : useView.getState().drawMode }),
  reportSize: (id, w, h) => {
    // The pending buffer wins the comparison: a card that grew and shrank back
    // inside one frame must clear its pending entry, not skip past it and let
    // the intermediate size get flushed as though it were the final one.
    const prev = pendingSizes.get(id) ?? useView.getState().sizes[id];
    if (prev && Math.abs(prev.w - w) < 1 && Math.abs(prev.h - h) < 1) {
      if (pendingSizes.has(id)) return;
      const settled = useView.getState().sizes[id];
      if (settled && Math.abs(settled.w - w) < 1 && Math.abs(settled.h - h) < 1) return;
    }
    pendingSizes.set(id, { w, h });
    scheduleSizeFlush();
  },
  setEditing: (editingId) => set({ editingId }),
  setFocused: (focusedId) => set((s) => (s.focusedId === focusedId ? s : { focusedId })),

  // Re-pushing an id that is already open moves it to the top rather than
  // stacking a duplicate: the panel switcher swaps Search for Settings without
  // ever unmounting, and two entries would take two Escapes to clear.
  pushOverlay: (id) => set((s) => ({ overlays: [...s.overlays.filter((o) => o !== id), id] })),
  popOverlay: (id) => set((s) => ({ overlays: s.overlays.filter((o) => o !== id) })),

  // toCanvas converts client (screen) coordinates into board-canvas space.
  toCanvas(clientX, clientY, viewport) {
    const rect = viewport.getBoundingClientRect();
    const { panX, panY, scale } = get();
    return {
      x: (clientX - rect.left - panX) / scale,
      y: (clientY - rect.top - panY) / scale,
    };
  },
}));
