// The normalized element store — the client-side mirror of the backend's
// element graph. Every local mutation flows through commitTransaction():
// apply optimistically → push inverse onto the undo stack → POST → the
// server broadcasts to peers. Remote transactions arrive over the socket and
// run through the SAME applyOps reducer — one code path for local and remote
// mutations (§9.5/§9.9).
import { create } from 'zustand';
import { api } from '../api/client';
import type { BreadcrumbEntry, Op, PresenceUser, QElement, User } from '../api/types';
import { loadBoardSnapshot, saveBoardSnapshot, type BoardSnapshot } from '../lib/boardCache';
import { newClientId, newObjectId } from '../lib/objectId';
import { toast } from '../components/ui/Toaster';
import { t } from '../i18n';

export const clientId = newClientId();

interface UndoEntry {
  ops: Op[];
  /**
   * Which board these ops were committed against.
   *
   * The stack now survives navigation inside one board tree, so by the time an
   * entry is undone the open board may not be the board it belongs to. Posting
   * the inverse against whatever happens to be open would aim a transaction at
   * a board that does not contain the elements it names.
   */
  boardId: string;
  /**
   * The agent run that wrote these ops, when one did.
   *
   * The invariant this exists to hold: ONE OP HAS ONE UNDO OWNER. An agent
   * transaction lands in this stack (correctly — it is your edit, made on your
   * behalf), and it is also owned by the run, which tracks what it has written
   * and what has since been undone. Undoing it as a raw inverse left the run
   * still claiming the work was applied, and the outcome card's Undo a no-op;
   * pressing Ctrl+Shift+Z afterwards put the work back on the board while the
   * run recorded it as reverted, with no way left to remove it but by hand.
   */
  agentRunId?: string;
  /** Undone through the run, so redo is not this stack's to give back. */
  runReverted?: boolean;
}

/**
 * Runs whose revert THIS client asked for, and how many are still in flight.
 *
 * The revert is itself an agent-origin transaction carrying the reverting
 * user's id, so it comes back over the socket looking exactly like the run's
 * own work and used to be adopted as a fresh undo entry — an entry whose undo
 * would ask the server to revert a run it has already reverted, i.e. a Ctrl+Z
 * that does nothing. Counted rather than a Set because a run reverted one
 * element at a time produces one transaction per revert.
 *
 * Every expectation expires. If the socket is down when the revert lands, the
 * message that would have consumed it never arrives, and an expectation with no
 * end would silently swallow the next real agent transaction for that run —
 * trading a Ctrl+Z that does nothing for a Ctrl+Z that is not there at all.
 */
const revertsInFlight = new Map<string, { count: number; until: number }>();
const REVERT_ECHO_WINDOW_MS = 30_000;

/** Called before asking the server to revert a run, from either surface. */
export function expectRunRevert(runId: string): void {
  const live = revertsInFlight.get(runId);
  const count = live && live.until > Date.now() ? live.count + 1 : 1;
  revertsInFlight.set(runId, { count, until: Date.now() + REVERT_ECHO_WINDOW_MS });
}

/** The revert never happened — drop the expectation rather than leaving it to
 *  eat the next transaction this run produces. */
export function forgetRunRevert(runId: string): void {
  revertsInFlight.delete(runId);
}

interface BoardState {
  user: User | null;
  boardId: string;
  /**
   * The top of the tree the open board belongs to.
   *
   * Undo used to be cleared on every openBoard, which meant stepping into a
   * column's sub-board and back — journey 4, the most ordinary navigation in
   * the product — destroyed the history, including for an agent run applied
   * ninety seconds earlier. Keyed on the root, the stack survives movement
   * within one tree and resets when you genuinely go somewhere else.
   */
  rootBoardId: string;
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
  upsertElements(els: QElement[]): void;
  applyOps(ops: Op[]): void;
  commitTransaction(ops: Op[]): Promise<void>;
  /** Record already-applied remote ops as a local undo step (see below). */
  adoptRemote(ops: Op[], agentRunId?: string): void;
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
  rootBoardId: '',
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
    // The stacks are NOT cleared here. Which tree this board belongs to is not
    // known until the breadcrumb arrives, and clearing first meant every
    // navigation destroyed the history — including the one step in and back out
    // that the product's own nesting encourages.
    set({ loading: true, boardId, elements: {}, selection: new Set(), presence: {}, remoteEditing: {} });

