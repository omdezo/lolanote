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
import type { PendingImport } from '../store/pasteImport';
import type {
  AgentAction, AgentAdjustment, AgentAutonomy, AgentCapabilities, AgentEvent,
  AgentActiveRun, AgentAuditEntry, AgentDrift, AgentPlan, AgentRun, AgentRunState, AgentScope,
} from '../api/types';
import { expectRunRevert, forgetRunRevert, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { toast } from '../components/ui/Toaster';
import { t, type TKey } from '../i18n';

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
  /**
   * The nested-board tile the review is pointed at, and the rows going into it.
   *
   * A ghost is one row and one rectangle, so hoverSeq is enough for it. A board
   * tile stands for every change filed anywhere inside it — often thirty of
   * them, none individually drawable here — so the link runs through the whole
   * set: hovering the badge lights those rows in the dock, and clicking pins
   * the review list to them.
   */
  tileFocus: { id: string; label: string; seqs: number[]; pinned: boolean } | null;
  /** Recent runs on this board, so an earlier one can still be reverted. */
  recent: AgentRun[];
  /**
   * A large paste waiting to become a request (JN7).
   *
   * Uploaded before the composer opens, so the bar never holds the only copy
   * of somebody's script breakdown: if the upload fails the paste falls back
   * to the ordinary card and this stays null.
   */
  pendingImport: PendingImport | null;
  setPendingImport(next: PendingImport | null): void;
  /**
   * What moved under the plan while it was being reviewed.
   *
   * The server has broadcast this on the run's event stream since exact-action
   * binding shipped, and no component ever read a run event — so the entire
   * recovery experience was a toast naming no element and no next step, over an
   * Apply button that stays enabled and fails identically every time.
   */
  stale: { ids: string[]; membership: boolean } | null;
  /** Read the board again and drop the stale flag if the drift has passed. */
  recheck(): Promise<void>;
  /**
   * The server's refusal when a retry would repeat a failure it has already
   * seen twice.
   *
   * Rendered rather than toasted, because the whole point of the refusal is
   * that Try again is the wrong next move: a toast disappears and leaves the
   * same primary button sitting there inviting a third identical pass. Held on
   * the store rather than in the card so it survives the card re-rendering
   * while the run row refreshes underneath it.
   */
  retryRefused: string | null;

  loadCapabilities(): Promise<void>;
  start(opts: { intent: string; scope: AgentScope; autonomy: AgentAutonomy; selectionIds?: string[]; attachmentIds?: string[]; source?: 'typed' | 'hint' }): Promise<void>;
  ingest(ev: AgentEvent & { state?: AgentRunState }): void;
  loadRecent(): Promise<void>;
  /** What the agent has actually changed on this board. */
  audit: AgentAuditEntry[];
  loadAudit(): Promise<void>;
  /** A quiet observation that this board wants attention. Costs no tokens. */
  drift: AgentDrift | null;
  /** How much of this board the agent can actually see, and how much it cannot. */
  reach: { visible: number; elided: number } | null;
  /** A run already under way here — yours to resume, or a colleague's to watch. */
  active: AgentActiveRun | null;
  loadBoardState(): Promise<void>;
  /** Re-enter a run that is already in flight, with its journal. */
  adopt(runId: string): Promise<void>;
  /** Fold a broadcast event from somebody else's run into the observer view. */
  observe(ev: AgentEvent & { state?: AgentRunState }): void;
  /** How many changes a colleague's run has staged so far. */
  observedStaged: number;
  dismissDrift(): void;
  setHover(seq: number | null): void;
  setTileFocus(focus: AgentState['tileFocus']): void;
  resync(): Promise<void>;
  adjust(a: AgentAdjustment): void;
  undoAdjust(seq: number): void;
  apply(): Promise<void>;
  /** Leave out the rows whose elements moved, and commit the rest. */
  applyWithoutStale(): Promise<void>;
  refine(note: string): Promise<void>;
  discard(): Promise<void>;
  cancel(): Promise<void>;
  revert(elementIds?: string[]): Promise<void>;
  /** Start again from a finished run's request, without retyping it. */
  retry(): Promise<void>;
  /** Pick up from what a run left undone; the server composes the request. */
  continueRun(): Promise<void>;
  /** Correct a run while it is still working. Delivered between model turns. */
  steer(note: string): Promise<void>;
  dismiss(): void;
  /** Scope the composer should start on, when opened from a selection. */
  pendingScope: AgentScope | null;
  setOpen(open: boolean, scope?: AgentScope): void;
}

