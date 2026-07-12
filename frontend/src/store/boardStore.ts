// The normalized element store — the client-side mirror of the backend's
// element graph. Every local mutation flows through commitTransaction():
// apply optimistically → push inverse onto the undo stack → POST → the
// server broadcasts to peers. Remote transactions arrive over the socket and
// run through the SAME applyOps reducer — one code path for local and remote
// mutations (§9.5/§9.9).
import { create } from 'zustand';
import { api } from '../api/client';
import type { BreadcrumbEntry, Op, PresenceUser, QElement, User } from '../api/types';
import { newClientId, newObjectId } from '../lib/objectId';
import { toast } from '../components/ui/Toaster';

export const clientId = newClientId();

interface UndoEntry { ops: Op[] }

interface BoardState {
  user: User | null;
  boardId: string;
  boardTitle: string;
  breadcrumb: BreadcrumbEntry[];
  role: string;
  readOnly: boolean;
  elements: Record<string, QElement>;
  selection: Set<string>;
  undoStack: UndoEntry[];
  redoStack: UndoEntry[];
  presence: Record<string, PresenceUser>;
  remoteEditing: Record<string, string>; // elementId -> peer name
  boardStats: Record<string, Record<string, number>>; // child board id -> type counts
  loading: boolean;

  setUser(u: User): void;
  openBoard(boardId: string): Promise<void>;
  refreshBoard(): Promise<void>;
  applyOps(ops: Op[]): void;
  commitTransaction(ops: Op[]): Promise<void>;
  undo(): void;
  redo(): void;
  select(ids: string[], additive?: boolean): void;
  clearSelection(): void;
  setPresence(users: PresenceUser[]): void;
  upsertPresence(u: PresenceUser): void;
  removePresence(clientId: string): void;
  setRemoteEditing(elementId: string, name: string, on: boolean): void;
}

// deepMerge mirrors the server's RFC-7386 merge-patch semantics.
function deepMerge(target: any, patch: any): any {
  if (patch === null || typeof patch !== 'object' || Array.isArray(patch)) return patch;
  const out = { ...(typeof target === 'object' && target !== null && !Array.isArray(target) ? target : {}) };
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else out[k] = deepMerge(out[k], v);
  }
  return out;
}

// snapshotForUndo extracts the current values of the fields a patch touches,
// producing the inverse patch (undoChanges) before the change applies.
export function snapshotForUndo(el: QElement | undefined, changes: Record<string, any>): Record<string, any> {
  if (!el) return {};
  const undo: Record<string, any> = {};
  for (const key of Object.keys(changes)) {
    if (key === 'content') {
      const inv: Record<string, any> = {};
      for (const ck of Object.keys(changes.content ?? {})) {
        inv[ck] = el.content?.[ck] ?? null;
      }
      undo.content = inv;
    } else if (key === 'location') {
      undo.location = JSON.parse(JSON.stringify(el.location));
    } else {
      undo[key] = (el as any)[key] ?? null;
    }
  }
  return undo;
}