    // Render-from-cache-first (§9.6): a cached snapshot paints instantly;
    // the network fetch below reconciles to server truth right after.
    const applySnapshot = (snap: BoardSnapshot | { view: any; children: QElement[]; unsorted: QElement[] }, loading: boolean) => {
      // A navigation may have superseded this board while we awaited.
      if (get().boardId !== boardId) return;
      const elements: Record<string, QElement> = { [snap.view.board.id]: snap.view.board };
      for (const el of [...snap.children, ...snap.unsorted]) elements[el.id] = el;
      // Ancestors, root first — so the first crumb is the top of this tree, and
      // a board with no crumbs IS the top of its own.
      const rootBoardId = snap.view.breadcrumb?.[0]?.id ?? snap.view.board.id;
      const leftTheTree = get().rootBoardId !== '' && get().rootBoardId !== rootBoardId;
      const title = snap.view.board.content?.title ?? 'Untitled';
      // AX18. A repo-wide grep for `document.title` returned zero while this
      // store maintained `boardTitle` for the chrome. Board navigation in this
      // product IS page navigation — `navigate()` swaps the entire store and
      // the realtime room and replaces the whole canvas — and it produced no
      // title change, no focus change and no announcement. The browser tab, the
      // window switcher, the history entry and the bookmark all kept saying
      // "QomraNote" no matter where you were.
      if (typeof document !== 'undefined') document.title = `${title} — QomraNote`;
      set({
        boardTitle: title,
        breadcrumb: snap.view.breadcrumb ?? [],
        rootBoardId,
        role: snap.view.role,
        readOnly: snap.view.role !== 'owner' && snap.view.role !== 'edit',
        elements,
        loading,
        ...(leftTheTree ? { undoStack: [], redoStack: [] } : {}),
      });
    };

    const cached = await loadBoardSnapshot(boardId);
    if (cached) applySnapshot(cached, true);

    const [view, children, unsorted] = await Promise.all([
      api.board(boardId),
      api.boardChildren(boardId),
      api.boardUnsorted(boardId),
    ]);
    applySnapshot({ view, children, unsorted }, false);
    void saveBoardSnapshot(boardId, { view, children, unsorted });