/**
 * A run as it arrives from the server, made safe to render.
 *
 * A plan with no actions is a legitimate outcome — an answer to a question, or
 * a question of its own — and Go marshals an empty slice as `null`. Normalizing
 * once, at the single point every run enters this store, is the difference
 * between one defensive line and a null check at every call site that will be
 * forgotten the next time somebody adds one.
 */
function safeRun(run: AgentRun | null): AgentRun | null {
  if (run?.plan && !Array.isArray(run.plan.actions)) {
    return { ...run, plan: { ...run.plan, actions: [] } };
  }
  return run;
}

/**
 * What to show the person when the server refuses.
 *
 * This used to read `e.status === 409 ? t('agent.busy') : ...`, which threw the
 * server's own sentence away on exactly the two statuses that carry one. The
 * backend composes real refusals — "Sara started one 3 minutes ago", "this is
 * already being written to the board — you can undo it once it lands", "ask the
 * owner to add you as an editor" — and every one of them arrived at the browser
 * as the single generic word the constant supplied. Prefer the server's text;
 * the local string is the fallback for a bare status with nothing to say.
 */
function refusalText(err: unknown, fallback: string): string {
  const e = err as ApiError;
  if (e?.message) return e.message;
  if (e?.status === 409) return t('agent.busy');
  return fallback;
}

/**
 * The part of the board the person can currently see, in board coordinates.
 *
 * The agent was spatially blind: it knew everything on the board and nothing
 * about where anyone was working, so on a large board new content appeared
 * wherever the packer decided — often far from the area being worked on, which
 * reads as the agent ignoring the request.
 */
function visibleRegion(): { x: number; y: number; width: number; height: number } | undefined {
  const el = document.querySelector('.canvas-viewport') as HTMLElement | null;
  if (!el) return undefined;
  const { panX, panY, scale } = useView.getState();
  if (!scale) return undefined;
  return {
    x: -panX / scale,
    y: -panY / scale,
    width: el.clientWidth / scale,
    height: el.clientHeight / scale,
  };
}

/**
 * The newest staleness report in a run's journal.
 *
 * Two shapes reach the client and they mean different things. A per-element
 * fingerprint miss carries `staleElements` and names exactly what moved. A
 * membership change carries no ids at all — the board gained or lost items, so
 * the plan would commit against a board it never saw — and that one cannot be
 * re-checked away, only re-planned.
 */
function staleFromEvents(events: AgentEvent[]): { ids: string[]; membership: boolean } | null {
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i];
    if (ev.type !== 'error') continue;
    const ids = ev.data?.staleElements;
    if (Array.isArray(ids) && ids.length > 0) return { ids: ids.map(String), membership: false };
    if (ev.message?.includes('gained or lost')) return { ids: [], membership: true };
  }
  return null;
}

/**
 * Put the selection on what the run just made.
 *
 * Mirrors Action.CreatedIDs() on the server: for nearly every kind the created
 * id is the action's own elementId, but a `duplicate` names the SOURCE it
 * copies and writes the ids in `copies` — treating them as the same thing
 * would select the original and leave the copy unselected.
 *
 * Only ids the board actually has after the refresh: a plan that filed
 * everything into a nested board created nothing on this canvas, and selecting
 * ids that are not here would clear the person's own selection for nothing.
 */
function selectWhatItMade(run: AgentRun | null): void {
  const actions = run?.plan?.actions ?? [];
  const ids: string[] = [];
  for (const a of actions) {
    if (a.kind === 'duplicate') {
      for (const c of a.copies ?? []) ids.push(c.newId);
    } else if (isCreate(a.kind) && a.elementId) {
      ids.push(a.elementId);
    }
  }
  const board = useBoard.getState();
  const here = ids.filter((id) => {
    const el = board.elements[id];
    return el && !el.deletedAt && el.location.parentId === board.boardId;
  });
  if (here.length > 0) board.select(here);
}

/** Boards the user waved off this session. Persisting it would be worse: a
 *  board that drifts again later genuinely is worth mentioning again. */
