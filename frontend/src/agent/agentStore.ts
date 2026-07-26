// Client state for AI agent runs.
//
// Two rules shape this file:
//
//  1. It never touches boardStore. A plan is not board state — it is a proposal
//     the user has not accepted. Keeping it out of the element graph is what
//     makes "preview writes nothing" true rather than merely intended, and
//     keeps undo/redo semantics clean.
//
//  2. It never builds ops. The user's edits accumulate as typed adjustments
//     that the SERVER recompiles against its own stored plan. The `effective`
//     projection below exists only so the review list and the canvas can show
//     what will happen — the same relationship deepMerge in boardStore has
//     with the server's merge-patch semantics.
import { useMemo } from 'react';
import { create } from 'zustand';
import { api, ApiError } from '../api/client';
import type {
  AgentAction, AgentAdjustment, AgentAutonomy, AgentCapabilities, AgentEvent,
  AgentAuditEntry, AgentPlan, AgentRun, AgentRunState, AgentScope,
} from '../api/types';
import { useBoard } from '../store/boardStore';
import { toast } from '../components/ui/Toaster';

interface AgentState {
  run: AgentRun | null;
  events: AgentEvent[];
  adjustments: AgentAdjustment[];
  capabilities: AgentCapabilities | null;
  busy: boolean;
  /** The dock is the single agent surface; open means it is on screen. */
  open: boolean;
  /** Plan row the pointer is over, so the canvas can highlight its ghost. */
  hoverSeq: number | null;
  /** Recent runs on this board, so an earlier one can still be reverted. */
  recent: AgentRun[];

  loadCapabilities(): Promise<void>;
  start(opts: { intent: string; scope: AgentScope; autonomy: AgentAutonomy; selectionIds?: string[] }): Promise<void>;
  ingest(ev: AgentEvent & { state?: AgentRunState }): void;
  loadRecent(): Promise<void>;
  /** What the agent has actually changed on this board. */
  audit: AgentAuditEntry[];
  loadAudit(): Promise<void>;
  setHover(seq: number | null): void;
  resync(): Promise<void>;
  adjust(a: AgentAdjustment): void;
  undoAdjust(seq: number): void;
  apply(): Promise<void>;
  refine(note: string): Promise<void>;
  discard(): Promise<void>;
  cancel(): Promise<void>;
  revert(): Promise<void>;
  dismiss(): void;
  /** Scope the composer should start on, when opened from a selection. */
  pendingScope: AgentScope | null;
  setOpen(open: boolean, scope?: AgentScope): void;
}

/** States where a run is still working and the user is just watching. */
export const WORKING: AgentRunState[] = ['CREATED', 'PLANNING', 'RUNNING', 'APPLYING', 'VERIFYING'];

