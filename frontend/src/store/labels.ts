// Labels store (§4.18). The backend owns CRUD + usage counts; this caches the
// user's label set and drives the label chips, popover, and filter.
import { create } from 'zustand';
import { api } from '../api/client';
import type { Label } from '../api/types';
import { useBoard, updateOp } from './boardStore';

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
  /**
   * CV17. Tagging by hand was the one write in the app that did not go through
   * the transaction path.
   *
   * These called the REST endpoint and then hand-patched `useBoard.setState`,
   * so nothing was journalled and nothing broadcast. Tag three cards, press
   * Ctrl+Z, and the undo reached PAST the tagging to whatever you did before —
   * silently. A colleague's chips never updated at all.
   *
   * The agent, meanwhile, has always compiled `apply_label` into a real op with
   * a full prior-array inverse, which made it the only correct labeller in the
   * product — and made the asymmetry read as an agent bug the first time it
   * bit somebody.
   *
   * The inverse carries the WHOLE prior array, not the one id, because a merge
   * patch replaces `labelIds` wholesale: an undo naming only the label just
   * added would clear every other tag on the card. `snapshotForUndo` already
   * does exactly this for any top-level key, which is why this is an op and not
   * a special case.
   *
   * REST stays for label CRUD — creating and deleting a label is an account
   * concern, not a board edit. Usage counts still settle, because the server's
   * update branch runs `authorizeLabelPatch` and `settleLabelUsage` on this
   * same path.
   */
  attach: async (elementId, labelId) => {
    const el = useBoard.getState().elements[elementId];
    if (!el) return;
    if ((el.labelIds ?? []).includes(labelId)) return; // already tagged; not an edit
    const labelIds = [...(el.labelIds ?? []), labelId];
    await useBoard.getState().commitTransaction([updateOp(el, { labelIds })]);
  },
  detach: async (elementId, labelId) => {
    const el = useBoard.getState().elements[elementId];
    if (!el) return;
    const labelIds = (el.labelIds ?? []).filter((id) => id !== labelId);
    if (labelIds.length === (el.labelIds ?? []).length) return;
    await useBoard.getState().commitTransaction([updateOp(el, { labelIds })]);
  },
  setFilter: (filter) => set({ filter }),
  byId: (id) => get().labels.find((l) => l.id === id),
}));