const dismissedBoards = new Set<string>();

/** Foreign runs we have already asked the server to identify, so a burst of
 *  events from one run does not become a burst of requests. */
const observing = new Set<string>();

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
  tileFocus: null,
  recent: [],
  pendingImport: null,
  stale: null,
  retryRefused: null,
  pendingScope: null,
  audit: [],
  drift: null,
  reach: null,
  active: null,
  observedStaged: 0,

  async loadCapabilities() {
    try {
      set({ capabilities: await api.agentCapabilities() });
    } catch {
      // A deployment without the agent configured simply has no capabilities;
      // every entry point stays hidden rather than erroring at the user.
      set({ capabilities: { enabled: false, can: [], cannot: [], limits: { maxActions: 0, maxSteps: 0 } } });
    }
  },

  async start({ intent, scope, autonomy, selectionIds, attachmentIds, source }) {
    const boardId = useBoard.getState().boardId;
    if (!boardId || !intent.trim()) return;
    set({ busy: true, events: [], adjustments: [], hoverSeq: null, tileFocus: null, retryRefused: null });
    try {
      const run = await api.agentCreateRun({
        boardId, intent: intent.trim(), scope, autonomy, selectionIds, attachmentIds,
        // Defaults to typed: everything that is not the ambient pill is a
        // person who wrote something.
        source: source ?? 'typed',
        viewport: visibleRegion(),
        // The filter the person has on screen is part of what they are asking
        // about. Without it, "tidy this up" while filtered to Blocked reads to
        // the agent as a request about the whole board.
        activeLabelIds: [...useView.getState().labelFilter],
      });
      set({ run: safeRun(run), busy: false });
    } catch (err) {
      set({ busy: false });
      toast.error(refusalText(err, t('agent.couldNotStart')));
    }
  },

  ingest(ev) {
    const { run } = get();
    if (!run || ev.runId !== run.id) {
      get().observe(ev);
      return;
    }
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
      set({ run: safeRun(fresh), events });
    } catch { /* the panel keeps showing what it has */ }
  },

  adjust(a) { set((s) => ({ adjustments: [...s.adjustments, a] })); },

  /** Remove every adjustment aimed at one action — the "put it back" affordance. */
  undoAdjust(seq) { set((s) => ({ adjustments: s.adjustments.filter((a) => a.seq !== seq) })); },

  /**
   * Drop the rows that went stale, and apply the rest.
   *
   * The server checks exact-action binding against the plan AFTER adjustments
   * now, so removing the rows aimed at elements that moved leaves a plan that
   * was computed against the board as it still is. Before that, one element
   * moving under a plan made every subsequent Apply fail identically and the
   * only exit was discarding a run the person had already paid for.
   */
  async applyWithoutStale() {
    const { run, stale, adjustments } = get();
    if (!run?.plan || !stale || stale.membership) return;
    const drop = new Set(stale.ids);
    const dropped: AgentAdjustment[] = run.plan.actions
      .filter((a) => a.elementId && drop.has(a.elementId))
      .map((a) => ({ kind: 'drop', seq: a.seq }));
    if (dropped.length === 0) return;
    // Merged with what the person had already edited by hand, and de-duplicated
    // so a row they had dropped themselves is not dropped twice.
    const seen = new Set(adjustments.filter((a) => a.kind === 'drop').map((a) => a.seq));
    set({ adjustments: [...adjustments, ...dropped.filter((a) => !seen.has(a.seq))] });
    await get().apply();
  },

  async apply() {
    const { run, adjustments } = get();
    if (!run) return;
    set({ busy: true });
    try {
      const applied = await api.agentApply(run.id, adjustments);
      set({ run: safeRun(applied), busy: false, adjustments: [], stale: null });
      // The agent's transaction arrives over the board socket like any other,
      // so the canvas updates itself. This refresh covers the case where the
      // socket is down and the user would otherwise see a stale board.
      //
      // Awaited, then the selection: every other creation path in the product
      // selects its result, and this one selected nothing — so after a run
      // every follow-up gesture (Ctrl+D, the floating action bar, "Ask Qomra
      // about these") targeted an empty selection. Selecting before the refresh
      // settles would name ids the store does not have yet.
      await useBoard.getState().refreshBoard();
      selectWhatItMade(applied);
    } catch (err) {
      set({ busy: false });
      const e = err as ApiError;
      if (e?.status === 409) {
        // Exact-action binding: the board moved under the plan, so the
        // approval no longer describes reality. Re-read rather than commit —
        // and RENDER the drift, because a toast that names no element, no
        // colleague and no next step leaves an enabled Apply button that will
        // fail every time it is pressed.
        void useBoard.getState().refreshBoard();
        await get().resync();
        set({ stale: staleFromEvents(get().events) });
      } else {
        toast.error(e?.message || t('agent.couldNotApply'));
      }
    }
  },

  /**
   * Read the board again and see whether the drift still touches this plan.
   *
   * Free, and honest about its limits: the server fingerprints per element
   * version, and the client cannot recompute that — but it can ask the
   * narrower question the fingerprint is standing in for, which is "has this
   * element been touched since the plan was made". An element back below that
   * line is no longer a reason to refuse. If the server disagrees, the next
   * Apply lands here again with a fresh list; nothing is lost by asking.
   */
  async recheck() {
    const { run, stale } = get();
    if (!run || !stale) return;
    set({ busy: true });
    await useBoard.getState().refreshBoard();
    await get().resync();
    const elements = useBoard.getState().elements;
    const planAt = Date.parse(run.createdAt);
    const still = stale.ids.filter((id) => {
      const el = elements[id];
      if (!el) return true; // gone from the board entirely: still a mismatch
      const touched = Date.parse(el.updatedAt);
      return Number.isNaN(touched) || Number.isNaN(planAt) || touched > planAt;
    });
    // Membership drift cannot un-happen — the board really did gain or lose
    // items — so it survives a re-check and only Revise clears it.
    set({
      busy: false,
      stale: still.length > 0 || stale.membership ? { ids: still, membership: stale.membership } : null,
    });
  },

  /**
   * Send a proposed plan back for another pass. The run keeps its identity and
   * its cost meter, so adjusting a plan no longer means paying for a fresh run
   * that has not been told what was wrong with the last one.
   */
  async refine(note) {
    const { run, adjustments } = get();
    if (!run || !note.trim()) return;
    set({ busy: true });
    try {
      // The hand edits go WITH the note, because they are the other half of the
      // same instruction. Sending only the sentence threw away every row the
      // person had just dropped, so the next pass proposed them again and they
      // had to delete them twice.
      const next = await api.agentRefine(run.id, note.trim(), adjustments);
      // Cleared only now that the server has them. Adjustments belonged to the
      // plan being replaced; carrying them forward would re-apply edits to
      // actions that no longer exist. The stale list goes with them: the new
      // plan was compiled against the board as it is now, so the drift it named
      // is no longer a drift.
      set({ run: safeRun(next), busy: false, adjustments: [], hoverSeq: null, tileFocus: null, stale: null });
    } catch (err) {
      set({ busy: false });
      toast.error((err as ApiError)?.message || t('agent.couldNotRevise'));
    }
  },

  async discard() {
    const { run, adjustments } = get();
    if (!run) return;
    set({ busy: true });
    try {
      // Read BEFORE the set() below clears them. They used to be cleared in the
      // same statement that fired this request, so the server never learned
      // that the person had edited four rows on their way to giving up — every
      // abandonment arrived as an undifferentiated "no".
      await api.agentDiscard(run.id, adjustments);
    } catch { /* discarding a run that already ended is not an error */ }
    set({ run: null, events: [], adjustments: [], busy: false, stale: null, retryRefused: null });
  },

  async cancel() {
    const { run } = get();
    if (!run) return;
    try {
      const stopped = await api.agentCancel(run.id);
      // The run the SERVER says it is now, not a guess. Clearing to null used
      // to happen outside the try, so a Stop that never left the browser wiped
      // the card while the run carried on writing to the board — the one state
      // where the person most needs the card to still be there.
      set({ run: safeRun(stopped), events: [], adjustments: [] });
    } catch (err) {
      // Includes the server refusing mid-write ("this is already being written
      // to the board — you can undo it once it lands"), which is information,
      // not a failure to report as silence.
      toast.error(refusalText(err, t('agent.couldNotStop')));
    }
  },

  /**
   * Undo a run, or just part of one.
   *
   * Passing element ids leaves the rest of the run standing — a run that got
   * four things right and one wrong no longer has to be thrown away whole.
   */
  async revert(elementIds) {
    const { run } = get();
    if (!run) return;
    set({ busy: true });
    expectRunRevert(run.id);
    try {
      const reverted = await api.agentRevert(run.id, elementIds);
      set({ run: safeRun(reverted), busy: false });
      void useBoard.getState().refreshBoard();
      // No toast: the bar reports the outcome inline, and a toast at the bottom
      // centre lands directly on top of it — the same sentence, twice, with the
      // second one hiding the first.
    } catch (err) {
      set({ busy: false });
      toast.error((err as ApiError)?.message || t('agent.couldNotRevert'));
    }
  },

  /**
   * Say something to a run that is still working.
   *
   * Watching a plan form with no control but Stop was the worst part of the
   * wait: by the third staged change you can usually tell it has misread you,
   * and killing the run threw away the tokens along with the work.
   */
  async steer(note) {
    const { run } = get();
    if (!run || !note.trim()) return;
    try {
      await api.agentSteer(run.id, note.trim());
    } catch (err) {
      toast.error((err as ApiError)?.message || t('agent.couldNotSteer'));
    }
  },

  /**
   * Recover from a failure without retyping. The intent, scope, attachments and
   * every steer already given are all still on the server; this reuses them.
   */
  async retry() {
    const { run } = get();
    if (!run) return;
    set({ busy: true });
    try {
      const fresh = await api.agentRetry(run.id);
      set({
        run: safeRun(fresh), busy: false, events: [], adjustments: [],
        hoverSeq: null, tileFocus: null, retryRefused: null,
      });
    } catch (err) {
      const e = err as ApiError;
      const message = refusalText(err, t('agent.couldNotStart'));
      // A 409 here is not "somebody else is running one" — that refusal is
      // authored server-side and says so in its own words: this failed the same
      // way twice, and a third pass costs money to produce the same answer.
      // Kept on the store so the card can stop offering Try again as the
      // obvious thing to press.
      set({ busy: false, retryRefused: e?.status === 409 ? message : null });
      toast.error(message);
    }
  },

  /**
   * Keep going from where a run stopped.
   *
   * A job bigger than one run's budget could only be continued by typing a
   * fresh prompt that knew nothing about the run before it — which is how
   * "complete" ended up building a second copy of the structure beside the
   * first. Nothing is sent but the id: the new request is composed on the
   * server from the finished run's own unmet list.
   */
  async continueRun() {
    const { run } = get();
    if (!run) return;
    set({ busy: true });
    try {
      const next = await api.agentContinue(run.id);
      set({ run: safeRun(next), busy: false, events: [], adjustments: [], hoverSeq: null, tileFocus: null });
    } catch (err) {
      set({ busy: false });
      toast.error(refusalText(err, t('agent.couldNotStart')));
    }
  },

  async loadRecent() {
    const boardId = useBoard.getState().boardId;
    if (!boardId) return;
    try {
      set({ recent: ((await api.agentRuns(boardId, 6)) ?? []).map((r) => safeRun(r)!) });
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

  /**
   * One read on board open: reach, drift, and any live run.
   *
   * Free by construction — a server-side pass over the board's shape, no model
   * call. That is what lets it run on every board open without costing
   * anything; the model only runs if the person accepts.
   */
  async loadBoardState() {
    const boardId = useBoard.getState().boardId;
    if (!boardId) { set({ drift: null, reach: null, active: null }); return; }
    try {
      const state = await api.agentBoardState(boardId);
      set({
        reach: { visible: state.visible, elided: state.elided },
        active: state.active ?? null,
        drift: dismissedBoards.has(boardId) ? null : (state.drift ?? null),
      });
      // Your own run, still going, from before a reload or a navigation.
      // Without this the bar showed the resting pill while the run kept
      // executing — so the board rearranged itself a minute later with no
      // explanation, and starting another was refused for a run the screen had
      // never mentioned.
      if (state.active?.mine && state.active.id && get().run?.id !== state.active.id) {
        await get().adopt(state.active.id);
      }
    } catch { set({ drift: null, reach: null, active: null }); }
  },

  /**
   * Someone else's run on a board you are both looking at.
   *
   * The journal has always been broadcast to the whole board; nothing ever
   * consumed it, because ingest returned early whenever the local store had no
   * run — which is always true for anyone who did not start it. So a colleague
   * watched cards move with no actor, no reason and no warning.
   *
   * What they get is the FACT and the SIZE. Not the plan, not the request:
   * those are the other person's words and the other person's decision. Enough
   * that the board changing has a visible cause, and no more.
   */
  observe(ev) {
    const { active } = get();
    // A run we have not heard of yet — ask who it belongs to. Once.
    if (!active || (active.id ?? '') !== ev.runId) {
      if (!observing.has(ev.runId)) {
        observing.add(ev.runId);
        void get().loadBoardState();
      }
    }
    const finished = ev.state && !WORKING.includes(ev.state) && ev.state !== 'PROPOSED';
    if (finished) {
      observing.delete(ev.runId);
      set({ active: null, observedStaged: 0 });
      // The writes arrive over the board socket like any other, so the canvas
      // is already correct. This only names who caused it.
      const who = active && !active.mine ? active.actor : '';
      if (who && ev.state === 'COMPLETED') toast.info(`${who} — ${t('agent.theirRunApplied')}`);
      void get().loadBoardState();
      return;
    }
    if (ev.state) {
      set((s) => ({ active: s.active ? { ...s.active, state: ev.state! } : s.active }));
    }
    if (ev.type === 'action.staged') {
      set((s) => ({ observedStaged: s.observedStaged + 1 }));
    }
  },

  /**
   * Re-enter a run already in flight: the authoritative run plus its whole
   * journal, so the bar resumes in the state it left — watching, or deciding.
   */
  async adopt(runId) {
    try {
      const [run, events] = await Promise.all([api.agentRun(runId), api.agentEvents(runId, 0)]);
      set({ run: safeRun(run), events, adjustments: [], hoverSeq: null, tileFocus: null });
    } catch { /* a run we cannot read is one we cannot resume */ }
  },

  /** Dismissed per board for the session — a hint that returns is a nag. */
  dismissDrift() {
    const boardId = useBoard.getState().boardId;
    if (boardId) dismissedBoards.add(boardId);
    set({ drift: null });
  },

  setHover(hoverSeq) { set({ hoverSeq }); },

  setTileFocus(tileFocus) { set({ tileFocus }); },

  dismiss() {
    set({ run: null, events: [], adjustments: [], hoverSeq: null, tileFocus: null });
    void get().loadRecent();
  },
  setPendingImport(next) { set({ pendingImport: next }); },

  setOpen(open, scope) {
    set({ open, pendingScope: scope ?? null });
    if (open) {
      void get().loadRecent();
      void get().loadAudit();
    }
  },
}));

