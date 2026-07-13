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
  sizes: Record<string, { w: number; h: number }>;
  editingId: string | null;
  lastPointer: { x: number; y: number }; // last canvas-space pointer (paste target)

  setView(panX: number, panY: number, scale: number): void;
  setDrag(d: DragState | null): void;
  setLineDraft(d: LineDraft | null): void;
  setDrawMode(on: boolean): void;
  reportSize(id: string, w: number, h: number): void;
  setEditing(id: string | null): void;
  toCanvas(clientX: number, clientY: number, viewport: HTMLElement): { x: number; y: number };
}

export const useView = create<ViewState>((set, get) => ({
  panX: 0,
  panY: 0,
  scale: 1,
  drag: null,
  lineDraft: null,
  drawMode: false,
  sizes: {},
  editingId: null,
  lastPointer: { x: 0, y: 0 },

  setView: (panX, panY, scale) => set({ panX, panY, scale }),
  setDrag: (drag) => set({ drag }),
  setLineDraft: (lineDraft) => set({ lineDraft }),
  setDrawMode: (drawMode) => set({ drawMode }),
  reportSize: (id, w, h) =>
    set((s) => {
      const prev = s.sizes[id];
      if (prev && Math.abs(prev.w - w) < 1 && Math.abs(prev.h - h) < 1) return s;
      return { sizes: { ...s.sizes, [id]: { w, h } } };
    }),
  setEditing: (editingId) => set({ editingId }),

  // toCanvas converts client (screen) coordinates into board-canvas space and
  // records the result as the paste target.
  toCanvas(clientX, clientY, viewport) {
    const rect = viewport.getBoundingClientRect();
    const { panX, panY, scale } = get();
    const pt = {
      x: (clientX - rect.left - panX) / scale,
      y: (clientY - rect.top - panY) / scale,
    };
    return pt;
  },
}));