export const useBoard = create<BoardState>((set, get) => ({
  user: null,
  boardId: '',
  boardTitle: '',
  breadcrumb: [],
  role: 'none',
  readOnly: false,
  elements: {},
  selection: new Set(),
  undoStack: [],
  redoStack: [],
  presence: {},
  remoteEditing: {},
  boardStats: {},
  loading: false,

  setUser: (u) => set({ user: u }),

  async openBoard(boardId) {
    set({ loading: true, boardId, elements: {}, selection: new Set(), undoStack: [], redoStack: [], presence: {}, remoteEditing: {} });
    const [view, children, unsorted] = await Promise.all([
      api.board(boardId),
      api.boardChildren(boardId),
      api.boardUnsorted(boardId),
    ]);
    const elements: Record<string, QElement> = { [view.board.id]: view.board };
    for (const el of [...children, ...unsorted]) elements[el.id] = el;
    set({
      boardTitle: view.board.content?.title ?? 'Untitled',
      breadcrumb: view.breadcrumb ?? [],
      role: view.role,
      readOnly: view.role !== 'owner' && view.role !== 'edit',
      elements,
      loading: false,
    });
    // Board-tile subtitles load after the canvas paints; failures are cosmetic.
    api.boardChildStats(boardId)
      .then((boardStats) => set({ boardStats: boardStats ?? {} }))
      .catch(() => set({ boardStats: {} }));
  },

  async refreshBoard() {
    const { boardId } = get();
    if (boardId) await get().openBoard(boardId);
  },

  // applyOps is THE reducer — local commits and remote broadcasts both land here.
  applyOps(ops) {
    set((state) => {
      const elements = { ...state.elements };
      for (const op of ops) {
        switch (op.action) {
          case 'create': {
            const ch = op.changes ?? {};
            elements[op.elementId] = {
              id: op.elementId,
              type: ch.type ?? 'UNKNOWN',
              location: ch.location ?? { parentId: state.boardId, section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 },
              content: ch.content ?? {},
              createdBy: ch.createdBy ?? '',
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            };
            break;
          }
          case 'update':
          case 'move': {
            const el = elements[op.elementId];
            if (el) {
              elements[op.elementId] = {
                ...el,
                content: op.changes?.content !== undefined ? deepMerge(el.content, op.changes.content) : el.content,
                location: op.changes?.location !== undefined ? deepMerge(el.location, op.changes.location) : el.location,
                labelIds: op.changes?.labelIds !== undefined ? op.changes.labelIds : el.labelIds,
                updatedAt: new Date().toISOString(),
              };
            }
            break;
          }
          case 'delete': {
            const el = elements[op.elementId];
            if (el) elements[op.elementId] = { ...el, deletedAt: new Date().toISOString() };
            break;
          }
          case 'restore': {
            const el = elements[op.elementId];
            if (el) elements[op.elementId] = { ...el, deletedAt: null };
            break;
          }
        }
      }
      return { elements };
    });
  },

  async commitTransaction(ops) {
    const { boardId, applyOps, readOnly } = get();
    // Defense in depth: viewers never write. The backend rejects this too,
    // but blocking here keeps the optimistic UI honest.
    if (readOnly) return;
    applyOps(ops);
    set((s) => ({ undoStack: [...s.undoStack.slice(-99), { ops }], redoStack: [] }));
    try {
      await api.applyTransaction(boardId, clientId, ops);
    } catch (err: any) {
      // Server rejected: surface it and roll back by reloading truth.
      toast.error(err?.message ? `Change reverted: ${err.message}` : 'Change reverted');
      await get().refreshBoard();
    }
  },

  undo() {
    const { undoStack, boardId, applyOps } = get();
    const entry = undoStack[undoStack.length - 1];
    if (!entry) return;
    // Replay each op's undoChanges, in reverse order (§9.5).
    const inverse: Op[] = [...entry.ops].reverse().map((op) => invertOp(op));
    applyOps(inverse);
    set((s) => ({
      undoStack: s.undoStack.slice(0, -1),
      redoStack: [...s.redoStack, entry],
    }));
    api.applyTransaction(boardId, clientId, inverse).catch(() => get().refreshBoard());
  },

  redo() {
    const { redoStack, boardId, applyOps } = get();
    const entry = redoStack[redoStack.length - 1];
    if (!entry) return;
    applyOps(entry.ops);
    set((s) => ({
      redoStack: s.redoStack.slice(0, -1),
      undoStack: [...s.undoStack, entry],
    }));
    api.applyTransaction(boardId, clientId, entry.ops).catch(() => get().refreshBoard());
  },

  select(ids, additive = false) {
    set((s) => {
      const selection = new Set(additive ? s.selection : []);
      for (const id of ids) selection.add(id);
      return { selection };
    });
  },

  clearSelection: () => set({ selection: new Set() }),

  setPresence: (users) => set({ presence: Object.fromEntries(users.filter((u) => u.clientId !== clientId).map((u) => [u.clientId, u])) }),
  upsertPresence: (u) => set((s) => (u.clientId === clientId ? s : { presence: { ...s.presence, [u.clientId]: u } })),
  removePresence: (id) => set((s) => {
    const presence = { ...s.presence };
    delete presence[id];
    return { presence };
  }),
  setRemoteEditing: (elementId, name, on) => set((s) => {
    const remoteEditing = { ...s.remoteEditing };
    if (on) remoteEditing[elementId] = name;
    else delete remoteEditing[elementId];
    return { remoteEditing };
  }),
}));

function invertOp(op: Op): Op {
  switch (op.action) {
    case 'create':
      return { elementId: op.elementId, action: 'delete', changes: {}, undoChanges: op.changes };
    case 'delete':
      return { elementId: op.elementId, action: 'restore', changes: {}, undoChanges: {} };
    case 'restore':
      return { elementId: op.elementId, action: 'delete', changes: {}, undoChanges: {} };
    default:
      return { elementId: op.elementId, action: op.action, changes: op.undoChanges, undoChanges: op.changes };
  }
}

// ---- convenience op builders used across components ----

export function createOp(type: string, parentId: string, extra: {
  position?: { x: number; y: number };
  section?: 'CANVAS' | 'UNSORTED';
  index?: number;
  width?: number;
  content?: Record<string, any>;
}): Op {
  return {
    elementId: newObjectId(),
    action: 'create',
    changes: {
      type,
      location: {
        parentId,
        section: extra.section ?? 'CANVAS',
        position: extra.position ?? { x: 0, y: 0 },
        index: extra.index ?? Date.now() / 1000,
        width: extra.width ?? 0,
        height: 0,
      },
      content: extra.content ?? {},
    },
    undoChanges: {},
  };
}

export function updateOp(el: QElement, changes: Record<string, any>): Op {
  return {
    elementId: el.id,
    action: 'update',
    changes,
    undoChanges: snapshotForUndo(el, changes),
  };
}

export function moveOp(el: QElement, location: Partial<QElement['location']>): Op {
  return {
    elementId: el.id,
    action: 'move',
    changes: { location },
    undoChanges: { location: JSON.parse(JSON.stringify(el.location)) },
  };
}

export function deleteOp(el: QElement): Op {
  return { elementId: el.id, action: 'delete', changes: {}, undoChanges: {} };
}