/**
 * Undo part of a run from OUTSIDE the agent surface — this is what Ctrl+Z on an
 * adopted agent transaction calls.
 *
 * The two undo systems used to sit on the same ops and destroy each other: the
 * outcome card kept `RevertedElementIDs`, Ctrl+Z posted a raw inverse that the
 * run never heard about, and which of the two you got depended on whether you
 * had changed boards since. One op, one owner: the keyboard and the card are
 * now two surfaces on the same mechanism, which is what the auto-apply tooltip
 * has been promising all along.
 *
 * A free function rather than a store action because the caller is boardStore,
 * which agentStore imports — the dependency runs one way and the dynamic import
 * at the call site is what keeps it that way.
 */
export async function revertRunElements(runId: string, elementIds: string[]): Promise<void> {
  try {
    const reverted = await api.agentRevert(runId, elementIds);
    // Only when it IS the run on screen: undoing an old run from the keyboard
    // must not replace the card the person is currently looking at.
    if (useAgent.getState().run?.id === runId) useAgent.setState({ run: safeRun(reverted) });
    void useBoard.getState().refreshBoard();
  } catch (err) {
    forgetRunRevert(runId);
    toast.error((err as ApiError)?.message || t('agent.couldNotRevert'));
    void useBoard.getState().refreshBoard();
  }
}