    // Board-tile subtitles load after the canvas paints; failures are cosmetic.
    api.boardChildStats(boardId)
      .then((boardStats) => set({ boardStats: boardStats ?? {} }))
      .catch(() => set({ boardStats: {} }));
  },

  // refreshBoard re-syncs the CURRENT board with server truth in place — no
  // clearing, no loading flash, no unmount/remount of every card, and the
  // undo/redo stacks survive. (openBoard's full reset is for navigation.)
  // Used after socket reconnects and server-rejected transactions.
  async refreshBoard() {
    const { boardId } = get();
    if (!boardId) return;
    const [view, children, unsorted] = await Promise.all([
      api.board(boardId),
      api.boardChildren(boardId),
      api.boardUnsorted(boardId),
    ]);
    const elements: Record<string, QElement> = { [view.board.id]: view.board };
    for (const el of [...children, ...unsorted]) elements[el.id] = el;
    set((s) => ({
      boardTitle: view.board.content?.title ?? 'Untitled',
      breadcrumb: view.breadcrumb ?? [],
      role: view.role,
      readOnly: view.role !== 'owner' && view.role !== 'edit',
      elements,
      // Keep whatever selection still exists.
      selection: new Set(Array.from(s.selection).filter((id) => elements[id])),
    }));
    void saveBoardSnapshot(boardId, { view, children, unsorted });
    api.boardChildStats(boardId)
      .then((boardStats) => set({ boardStats: boardStats ?? {} }))
      .catch(() => undefined);
  },

  // upsertElements merges server-created elements (duplicates, clones) into
  // the store without refetching the board.
  upsertElements(els) {
    if (els.length === 0) return;
    set((s) => {
      const elements = { ...s.elements };
      for (const el of els) elements[el.id] = el;
      return { elements };
    });
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
    set((s) => ({ undoStack: [...s.undoStack.slice(-99), { ops, boardId }], redoStack: [] }));
    try {
      await api.applyTransaction(boardId, clientId, ops);
    } catch (err: any) {
      // Server rejected: surface it and roll back by reloading truth.
      toast.error(err?.message ? `Change reverted: ${err.message}` : 'Change reverted');
      await get().refreshBoard();
    }
  },

  /**
   * Record ops that have ALREADY been applied as a local undo step.
   *
   * For writes that arrive over the socket but are the user's own doing — an
   * agent run acting on their behalf. Without this the agent's transaction was
   * applied and never recorded, so Ctrl+Z reached past it and undid whatever
   * the person did before, silently, while the auto-apply tooltip promised
   * "still one undo".
   *
   * Deliberately does not call applyOps: the caller has already reduced them,
   * and applying twice would double every move.
   */
  adoptRemote(ops, agentRunId) {
    if (!ops.length) return;
    // A revert we asked for is not new work to remember: adopting it would put
    // an entry on the stack whose undo asks the server to revert a run it has
    // already reverted.
    const expected = agentRunId ? revertsInFlight.get(agentRunId) : undefined;
    if (expected && expected.until > Date.now()) {
      if (expected.count > 1) revertsInFlight.set(agentRunId!, { ...expected, count: expected.count - 1 });
      else revertsInFlight.delete(agentRunId!);
      return;
    }
    set((s) => ({
      undoStack: [...s.undoStack.slice(-99), { ops, boardId: s.boardId, agentRunId }],
      redoStack: [],
    }));
  },

  undo() {
    const { undoStack, applyOps } = get();
    const entry = undoStack[undoStack.length - 1];
    if (!entry) return;

    // An agent transaction has an owner, and it is the run. Undoing it as a raw
    // inverse would leave the run's RevertedElementIDs — the thing the outcome
    // card reads — still claiming the work stands.
    if (entry.agentRunId) {
      const ids = Array.from(new Set(entry.ops.map((op) => op.elementId)));
      expectRunRevert(entry.agentRunId);
      set((s) => ({
        undoStack: s.undoStack.slice(0, -1),
        redoStack: [...s.redoStack, { ...entry, runReverted: true }],
      }));
      void import('../agent/agentStore').then(({ revertRunElements }) =>
        revertRunElements(entry.agentRunId!, ids),
      );
      return;
    }

    // Replay each op's undoChanges, in reverse order (§9.5).
    const inverse: Op[] = [...entry.ops].reverse().map((op) => invertOp(op));
    applyOps(inverse);
    set((s) => ({
      undoStack: s.undoStack.slice(0, -1),
      redoStack: [...s.redoStack, entry],
    }));
    api.applyTransaction(entry.boardId, clientId, inverse).catch(() => get().refreshBoard());
  },

  redo() {
    const { redoStack, applyOps } = get();
    const entry = redoStack[redoStack.length - 1];
    if (!entry) return;

    // Refused, by name. Re-applying would put the run's work back on the board
    // while the run itself still recorded it as reverted — after which the
    // outcome card's Undo short-circuits and there is no way left to remove it
    // except by hand. Retry on the run is the honest way to get it back.
    if (entry.runReverted) {
      toast.info(t('undo.agentRedoRefused'));
      return;
    }

    applyOps(entry.ops);
    set((s) => ({
      redoStack: s.redoStack.slice(0, -1),
      undoStack: [...s.undoStack, entry],
    }));
    api.applyTransaction(entry.boardId, clientId, entry.ops).catch(() => get().refreshBoard());
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
