// The agent lives in one place: a bar at the bottom centre of the canvas.
//
// Why here. It is the one spot a person's eye returns to between actions, so
// the agent is always visible without ever being in the way — no hunting a rail
// icon, no panel eating a third of the board. It also keeps the *canvas* as the
// review surface: the plan appears as ghosts in place, and this bar is only
// where you ask and decide. The line-by-line list is one click away for when
// something looks wrong, rather than always on screen competing with the work.
//
// Everything grows upward from the bar so the anchor never moves.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import gsap from 'gsap';
import { useBoard } from '../store/boardStore';
import { uploadFile } from '../api/client';
import { toast } from '../components/ui/Toaster';
import { prompt } from '../components/ui/Prompt';
import { useView } from '../store/viewStore';
import { CloseIcon, ShieldIcon, SparkleIcon } from '../components/Icons';
import type { AgentAction, AgentAutonomy, AgentEvent, AgentRun, AgentScope } from '../api/types';
import {
  isCreate, isDestructive, kindLabel, outcomeText, useAgent, useEffectivePlan, WORKING,
} from './agentStore';

/** Height reserved at the bottom of the canvas so framing never hides behind the bar. */
export const BAR_RESERVE = 150;

export function AgentBar() {
  const enabled = useAgent((s) => s.capabilities?.enabled ?? false);
  const readOnly = useBoard((s) => s.readOnly);
  const run = useAgent((s) => s.run);
  const open = useAgent((s) => s.open);
  const ref = useRef<HTMLDivElement>(null);

  useLayoutEffect(() => {
    if (ref.current) {
      gsap.fromTo(ref.current, { y: 12, opacity: 0 }, { y: 0, opacity: 1, duration: 0.26, ease: 'power3.out' });
    }
  }, [!!run, open]);

  if (!enabled || readOnly) return null;

  return (
    <div className="agent-bar">
      <div ref={ref} className={`agent-shell${run || open ? ' expanded' : ''}`}>
        {!run && !open && <Pill />}
        {!run && open && <Ask />}
        {run && WORKING.includes(run.state) && <Working run={run} />}
        {run?.state === 'PROPOSED' && <Decide run={run} />}
        {run && !WORKING.includes(run.state) && run.state !== 'PROPOSED' && <Done run={run} />}
      </div>
    </div>
  );
}

/**
 * Resting state: a quiet invitation that is always on screen.
 *
 * When the board has visibly drifted the pill says so instead — one clause,
 * dismissible, and clicking it sends the request rather than opening an empty
 * composer. It costs nothing to detect, so it can be honest about being
 * optional: a hint that nags is one people learn to ignore.
 */
function Pill() {
  const drift = useAgent((s) => s.drift);
  if (drift) {
    return (
      <div className="agent-pill drift">
        <SparkleIcon size={15} />
        <button className="drift-take" onClick={() => {
          useAgent.getState().dismissDrift();
          void useAgent.getState().start({ intent: drift.intent, scope: 'board', autonomy: 'preview' });
        }}>
          {drift.message} — <em>tidy it?</em>
        </button>
        <button className="drift-no" title="Not now"
          onClick={() => useAgent.getState().dismissDrift()}>
          <CloseIcon size={12} />
        </button>
      </div>
    );
  }
  return (
    <button className="agent-pill" onClick={() => useAgent.getState().setOpen(true)}>
      <SparkleIcon size={15} />
      <span>Ask Qomra to do anything on this board…</span>
      <kbd>⌘K</kbd>
    </button>
  );
}

// ---------------------------------------------------------------------------
// Ask
// ---------------------------------------------------------------------------

/* Short enough to sit on one line. Four wrapped to two rows, which made the
   resting card look like a menu before a single word had been typed. */
const PRESETS = [
  'Organize into columns',
  'Draft a project plan',
  'Summarize this board',
];

/** Auto-apply. No bolt in the shared set, and this is the only place one is used. */
function BoltIcon({ size = 12 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M13 2 4 14h7l-1 8 9-12h-7l1-8z" />
    </svg>
  );
}

/** The last request, recalled with ↑ — people re-run variations constantly. */
let lastIntent = '';

const scopeLabel = (s: AgentScope) =>
  s === 'board' ? 'This board' : s === 'unsorted' ? 'Unsorted' : 'Selection';