/** A plan with the user's adjustments folded in, for rendering only. */
export interface EffectivePlan {
  actions: AgentAction[];
  /** Sequences the user removed, so the review list can offer them back. */
  dropped: number[];
  /** The subset they actually clicked — the rest went with a container. */
  explicitlyDropped: number[];
  /** For each explicit drop, how many further rows went with it. */
  cascadedFrom: Map<number, number>;
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
  // Exported, so it is reachable with a plan that never passed through the
  // store's normalizer. An answer with no changes serializes actions as null,
  // and this walks them several times below.
  if (!Array.isArray(plan.actions)) plan = { ...plan, actions: [] };

  const dropped = new Set<number>();
  const retitle = new Map<number, string>();
  const retext = new Map<number, string>();
  const reparent = new Map<number, string>();
  for (const a of adjustments) {
    if (a.seq < 0 || a.seq >= (plan.actions?.length ?? 0)) continue;
    if (a.kind === 'drop') dropped.add(a.seq);
    if (a.kind === 'retitle') retitle.set(a.seq, a.value);
    if (a.kind === 'retext') retext.set(a.seq, a.value);
    if (a.kind === 'reparent') reparent.set(a.seq, a.value);
  }
  // The EFFECTIVE parent, so the cascade below sees where a redirected action
  // is actually going. The server does the same; a projection that disagreed
  // would show a card surviving a column it will not survive.
  const parentOf = (i: number) => reparent.get(i) ?? plan!.actions[i].parentId;