export const useAgent = create<AgentState>((set, get) => ({
  run: null,
  events: [],
  adjustments: [],
  capabilities: null,
  busy: false,
  open: false,
  hoverSeq: null,
  recent: [],
  pendingScope: null,
  audit: [],

  async loadCapabilities() {
    try {
      set({ capabilities: await api.agentCapabilities() });
    } catch {
      // A deployment without the agent configured simply has no capabilities;
      // every entry point stays hidden rather than erroring at the user.
      set({ capabilities: { enabled: false, can: [], cannot: [], limits: { maxActions: 0, maxSteps: 0 } } });
    }
  },

  async start({ intent, scope, autonomy, selectionIds }) {
    const boardId = useBoard.getState().boardId;
    if (!boardId || !intent.trim()) return;
    set({ busy: true, events: [], adjustments: [], hoverSeq: null });
    try {
      const run = await api.agentCreateRun({ boardId, intent: intent.trim(), scope, autonomy, selectionIds });
      set({ run, busy: false });
    } catch (err) {
      set({ busy: false });
      const e = err as ApiError;
      toast.error(e?.status === 409
        ? 'Qomra is already working on this board.'
        : e?.message || 'Could not start.');
    }
  },

  ingest(ev) {
    const { run } = get();
    if (!run || ev.runId !== run.id) return;
    set((s) => {
      // Events can arrive out of order across a reconnect; keep the journal
      // sorted and de-duplicated by sequence so the trace reads correctly.
      const seen = new Set(s.events.map((e) => e.sequence));
      const events = seen.has(ev.sequence) ? s.events : [...s.events, ev].sort((a, b) => a.sequence - b.sequence);
      const next = ev.state && ev.state !== s.run!.state ? { ...s.run!, state: ev.state } : s.run!;
      return { events, run: next };
    });
    // A state change means the authoritative run has more to tell us than the
    // event carries (the plan, the verdict, the spend) — re-read it.
    if (ev.type === 'run.state' || ev.type === 'plan.ready' || ev.type === 'verdict') {
      void get().resync();
    }
  },

  async resync() {
    const { run } = get();
    if (!run) return;
    try {
      const [fresh, events] = await Promise.all([api.agentRun(run.id), api.agentEvents(run.id, 0)]);
      set({ run: fresh, events });
    } catch { /* the panel keeps showing what it has */ }
  },

  adjust(a) { set((s) => ({ adjustments: [...s.adjustments, a] })); },

  /** Remove every adjustment aimed at one action — the "put it back" affordance. */
  undoAdjust(seq) { set((s) => ({ adjustments: s.adjustments.filter((a) => a.seq !== seq) })); },

  async apply() {
    const { run, adjustments } = get();
    if (!run) return;
    set({ busy: true });
    try {
      const applied = await api.agentApply(run.id, adjustments);
      set({ run: applied, busy: false, adjustments: [] });
      // The agent's transaction arrives over the board socket like any other,
      // so the canvas updates itself. This refresh covers the case where the
      // socket is down and the user would otherwise see a stale board.
      void useBoard.getState().refreshBoard();
    } catch (err) {
      set({ busy: false });
      const e = err as ApiError;
      if (e?.status === 409) {
        // Exact-action binding: the board moved under the plan, so the
        // approval no longer describes reality. Re-read rather than commit.
        toast.error('The board changed while you were reviewing.');
        void get().resync();
        void useBoard.getState().refreshBoard();
      } else {
        toast.error(e?.message || 'Could not apply.');
      }
    }
  },

  /**
   * Send a proposed plan back for another pass. The run keeps its identity and
   * its cost meter, so adjusting a plan no longer means paying for a fresh run
   * that has not been told what was wrong with the last one.
   */
  async refine(note) {
    const { run } = get();
    if (!run || !note.trim()) return;
    set({ busy: true });
    try {
      const next = await api.agentRefine(run.id, note.trim());
      // Adjustments belonged to the plan being replaced; carrying them over
      // would silently re-apply edits to actions that no longer exist.
      set({ run: next, busy: false, adjustments: [], hoverSeq: null });
    } catch (err) {
      set({ busy: false });
      toast.error((err as ApiError)?.message || 'Could not revise.');
    }
  },

  async discard() {
    const { run } = get();
    if (!run) return;
    set({ busy: true });
    try {
      await api.agentDiscard(run.id);
    } catch { /* discarding a run that already ended is not an error */ }
    set({ run: null, events: [], adjustments: [], busy: false });
  },

  async cancel() {
    const { run } = get();
    if (!run) return;
    try {
      await api.agentCancel(run.id);
    } catch { /* idempotent */ }
    set({ run: null, events: [], adjustments: [] });
  },

  async revert() {
    const { run } = get();
    if (!run) return;
    set({ busy: true });
    try {
      const reverted = await api.agentRevert(run.id);
      set({ run: reverted, busy: false });
      void useBoard.getState().refreshBoard();
      // No toast: the bar reports the outcome inline, and a toast at the bottom
      // centre lands directly on top of it — the same sentence, twice, with the
      // second one hiding the first.
    } catch (err) {
      set({ busy: false });
      toast.error((err as ApiError)?.message || 'Could not revert.');
    }
  },

  async loadRecent() {
    const boardId = useBoard.getState().boardId;
    if (!boardId) return;
    try {
      set({ recent: (await api.agentRuns(boardId, 6)) ?? [] });
    } catch { set({ recent: [] }); }
  },

  /**
   * Every transaction has recorded its origin since the agent shipped; nothing
   * ever surfaced it. Trust in an agent is mostly being able to check up on it
   * afterwards.
   */
  async loadAudit() {
    const boardId = useBoard.getState().boardId;
    if (!boardId) return;
    try {
      set({ audit: (await api.agentAudit(boardId)) ?? [] });
    } catch { set({ audit: [] }); }
  },

  setHover(hoverSeq) { set({ hoverSeq }); },

  dismiss() {
    set({ run: null, events: [], adjustments: [], hoverSeq: null });
    void get().loadRecent();
  },
  setOpen(open, scope) {
    set({ open, pendingScope: scope ?? null });
    if (open) {
      void get().loadRecent();
      void get().loadAudit();
    }
  },
}));

/** A plan with the user's adjustments folded in, for rendering only. */
export interface EffectivePlan {
  actions: AgentAction[];
  /** Sequences the user removed, so the review list can offer them back. */
  dropped: number[];
}