function Ask() {
  const busy = useAgent((s) => s.busy);
  const recent = useAgent((s) => s.recent);
  const audit = useAgent((s) => s.audit);
  const { boardId, elements, selection } = useBoard();
  const [intent, setIntent] = useState('');
  const [files, setFiles] = useState<{ id: string; name: string }[]>([]);
  const [uploading, setUploading] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const [autonomy, setAutonomy] = useState<AgentAutonomy>('preview');
  const [scope, setScope] = useState<AgentScope>('board');
  const inputRef = useRef<HTMLTextAreaElement>(null);

  const counts = useMemo(() => {
    let unsorted = 0;
    let canvas = 0;
    for (const el of Object.values(elements)) {
      if (el.location.parentId !== boardId || el.deletedAt || el.type === 'LINE') continue;
      if (el.location.section === 'UNSORTED') unsorted += 1;
      else canvas += 1;
    }
    return { unsorted, board: unsorted + canvas, selection: selection.size };
  }, [elements, boardId, selection]);

  useEffect(() => {
    // Opened from a right-click on a selection, the composer starts there —
    // otherwise the entry point would silently lose what you pointed at.
    const pending = useAgent.getState().pendingScope;
    setScope(pending ?? (counts.selection > 0 ? 'selection' : 'board'));
    const id = window.setTimeout(() => inputRef.current?.focus(), 50);
    return () => window.clearTimeout(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const go = () => {
    if (!intent.trim() || uploading) return;
    lastIntent = intent.trim();
    void useAgent.getState().start({
      intent, scope, autonomy,
      selectionIds: scope === 'selection' ? Array.from(selection) : undefined,
      attachmentIds: files.map((f) => f.id),
    });
    setIntent('');
    setFiles([]);
  };

  /**
   * Files attached to the REQUEST, as opposed to something already on the
   * board. Uploaded immediately through the ordinary presign pipeline, so by
   * the time the run starts the server has them and only needs their ids.
   */
  const attach = async (list: FileList | File[]) => {
    const picked = Array.from(list).slice(0, 4 - files.length);
    if (picked.length === 0) return;
    setUploading(true);
    try {
      const done = await Promise.all(picked.map(async (f) => {
        const { attachmentId } = await uploadFile(f);
        return { id: attachmentId, name: f.name };
      }));
      setFiles((prev) => [...prev, ...done].slice(0, 4));
    } catch {
      toast.error('That file could not be attached.');
    } finally {
      setUploading(false);
    }
  };

  // Only the scopes that mean something right now: "selection" is not offered
  // when nothing is selected, so the chip never cycles through a dead option.
  const scopes: AgentScope[] = ['board', 'unsorted', ...(counts.selection > 0 ? ['selection' as const] : [])];
  const cycleScope = () => setScope(scopes[(scopes.indexOf(scope) + 1) % scopes.length]);

  const board = boardId ? elements[boardId] : undefined;
  const rules = (board?.content?.agentInstructions as string | undefined) ?? '';
  const editRules = async () => {
    const next = await prompt({
      title: 'How this board works',
      placeholder: 'e.g. Columns are pipeline stages — never add one. Tag by owner.',
      defaultValue: rules,
      confirmLabel: 'Save',
      multiline: true,
    });
    if (next === null || !boardId || next === rules) return;
    // Through the ordinary transaction path, so it is undoable and syncs like
    // any other edit rather than being agent-only state.
    void useBoard.getState().commitTransaction([{
      elementId: boardId,
      action: 'update',
      changes: { content: { agentInstructions: next } },
      undoChanges: { content: { agentInstructions: rules } },
    }]);
  };
  const revertible = recent.filter((r) => r.state === 'COMPLETED');

  return (
    <div className="agent-card">
      {!intent && (
        <div className="ac-presets">
          {PRESETS.map((p) => (
            <button key={p} className="ac-preset" onClick={() => { setIntent(p); inputRef.current?.focus(); }}>
              {p}
            </button>
          ))}
        </div>
      )}

      {/* Earlier runs stay revertible — regret usually arrives a few minutes late. */}
      {!intent && revertible.length > 0 && (
        <div className="ac-recent">
          {revertible.slice(0, 2).map((r) => (
            <div key={r.id} className="ac-recent-row">
              <span title={r.task.intent}>{r.task.intent}</span>
              <button onClick={async () => { useAgent.setState({ run: r }); await useAgent.getState().revert(); }}>
                Undo
              </button>
            </div>
          ))}
        </div>
      )}

      {/* What the agent has already changed here. Shown only when there is
          something to show, and only before you start typing. */}
      {!intent && revertible.length === 0 && audit.length > 0 && (
        <div className="ac-recent">
          {audit.slice(0, 2).map((a) => (
            <div key={a.runId} className="ac-recent-row">
              <span title={a.intent}>{a.intent}</span>
              <em className="ac-audit-note">
                {a.reverted ? 'reverted' : `${a.ops} change${a.ops === 1 ? '' : 's'}`}
              </em>
            </div>
          ))}
        </div>
      )}

      {/* Attached files, before the field, so it is obvious they are part of
          the request rather than something the agent found. */}
      {files.length > 0 && (
        <div className="ac-files">
          {files.map((f) => (
            <span key={f.id} className="ac-file">
              {f.name}
              <button title="Remove" onClick={() => setFiles((p) => p.filter((x) => x.id !== f.id))}>
                <CloseIcon size={10} />
              </button>
            </span>
          ))}
        </div>
      )}

      <div className="ac-inputrow">
        <SparkleIcon size={16} />
        <textarea
          ref={inputRef}
          className="ac-intent"
          dir="auto"
          rows={1}
          value={intent}
          placeholder="What would you like done on this board?"
          onChange={(e) => {
            setIntent(e.target.value);
            // Grow with the text instead of scrolling a two-line box.
            e.target.style.height = 'auto';
            e.target.style.height = `${Math.min(e.target.scrollHeight, 140)}px`;
          }}
          onPaste={(e) => {
            const f = Array.from(e.clipboardData.files);
            if (f.length) { e.preventDefault(); void attach(f); }
          }}
          onDrop={(e) => {
            if (e.dataTransfer.files.length) { e.preventDefault(); void attach(e.dataTransfer.files); }
          }}
          onDragOver={(e) => { if (e.dataTransfer.types.includes('Files')) e.preventDefault(); }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); go(); }
            if (e.key === 'ArrowUp' && !intent && lastIntent) { e.preventDefault(); setIntent(lastIntent); }
          }}
        />
        {/* The arrow is compact, but on its own it never says what pressing it
            does. The label carries the outcome of the current mode, so the
            promise is legible before the click, not after. */}
        <input ref={fileRef} type="file" multiple hidden
          accept="image/png,image/jpeg,image/webp,image/gif,application/pdf"
          onChange={(e) => { void attach(e.target.files ?? []); e.target.value = ''; }} />
        <button className="ac-clip" title="Attach an image or PDF to this request"
          disabled={uploading || files.length >= 4} onClick={() => fileRef.current?.click()}>
          {uploading ? '…' : '+'}
        </button>
        <button
          className="ac-send"
          disabled={busy || !intent.trim()}
          onClick={go}
          title={autonomy === 'preview'
            ? 'Plan it (Enter) — nothing is written until you accept'
            : 'Do it (Enter) — applies straight away, still one undo'}
        >
          {busy ? '…' : '↑'}
        </button>
      </div>

      {/* Two chips, each showing its current value. The old row spelled the
          options out in two segmented tracks with lead-in words — five controls
          and three labels to say two things that already have safe defaults. */}
      <div className="ac-controls">
        <button className="ac-chip" onClick={cycleScope}
          title={`The agent can see ${scopeLabel(scope)} — click to change`}>
          {scopeLabel(scope)} <b>{counts[scope]}</b>
        </button>
        <button
          className={`ac-chip mode ${autonomy}`}
          onClick={() => setAutonomy(autonomy === 'preview' ? 'auto' : 'preview')}
          title={autonomy === 'preview'
            ? 'Preview: the plan is shown first and nothing is written until you accept. Click for auto-apply.'
            : 'Auto: changes are applied straight away — still one undo, still revertible, and never deletes. Click to preview first.'}
        >
          {autonomy === 'preview' ? <ShieldIcon size={12} /> : <BoltIcon size={12} />}
          {autonomy === 'preview' ? 'Preview' : 'Auto-apply'}
        </button>
        <span className="ac-spacer" />
        {/* Board conventions the agent should always honour. Author-written:
            rules it inferred for itself would be invisible and could not be
            argued with. */}
        <button className="ac-chip" onClick={editRules}
          title="Standing notes Qomra should follow on this board">
          {rules ? 'Rules ✓' : 'Rules'}
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Working — the plan writes itself
// ---------------------------------------------------------------------------

function phraseFor(ev: AgentEvent | undefined): string {
  if (!ev) return 'Reading the board…';
  if (ev.type === 'action.staged') return ev.message || 'Planning a change';
  if (ev.message?.startsWith('read board')) return 'Reading the board…';
  if (ev.message?.includes('read_board')) return 'Looking inside a board…';
  if (ev.message?.includes('search')) return 'Searching your boards…';
  if (ev.message?.includes('create')) return 'Drafting the structure…';
  if (ev.message?.includes('move')) return 'Deciding where things go…';
  return 'Thinking…';
}

function Working({ run }: { run: AgentRun }) {
  const events = useAgent((s) => s.events);
  const staged = events.filter((e) => e.type === 'action.staged');
  const latest = [...events].reverse().find((e) => e.type === 'step.finished' || e.type === 'action.staged');

  return (
    <div className="agent-card">
      {/* Staged changes tick past as they are decided, so the wait is spent
          watching the plan form rather than watching a spinner. */}
      {staged.length > 0 && (
        <ol className="ac-ticker">
          {staged.slice(-4).map((e) => (
            <li key={e.sequence} className={e.data?.destructive ? 'danger' : ''}>
              <span className={`ap-kind k-${e.data?.kind}`}>{kindLabel(String(e.data?.kind ?? ''))}</span>
              <span>{e.message}</span>
            </li>
          ))}
        </ol>
      )}
      <div className="ac-statusrow">
        <span className="dock-pulse" />
        <span className="ac-status">{phraseFor(latest)}</span>
        {staged.length > 0 && <span className="ac-badge">{staged.length} so far</span>}
        <button className="ac-ghostbtn" onClick={() => void useAgent.getState().cancel()}>Stop</button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Decide
// ---------------------------------------------------------------------------

function Decide({ run }: { run: AgentRun }) {
  const busy = useAgent((s) => s.busy);
  const adjustments = useAgent((s) => s.adjustments);
  const effective = useEffectivePlan();
  const [details, setDetails] = useState(false);
  const [revising, setRevising] = useState(false);
  const [note, setNote] = useState('');
  const plan = run.plan;

  const send = () => {
    if (!note.trim()) return;
    void useAgent.getState().refine(note);
    setNote('');
    setRevising(false);
  };

  if (!plan) return null;

  // A question is a plan with nothing in it: the run stopped rather than guess
  // wrong. Answering is one click, and the answer travels the refine path.
  if (plan.question) {
    return (
      <div className="agent-card">
        <div className="ac-question">
          <SparkleIcon size={15} />
          <span>{plan.question.text}</span>
        </div>
        <div className="ac-answers">
          {(plan.question.options ?? []).map((o) => (
            <button key={o} className="ac-answer" disabled={busy}
              onClick={() => void useAgent.getState().refine(o)}>
              {o}
            </button>
          ))}
          <span className="ac-spacer" />
          <button className="ac-ghostbtn" disabled={busy}
            onClick={() => void useAgent.getState().discard()}>
            Never mind
          </button>
        </div>
      </div>
    );
  }

  if (!effective) return null;

  const n = effective.actions.length;
  const created = effective.actions.filter((a) => isCreate(a.kind)).length;
  const destructive = effective.actions.filter((a) => isDestructive(a.kind)).length;

  return (
    <div className="agent-card">
      {details && <PlanList plan={plan} />}

      {plan.summary && !details && <div className="ac-summary">{plan.summary}</div>}

      {/* Containment already dropped the foreign ids. This says the rest of the
          run is suspect too, and is why it did not auto-apply. */}
      {plan.quarantined && (
        <div className="ac-warn">
          Held for review — something written on this board tried to redirect Qomra.
          The instructions were ignored, but check this plan before applying it.
        </div>
      )}

      {destructive > 0 && (
        <div className="ac-warn">
          {destructive} item{destructive === 1 ? '' : 's'} will go to the trash — restorable afterwards.
        </div>
      )}

      {revising && (
        <div className="ac-revise">
          <input
            className="ac-revise-input"
            autoFocus
            dir="auto"
            placeholder="What should be different? e.g. fewer columns, group by date instead"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && note.trim()) { send(); }
              if (e.key === 'Escape') { e.stopPropagation(); setRevising(false); setNote(''); }
            }}
          />
          <button className="ac-apply" disabled={busy || !note.trim()} onClick={send}>
            {busy ? 'Thinking…' : 'Revise'}
          </button>
        </div>
      )}

      <div className="ac-decide">
        <SparkleIcon size={15} />
        <span className="ac-decide-text">
          <strong>{n}</strong> change{n === 1 ? '' : 's'}
          {created > 0 && <> · {created} new</>}
          {adjustments.length > 0 && <em> · {adjustments.length} edit{adjustments.length === 1 ? '' : 's'}</em>}
        </span>
        <button className="ac-ghostbtn" onClick={() => setDetails(!details)}>
          {details ? 'Hide' : 'Review'} {details ? '▾' : '▴'}
        </button>
        {/* Revising used to mean discarding and retyping, which threw away both
            the plan and the fact that it was wrong. */}
        <button className="ac-ghostbtn" title="Ask for a different plan" disabled={busy}
          onClick={() => setRevising((v) => !v)}>
          Revise
        </button>
        <button className="ac-ghostbtn" title="Discard (Esc)" disabled={busy}
          onClick={() => void useAgent.getState().discard()}>
          Discard
        </button>
        <button className="ac-apply" disabled={busy || n === 0} onClick={() => void useAgent.getState().apply()}>
          {busy ? 'Applying…' : 'Apply'}<kbd>⌘⏎</kbd>
        </button>
      </div>
    </div>
  );
}

function PlanList({ plan }: { plan: NonNullable<AgentRun['plan']> }) {
  const effective = useEffectivePlan();
  if (!effective) return null;
  const kept = new Set(effective.actions.map((a) => a.seq));
  return (
    <ol className="ac-plan">
      {(plan.actions ?? []).map((a) => (
        <PlanRow
          key={a.seq}
          action={effective.actions.find((e) => e.seq === a.seq) ?? a}
          dropped={!kept.has(a.seq)}
          depth={depthOf(a, plan.actions)}
        />
      ))}
    </ol>
  );
}

function depthOf(a: AgentAction, all: AgentAction[]): number {
  let depth = 0;
  let parent = a.parentId;
  for (let guard = 0; guard < 6 && parent; guard++) {
    const owner = all.find((x) => isCreate(x.kind) && x.elementId === parent);
    if (!owner) break;
    depth += 1;
    parent = owner.parentId;
  }
  return depth;
}

function PlanRow({ action, dropped, depth }: { action: AgentAction; dropped: boolean; depth: number }) {
  const hovered = useAgent((s) => s.hoverSeq === action.seq);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(action.title ?? action.text ?? '');
  const editable = action.title !== undefined || action.text !== undefined;

  const commit = () => {
    setEditing(false);
    const next = draft.trim();
    const current = action.title ?? action.text ?? '';
    if (!next || next === current) { setDraft(current); return; }
    useAgent.getState().adjust(
      action.title !== undefined
        ? { kind: 'retitle', seq: action.seq, value: next }
        : { kind: 'retext', seq: action.seq, value: next },
    );
  };

  return (
    <li
      className={`ap-row${dropped ? ' dropped' : ''}${isDestructive(action.kind) ? ' danger' : ''}${hovered ? ' hot' : ''}`}
      style={{ paddingInlineStart: 8 + depth * 14 }}
      // Hovering a row lights its ghost on the canvas: a line of text and a
      // rectangle in space should read as the same object.
      onMouseEnter={() => useAgent.getState().setHover(action.seq)}
      onMouseLeave={() => useAgent.getState().setHover(null)}
    >
      <span className={`ap-kind k-${action.kind}`}>{kindLabel(action.kind)}</span>
      {editing ? (
        <input
          className="ap-row-input" value={draft} autoFocus dir="auto"
          onFocus={(e) => e.currentTarget.select()}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
            if (e.key === 'Escape') { setDraft(action.title ?? action.text ?? ''); setEditing(false); }
          }}
        />
      ) : (
        <button className="ap-row-text" disabled={dropped || !editable} dir="auto"
          title={action.because || (editable ? 'Click to edit' : undefined)}
          onClick={() => { setDraft(action.title ?? action.text ?? ''); setEditing(true); }}>
          {action.title || action.text || action.summary}
        </button>
      )}
      {dropped ? (
        <button className="ap-row-x" title="Put this back" onClick={() => useAgent.getState().undoAdjust(action.seq)}>↩</button>
      ) : (
        <button className="ap-row-x" title="Leave this one out"
          onClick={() => useAgent.getState().adjust({ kind: 'drop', seq: action.seq })}>
          <CloseIcon size={10} />
        </button>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Done
// ---------------------------------------------------------------------------

function Done({ run }: { run: AgentRun }) {
  const busy = useAgent((s) => s.busy);
  const { tone, title, detail } = outcomeText(run);
  const revertible = run.state === 'COMPLETED' || run.state === 'PARTIAL';
  const failed = run.verdict?.criteria.filter((c) => !c.passed) ?? [];

  return (
    <div className="agent-card">
      {/* When verification fails the board has already been restored — say so
          plainly rather than leaving the user to wonder what state they are in. */}
      {failed.length > 0 && (
        <ul className="ac-checks">{failed.slice(0, 2).map((c) => <li key={c.name}>{c.detail || c.name}</li>)}</ul>
      )}
      <div className="ac-decide">
        <span className={`ac-outcome-dot ${tone}`} />
        <span className="ac-decide-text"><strong>{title}</strong> · {detail}</span>
        {run.usage?.costUsd > 0 && <span className="ac-badge">${run.usage.costUsd.toFixed(3)}</span>}
        {revertible && (
          <button className="ac-ghostbtn" disabled={busy} onClick={() => void useAgent.getState().revert()}>Undo</button>
        )}
        <button className="ac-apply" onClick={() => { useAgent.getState().dismiss(); useAgent.getState().setOpen(true); }}>
          Ask again
        </button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Keyboard + auto-framing
// ---------------------------------------------------------------------------

/**
 * Global agent keyboard map and the canvas auto-frame.
 *
 * Framing is the difference between a preview and a rumour: the agent places
 * new things below whatever is already on the board, so a user scrolled
 * elsewhere would watch nothing happen. When a plan arrives the canvas tweens
 * to show it, keeping clear of the bar at the bottom.
 */
export function useAgentShell() {
  const state = useAgent((s) => s.run?.state);
  const plan = useAgent((s) => s.run?.plan);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inField = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      const s = useAgent.getState();
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        if (!s.capabilities?.enabled) return;
        e.preventDefault();
        s.setOpen(!s.open);
        return;
      }
      if (e.key === 'Escape' && !s.run && s.open) { s.setOpen(false); return; }
      if (inField) return;
      if (e.key === 'Escape' && s.run?.state === 'PROPOSED') void s.discard();
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && s.run?.state === 'PROPOSED') void s.apply();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  useEffect(() => {
    if (state !== 'PROPOSED' || !plan) return;
    const boxes = plan.actions.map((a) => a.position).filter(Boolean) as Array<{ x: number; y: number; width: number }>;
    if (boxes.length === 0) return;
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    if (!viewport) return;

    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const b of boxes) {
      minX = Math.min(minX, b.x);
      minY = Math.min(minY, b.y);
      maxX = Math.max(maxX, b.x + b.width);
      maxY = Math.max(maxY, b.y + 260);
    }
    const pad = 70;
    const w = viewport.clientWidth;
    // Reserve the bar's height so the plan is centred in what is actually
    // visible, not behind the controls.
    const h = Math.max(240, viewport.clientHeight - BAR_RESERVE);
    const scale = Math.min(1.1, Math.max(0.3,
      Math.min(w / (maxX - minX + pad * 2), h / (maxY - minY + pad * 2))));
    const target = {
      panX: -(minX - pad) * scale + (w - (maxX - minX + pad * 2) * scale) / 2,
      panY: -(minY - pad) * scale + (h - (maxY - minY + pad * 2) * scale) / 2,
      scale,
    };
    const v = useView.getState();
    const from = { panX: v.panX, panY: v.panY, scale: v.scale };
    const tween = gsap.to(from, {
      ...target, duration: 0.55, ease: 'power3.inOut',
      onUpdate: () => useView.getState().setView(from.panX, from.panY, from.scale),
    });
    return () => { tween.kill(); };
  }, [state, plan]);
}