  // Cascade: an action parented to a dropped create cannot survive it — the
  // child would have nowhere to go. The server does the same, so the preview
  // and the commit agree about what a single click removed.
  //
  // `killedBy` attributes each cascaded row to the CLICK that removed it, not
  // to the intermediate row it hung from, so the review list can say "and the
  // 6 changes inside it" against the thing the person actually pressed. It is
  // also the honest half: dropping a container silently removed nine children
  // with no row saying so, which made a reviewer's drop a bigger act than they
  // knew — and the correction miner learned one rejection where there were ten.
  const dead = new Map<string, number>();
  const killedBy = new Map<number, number>();
  const explicit = new Set(dropped);
  for (const seq of dropped) {
    for (const id of createdIds(plan.actions[seq])) dead.set(id, seq);
  }
  for (let changed = true; changed;) {
    changed = false;
    plan.actions.forEach((a, i) => {
      if (dropped.has(i)) return;
      const parent = parentOf(i);
      const cause = parent ? dead.get(parent) : undefined;
      if (cause === undefined) return;
      dropped.add(i);
      killedBy.set(i, cause);
      for (const id of createdIds(a)) dead.set(id, cause);
      changed = true;
    });
  }
  const cascade = new Map<number, number>();
  for (const cause of killedBy.values()) cascade.set(cause, (cascade.get(cause) ?? 0) + 1);