// computeEffective mirrors the server's ApplyAdjustments so the review list and
// the canvas show what will actually happen. The server stays authoritative —
// this is a rendering projection, and a disagreement surfaces as a failed
// precondition rather than as a wrong write.
//
// It is a plain function, not a store selector: a selector that built a fresh
// object on every call makes React's getSnapshot uncacheable, which loops.
export function computeEffective(
  plan: AgentPlan | undefined,
  adjustments: AgentAdjustment[],
): EffectivePlan | null {
  if (!plan) return null;

  const dropped = new Set<number>();
  const retitle = new Map<number, string>();
  const retext = new Map<number, string>();
  for (const a of adjustments) {
    if (a.seq < 0 || a.seq >= plan.actions.length) continue;
    if (a.kind === 'drop') dropped.add(a.seq);
    if (a.kind === 'retitle') retitle.set(a.seq, a.value);
    if (a.kind === 'retext') retext.set(a.seq, a.value);
  }

  // Cascade: an action parented to a dropped create cannot survive it — the
  // child would have nowhere to go. The server does the same, so the preview
  // and the commit agree about what a single click removed.
  const dead = new Set<string>();
  for (const seq of dropped) {
    const a = plan.actions[seq];
    if (a && isCreate(a.kind)) dead.add(a.elementId);
  }
  for (let changed = true; changed;) {
    changed = false;
    plan.actions.forEach((a, i) => {
      if (dropped.has(i)) return;
      if (a.parentId && dead.has(a.parentId)) {
        dropped.add(i);
        if (isCreate(a.kind)) dead.add(a.elementId);
        changed = true;
      }
    });
  }

  const actions: AgentAction[] = [];
  plan.actions.forEach((a, i) => {
    if (dropped.has(i)) return;
    actions.push({ ...a, title: retitle.get(i) ?? a.title, text: retext.get(i) ?? a.text });
  });
  return { actions, dropped: [...dropped].sort((x, y) => x - y) };
}

/** Memoized effective plan, for rendering. */
export function useEffectivePlan(): EffectivePlan | null {
  const plan = useAgent((s) => s.run?.plan);
  const adjustments = useAgent((s) => s.adjustments);
  return useMemo(() => computeEffective(plan, adjustments), [plan, adjustments]);
}

/**
 * Whether an action brings a new element into being, and therefore whether
 * dropping it must cascade to everything parented to it.
 *
 * Must mirror ActionKind.Creates() on the server exactly. A prefix test on
 * "create_" looks equivalent and is not: `connect` and `clone_here` also make
 * elements, and treating them as edits would leave orphans behind a dropped
 * parent.
 */
const CREATE_KINDS = new Set([
  'create_board', 'create_column', 'create_note', 'create_todo',
  'create_link', 'create_table', 'connect', 'clone_here',
]);
export function isCreate(kind: string): boolean { return CREATE_KINDS.has(kind); }
export function isDestructive(kind: string): boolean { return kind === 'delete_element'; }

/** Short label for an action kind, for the review list. */
export function kindLabel(kind: string): string {
  return ({
    create_board: 'Board', create_column: 'Column', create_note: 'Note',
    create_todo: 'To-do', create_link: 'Link', move_element: 'Move',
    rename: 'Rename', set_note_text: 'Edit', delete_element: 'Trash',
    apply_label: 'Tag', set_color: 'Colour', set_task_done: 'Tick',
    connect: 'Link', create_table: 'Table', clone_here: 'Mirror',
    place: 'Move', comment: 'Note',
  } as Record<string, string>)[kind] ?? kind;
}

/** Human-readable summary of a terminal run, for the outcome card. */
export function outcomeText(run: AgentRun): { tone: 'ok' | 'warn' | 'bad'; title: string; detail: string } {
  switch (run.state) {
    case 'COMPLETED': {
      const n = run.plan?.actions.length ?? 0;
      return { tone: 'ok', title: 'Done', detail: `${n} change${n === 1 ? '' : 's'} applied` };
    }
    case 'REVERTED':
      return { tone: 'ok', title: 'Reverted', detail: 'The board is back as it was' };
    case 'PARTIAL':
      return { tone: 'warn', title: 'Nothing to do', detail: run.reason || 'No changes were needed' };
    case 'DISCARDED':
      return { tone: 'warn', title: 'Discarded', detail: 'Nothing was changed' };
    case 'CANCELLED':
      return { tone: 'warn', title: 'Stopped', detail: 'Nothing was changed' };
    case 'BUDGET_EXHAUSTED':
      return { tone: 'warn', title: 'Ran out of room', detail: run.reason || 'The run hit its limit' };
    case 'SECURITY_QUARANTINED':
      return { tone: 'bad', title: 'Stopped for safety', detail: run.reason || 'Something in this board looked unsafe' };
    default:
      return { tone: 'bad', title: 'Could not finish', detail: run.reason || 'Something went wrong' };
  }
}
