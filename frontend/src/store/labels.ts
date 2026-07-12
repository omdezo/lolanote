// Labels store (§4.18). The backend owns CRUD + usage counts; this caches the
// user's label set and drives the label chips, popover, and filter.
import { create } from 'zustand';
import { api } from '../api/client';
import type { Label } from '../api/types';
import { useBoard } from './boardStore';

interface LabelState {
  labels: Label[];
  filter: string | null; // active label filter id, or null
  load(): Promise<void>;
  create(name: string, color?: string): Promise<Label>;
  attach(elementId: string, labelId: string): Promise<void>;
  detach(elementId: string, labelId: string): Promise<void>;
  setFilter(id: string | null): void;
  byId(id: string): Label | undefined;
}

export const useLabels = create<LabelState>((set, get) => ({
  labels: [],
  filter: null,
  load: async () => {
    try { set({ labels: await api.labels() }); } catch { /* labels are optional */ }
  },
  create: async (name, color) => {
    const label = await api.createLabel(name, color);
    set((s) => (s.labels.some((l) => l.id === label.id) ? s : { labels: [...s.labels, label] }));
    return label;
  },
  attach: async (elementId, labelId) => {
    await api.attachLabel(elementId, labelId);
    // Reflect locally so chips appear immediately.
    const state = useBoard.getState();
    const el = state.elements[elementId];
    if (el) {
      const labelIds = Array.from(new Set([...(el.labelIds ?? []), labelId]));
      useBoard.setState({ elements: { ...state.elements, [elementId]: { ...el, labelIds } } });
    }
  },
  detach: async (elementId, labelId) => {
    await api.detachLabel(elementId, labelId);
    const state = useBoard.getState();
    const el = state.elements[elementId];
    if (el) {
      const labelIds = (el.labelIds ?? []).filter((id) => id !== labelId);
      useBoard.setState({ elements: { ...state.elements, [elementId]: { ...el, labelIds } } });
    }
  },
  setFilter: (filter) => set({ filter }),
  byId: (id) => get().labels.find((l) => l.id === id),
}));