  const actions: AgentAction[] = [];
  plan.actions.forEach((a, i) => {
    if (dropped.has(i)) return;
    const to = reparent.get(i);
    actions.push({
      ...a,
      title: retitle.get(i) ?? a.title,
      text: retext.get(i) ?? a.text,
      // Destination and position are the server's to recompute on apply. Until
      // then the row shows nothing rather than the stale previous answer, which
      // would say the change is still going where it no longer is.
      ...(to ? { parentId: to, destination: undefined, position: undefined } : {}),
    });
  });
  return {
    actions,
    dropped: [...dropped].sort((x, y) => x - y),
    explicitlyDropped: [...explicit].sort((x, y) => x - y),
    cascadedFrom: cascade,
  };
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
  'create_heading', 'place_file', 'comment',
  'write_document', 'add_color', 'link_board', 'duplicate',
]);
export function isCreate(kind: string): boolean { return CREATE_KINDS.has(kind); }

/**
 * Every element id an action brings into being — the mirror of the server's
 * Action.CreatedIDs.
 *
 * A duplicate creates one element per entry in `copies` and its own elementId
 * is not among them, so reading elementId alone left every copy looking alive
 * after the duplicate was dropped: the preview showed rows the commit was going
 * to remove. Kinds that create nothing return nothing, which is what stops an
 * edit acting as a parent in the cascade.
 */
function createdIds(a: AgentAction | undefined): string[] {
  if (!a) return [];
  if (a.kind === 'duplicate') return (a.copies ?? []).map((c) => c.newId);
  return isCreate(a.kind) && a.elementId ? [a.elementId] : [];
}
export function isDestructive(kind: string): boolean { return kind === 'delete_element'; }

/**
 * Short label for an action kind, for the review list.
 *
 * Most of these are element names the toolbar already translates, so the chip
 * on a plan row and the tool that makes the same thing read alike in both
 * languages. Only the verbs the toolbar has no word for get their own key.
 */
const KIND_KEYS: Record<string, TKey> = {
  create_board: 'tool.board', create_column: 'tool.column', create_note: 'tool.note',
  create_todo: 'tool.todo', create_link: 'tool.link', create_table: 'tool.table',
  connect: 'tool.link', comment: 'tool.note',
  move_element: 'agent.kind.move', place: 'agent.kind.move',
  rename: 'agent.kind.rename', set_note_text: 'agent.kind.edit',
  delete_element: 'agent.kind.trash', apply_label: 'agent.kind.tag',
  set_color: 'agent.kind.colour', set_task_done: 'agent.kind.tick',
  clone_here: 'agent.kind.mirror',
  create_heading: 'tool.heading', place_file: 'tool.image',
  write_document: 'tool.document', add_color: 'tool.color',
  link_board: 'agent.kind.shortcut', edit_table: 'tool.table',
  set_url: 'tool.link', set_caption: 'agent.kind.caption',
  collapse: 'agent.kind.collapse', duplicate: 'agent.kind.copy',
  convert: 'agent.kind.convert', add_tasks: 'tool.todo',
  set_assignee: 'agent.kind.assign', set_reminder: 'agent.kind.remind',
  resize: 'agent.kind.resize',
  set_direction: 'agent.kind.direction', resolve_thread: 'agent.kind.resolve',
  restore: 'agent.kind.restore', style_board: 'agent.kind.tile',
};

export function kindLabel(kind: string, translate: (key: TKey) => string = t): string {
  const key = KIND_KEYS[kind];
  return key ? translate(key) : kind;
}

/**
 * Human-readable summary of a terminal run, for the outcome card.
 *
 * `translate` is a parameter rather than a module import so this stays a pure
 * function of (run, language) — the card subscribes with useT() and passes the
 * live translator, and the tests can assert the branch without a language.
 *
 * FAILED and DENIED get their own copy. They used to share the `default:` arm,
 * so "you do not have permission to edit this board" and "the model produced
 * something invalid" rendered as the same sentence — one is a thing the user
 * can act on, the other is not, and reading them as identical teaches people
 * that the agent breaks at random.
 */
export function outcomeText(
  run: AgentRun,
  translate: (key: TKey) => string = t,
): { tone: 'ok' | 'warn' | 'bad'; title: string; detail: string } {
  const say = (key: TKey) => translate(key);
  switch (run.state) {
    case 'COMPLETED': {
      const n = run.plan?.actions?.length ?? 0;
      return { tone: 'ok', title: say('agent.out.done'), detail: `${n} ${plural(n, say('agent.change'), say('agent.changes'))} ${say('agent.out.doneDetail')}` };
    }
    case 'REVERTED':
      return { tone: 'ok', title: say('agent.out.reverted'), detail: say('agent.out.revertedDetail') };
    case 'PARTIAL':
      return { tone: 'warn', title: say('agent.out.nothing'), detail: run.reason || say('agent.out.nothingDetail') };
    case 'DISCARDED':
      return { tone: 'warn', title: say('agent.out.discarded'), detail: say('agent.out.discardedDetail') };
    case 'CANCELLED':
      return { tone: 'warn', title: say('agent.out.cancelled'), detail: say('agent.out.cancelledDetail') };
    case 'BUDGET_EXHAUSTED':
      return { tone: 'warn', title: say('agent.out.exhausted'), detail: run.reason || say('agent.out.exhaustedDetail') };
    case 'SECURITY_QUARANTINED':
      return { tone: 'bad', title: say('agent.out.unsafe'), detail: run.reason || say('agent.out.unsafeDetail') };
    case 'DENIED':
      // Not a malfunction: the run asked for authority this person does not
      // have here. The reason from the server names what was refused.
      return { tone: 'warn', title: say('agent.out.denied'), detail: run.reason || say('agent.out.deniedDetail') };
    case 'FAILED':
    default:
      return { tone: 'bad', title: say('agent.out.failed'), detail: run.reason || say('agent.out.failedDetail') };
  }
}

/** Arabic has no -s plural, so counting words is a dictionary lookup, not a suffix. */
export function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}
