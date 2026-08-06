// Decide — the one screen where a person accepts or refuses a plan.
//
// The canvas is the real review surface (ghosts in place); this card is the
// summary and the decision. The line-by-line list is one click away, for when
// something looks wrong.
import { useEffect, useState } from 'react';
import { SparkleIcon, CloseIcon } from '../components/Icons';
import { useT, type TKey } from '../i18n';
import { deadlineLabel, remaining, withinWarning, TEN_MINUTES } from '../lib/deadline';
import { useBoard } from '../store/boardStore';
import type { AgentAction, AgentRun, QElement } from '../api/types';
import { PlanNotes } from './PlanNotes';
import { frameProposal } from './useAgentShell';
import {
  isCreate, isDestructive, kindLabel, plural, useAgent, useEffectivePlan,
} from './agentStore';

export function Decide({ run }: { run: AgentRun }) {
  const busy = useAgent((s) => s.busy);
  const adjustments = useAgent((s) => s.adjustments);
  const effective = useEffectivePlan();
  const boardId = useBoard((s) => s.boardId);
  const t = useT();
  const [details, setDetails] = useState(false);
  const [revising, setRevising] = useState(false);
  // Clicking a board tile's badge on the canvas opens the list already narrowed
  // to that destination. Opening it by hand would be the person doing the work
  // the badge just offered to do for them.
  const tileFocus = useAgent((s) => s.tileFocus);
  const stale = useAgent((s) => s.stale);
  const showList = details || !!tileFocus?.pinned;
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
            {t('agent.neverMind')}
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
      {/* AX31, and it is the whole item. The ghost layer is this product's
          central claim — "what you approve is positioned identically to what
          commits" — and it is a purely VISUAL assertion. For a blind reviewer
          the plan had a list, not a preview, and that list was a secondary
          surface opened by a `▴` glyph and collapsed by default.
          The accessibility answer inverts the design: the LIST is canonical for
          assistive technology and the canvas is the enhancement. So the list is
          always rendered and merely hidden from sight when the canvas preview
          is the active surface — which is also the compiler clause, because two
          renderers that only one of them exists at a time is how they drift. */}
      <div className={showList ? undefined : 'sr-only'}>
        <PlanList plan={plan} boardId={boardId} />
      </div>

      {/* Staleness was a toast that named no element, no colleague and no next
          step, over an Apply button that stayed enabled and would fail every
          time it was pressed. Nothing recomputes the plan's fingerprint, so the
          second attempt fails identically to the first — the two exits that do
          work are Revise (which keeps this run and its cost meter) and Discard,
          and neither was ever suggested. */}
      {stale && <StaleNotice onRevise={() => setRevising(true)} />}

      {plan.summary && !showList && <div className="ac-summary">{plan.summary}</div>}

      {/* Before the decision, not after it: what the plan leaves undone is part
          of what is being approved. */}
      <PlanNotes plan={plan} />

      {/* Containment already dropped the foreign ids. This says the rest of the
          run is suspect too, and is why it did not auto-apply. */}
      {plan.quarantined && <div className="ac-warn">{t('agent.quarantined')}</div>}

      {destructive > 0 && (
        <div className="ac-warn">
          {destructive} {plural(destructive, t('agent.item'), t('agent.items'))} {t('agent.toTrash')}
        </div>
      )}

      {/* JN21. The proposal has always had a deadline — the server computes and
          stores `proposalExpiresAt`, and past it the delegation stops holding —
          and no surface in the product has ever said so. A person reading forty
          staged actions did not know the reading itself was timed, and the only
          way to find out was to press Apply and be refused.
          It appears inside ten minutes and not before: a clock on a
          twenty-nine-minute window is furniture people learn to stop reading. */}
      <ProposalDeadline at={run.proposalExpiresAt} />


      {revising && (
        <div className="ac-revise">
          <input
            className="ac-revise-input"
            autoFocus
            dir="auto"
            aria-label={t('agent.revisePlaceholder')}
            placeholder={t('agent.revisePlaceholder')}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && note.trim()) { send(); }
              if (e.key === 'Escape') { e.stopPropagation(); setRevising(false); setNote(''); }
            }}
          />
          <button className="ac-apply" disabled={busy || !note.trim()} onClick={send}>
            {busy ? t('agent.thinking') : t('agent.revise')}
          </button>
        </div>
      )}

      <div className="ac-decide">
        <SparkleIcon size={15} />
        <span className="ac-decide-text">
          <strong>{n}</strong> {plural(n, t('agent.change'), t('agent.changes'))}
          {created > 0 && <> · {created} {t('agent.new')}</>}
          {adjustments.length > 0 && (
            <em> · {adjustments.length} {plural(adjustments.length, t('agent.edit'), t('agent.edits'))}</em>
          )}
        </span>
        {/* What this plan has already cost to produce. Applying adds nothing —
            saying so here is the difference between a knowable price and a
            number a person only meets on the receipt. */}
        {run.usage?.costUsd > 0 && (
          <span className="ac-badge" title={t('agent.costSoFar')}>${run.usage.costUsd.toFixed(3)}</span>
        )}
        {/* Framing happens once, when the plan arrives. After that the canvas
            belongs to the person — this is how they ask for it back. */}
        <button className="ac-ghostbtn" title={t('agent.showMe')} aria-label={t('agent.showMe')}
          onClick={frameProposal}>
          <span aria-hidden="true">⤢</span>
        </button>
        <button className="ac-ghostbtn" title={t('agent.reviewHint')}
          aria-expanded={showList}
          onClick={() => {
            if (tileFocus?.pinned) useAgent.getState().setTileFocus(null);
            setDetails(!showList);
          }}>
          {t(showList ? 'agent.hide' : 'agent.review')} <span aria-hidden="true">{showList ? '▾' : '▴'}</span>
        </button>
        {/* Revising used to mean discarding and retyping, which threw away both
            the plan and the fact that it was wrong. */}
        <button className="ac-ghostbtn" title={t('agent.reviseHint')} disabled={busy}
          onClick={() => setRevising((v) => !v)}>
          {t('agent.revise')}
        </button>
        <button className="ac-ghostbtn" title={t('agent.discardHint')} disabled={busy}
          onClick={() => void useAgent.getState().discard()}>
          {t('agent.discard')}
        </button>
        {/* Disabled while stale, because an enabled button that has already
            been paid for and will fail every single time it is pressed is a
            worse answer than an honest refusal. */}
        <button className="ac-apply" disabled={busy || n === 0 || !!stale}
          title={stale ? t('agent.stale.cannotApply') : undefined}
          onClick={() => void useAgent.getState().apply()}>
          {busy ? t('agent.applying') : t('agent.apply')}<kbd>⌘⏎</kbd>
        </button>
      </div>
    </div>
  );
}

/**
 * How much of a person's attention one row deserves.
 *
 * Both prior documents treat human review as the load-bearing safety guarantee.
 * Human-factors work says that guarantee decays exactly as the system gets
 * good: reviewers anchor on the first items, scrutiny falls with list length,
 * and approval becomes the default. A 41-row flat list with one Apply button is
 * unreviewable by construction, and the product is heading toward longer ones.
 *
 * The bands are computed from COMMITTED facts, never from anything the model
 * says about its own work — a band a model can argue for is another thing to
 * argue with. The server is the right place for this (it holds the whole
 * subtree); until its band arrives on the action, the client derives what it
 * can from the board it has and fails SAFE: an element it cannot see is treated
 * as existing material, never as an addition.
 */
/**
 * The proposal's own clock (JN21).
 *
 * Silent until the last ten minutes, then a plain sentence — "6 minutes left" —
 * that re-reads itself every fifteen seconds so it never lies by more than a
 * quarter minute. `role="status"` rather than `alert`: this is worth hearing,
 * and it is not worth interrupting a review that is already under time pressure.
 */
function ProposalDeadline({ at }: { at?: string }) {
  const t = useT();
  const [, tick] = useState(0);
  useEffect(() => {
    if (!at) return;
    const id = setInterval(() => tick((n) => n + 1), 15_000);
    return () => clearInterval(id);
  }, [at]);

  const left = remaining(at);
  if (!left || !withinWarning(left, TEN_MINUTES)) return null;
  return (
    <div className={`ac-warn ac-deadline d-${left.urgency}`} role="status">
      {t('agent.proposalExpires')} — {deadlineLabel(left, t)}
    </div>
  );
}

type RiskBand = 'touches-existing' | 'structural' | 'additive';

function bandOf(a: AgentAction, elements: Record<string, QElement>, boardId: string): RiskBand {
  // The server's band wins whenever it sent one: it walked the whole subtree,
  // and this function cannot see past the board that is loaded. Everything below
  // is the fallback for a plan made before the field existed — correct as far as
  // it can see, and safe past that.
  if (a.risk === 'touches-existing' || a.risk === 'structural' || a.risk === 'additive') {
    return a.risk;
  }
  // Deleting, and anything aimed at something that was here before this run,
  // is never collapsible. That is the invariant, not a heuristic.
  if (isDestructive(a.kind)) return 'touches-existing';
  if (!isCreate(a.kind)) return 'touches-existing';
  // A new container that will hold existing material is not simply additive:
  // approving it approves a re-filing of things the person already placed.
  if (CONTAINER_KINDS.has(a.kind)) return 'structural';
  // A create whose id already names something on this board is a repair of
  // existing material, not an addition.
  const existing = elements[a.elementId];
  if (existing && !existing.deletedAt) return 'touches-existing';
  // A locked element in the destination chain: never collapsible.
  const parent = a.parentId && a.parentId !== boardId ? elements[a.parentId] : undefined;
  if (parent?.content?.locked) return 'touches-existing';
  return 'additive';
}

/** The four narrowings, which are the questions a reviewer actually has. */
type RowFilter = 'all' | 'new' | 'moves' | 'deletes';

function matchesFilter(a: AgentAction, filter: RowFilter): boolean {
  switch (filter) {
    case 'new': return isCreate(a.kind);
    case 'moves': return a.kind === 'move_element' || a.kind === 'place';
    case 'deletes': return isDestructive(a.kind);
    default: return true;
  }
}

function PlanList({ plan, boardId }: { plan: NonNullable<AgentRun['plan']>; boardId: string }) {
  const effective = useEffectivePlan();
  const tileFocus = useAgent((s) => s.tileFocus);
  const stale = useAgent((s) => s.stale);
  const elements = useBoard((s) => s.elements);
  const t = useT();
  const allActions = plan.actions ?? [];
  const counts = {
    all: allActions.length,
    new: allActions.filter((a) => matchesFilter(a, 'new')).length,
    moves: allActions.filter((a) => matchesFilter(a, 'moves')).length,
    deletes: allActions.filter((a) => matchesFilter(a, 'deletes')).length,
  };
  // Deletes first when there are any: the warn banner already knows to raise
  // the alarm and then handed over an unfiltered list, which is the same as not
  // raising it.
  const [filter, setFilter] = useState<RowFilter>(counts.deletes > 0 ? 'deletes' : 'all');
  // The collapsed set opens once and stays open — a person who has decided to
  // read the additive rows should not have to decide again per scroll.
  const [expanded, setExpanded] = useState(false);
  if (!effective) return null;
  const actions = allActions;
  const kept = new Set(effective.actions.map((a) => a.seq));

  // Narrowed to one nested board, because its badge was clicked. Thirty rows
  // where twenty-six are going somewhere else is not a review of that board —
  // it is the same wall the badge exists to cut through. Only the ROWS narrow:
  // the redirect menu and the nesting depth are still read off the whole plan,
  // or a filtered review would offer fewer destinations than an unfiltered one.
  const pinned = tileFocus?.pinned ? tileFocus : null;
  const scoped = pinned
    ? actions.filter((a) => pinned.seqs.includes(a.seq))
    : actions;
  const rows = scoped.filter((a) => matchesFilter(a, filter));

  // LP3's proportionality: the surprising rows are shown, the additive ones are
  // one line. Only on a list long enough for the length itself to be the
  // problem — collapsing three rows behind a "+3" is theatre.
  const banded = rows.map((a) => [a, bandOf(a, elements, boardId)] as const);
  const additive = banded.filter(([, b]) => b === 'additive').map(([a]) => a);
  const collapsing = !expanded && rows.length > 8 && additive.length > 3;
  // Spot-check: two of the collapsed rows are promoted back into the reviewed
  // set. Cheap sampling is what keeps a reviewer calibrated — a person who
  // never sees an additive row has no way to notice the day they stop being
  // additive. Chosen by a stable stride rather than at random so the list does
  // not reshuffle itself under the cursor between renders.
  const sampled = collapsing
    ? new Set([additive[Math.floor(additive.length / 3)]?.seq, additive[additive.length - 1]?.seq])
    : new Set<number>();
  const collapsible = collapsing ? additive.filter((a) => !sampled.has(a.seq)) : [];
  const shown = collapsing
    ? rows.filter((a) => !collapsible.includes(a))
    : rows;

  // Everywhere a change could be redirected: the board itself, plus every
  // container already here or about to be made. Offering the wrong ones would
  // be worse than offering none — the server refuses them, and a menu that
  // silently does nothing is how a control loses trust.
  const targets: { id: string; label: string }[] = [{ id: boardId, label: '' }];
  for (const a of actions) {
    if (CONTAINER_KINDS.has(a.kind) && a.title) targets.push({ id: a.elementId, label: a.title });
  }
  for (const a of effective.actions) {
    if (a.destination && a.parentId && !targets.some((tg) => tg.id === a.parentId)) {
      targets.push({ id: a.parentId, label: a.destination });
    }
  }

  return (
    <ol className="ac-plan">
      {pinned && (
        <li className="ac-plan-filter">
          <span dir="auto">{pinned.label || t('app.untitled')}</span>
          <button className="ac-ghostbtn"
            onClick={() => useAgent.getState().setTileFocus(null)}>
            {t('agent.showAll')}
          </button>
        </li>
      )}
      {/* The counts were already in hand and the list was the least filterable
          one in the app — on the surface where a mistake is most expensive and
          the question is always the same: what does this delete, and what does
          it move that I already placed. */}
      <li className="ac-plan-chips">
        {(['all', 'new', 'moves', 'deletes'] as RowFilter[]).map((f) => (
          counts[f] > 0 || f === 'all' ? (
            <button
              key={f}
              className={`ac-plan-chip${filter === f ? ' on' : ''}${f === 'deletes' ? ' danger' : ''}`}
              aria-pressed={filter === f}
              onClick={() => setFilter(f)}
            >
              {t(FILTER_KEYS[f])} <b>{counts[f]}</b>
            </button>
          ) : null
        ))}
      </li>
      {shown.map((a) => (
        <PlanRow
          key={a.seq}
          action={effective.actions.find((e) => e.seq === a.seq) ?? a}
          dropped={!kept.has(a.seq)}
          cascaded={effective.cascadedFrom.get(a.seq) ?? 0}
          depth={depthOf(a, actions)}
          targets={targets}
          boardId={boardId}
          stale={!!stale?.ids.includes(a.elementId)}
        />
      ))}
      {collapsing && (
        <li className="ac-plan-more">
          <button className="ac-ghostbtn" onClick={() => setExpanded(true)}>
            +{collapsible.length} {t('agent.risk.additive')}
          </button>
          {/* The honest half of collapsing: say what was not read. */}
          <em className="ac-audit-note">{t('agent.risk.disclosure')}</em>
        </li>
      )}
      {rows.length === 0 && (
        <li className="ac-plan-more"><em className="ac-audit-note">{t('agent.risk.none')}</em></li>
      )}
    </ol>
  );
}

const FILTER_KEYS: Record<RowFilter, TKey> = {
  all: 'agent.filter.all',
  new: 'agent.filter.new',
  moves: 'agent.filter.moves',
  deletes: 'agent.filter.deletes',
};

/**
 * What changed under the plan while it was being reviewed, in the board's own
 * vocabulary rather than as element ids.
 *
 * The server has always broadcast the stale id list on the run's event stream
 * and no frontend component has ever read a run event — so the whole recovery
 * experience was one toast reading "The board changed while you were
 * reviewing", which names nothing and offers nothing. On a shared board this is
 * the NORMAL outcome of reviewing for more than a few seconds.
 */
function StaleNotice({ onRevise }: { onRevise: () => void }) {
  const stale = useAgent((s) => s.stale);
  const busy = useAgent((s) => s.busy);
  const elements = useBoard((s) => s.elements);
  const t = useT();
  if (!stale) return null;

  const named = stale.ids
    .map((id) => elements[id])
    .filter(Boolean)
    .map((el) => (el!.content?.title as string) || (el!.content?.textPreview as string) || '')
    .filter(Boolean)
    .slice(0, 3);

  return (
    <div className="ac-stale" role="alert">
      <div className="ac-stale-text">
        {stale.membership || stale.ids.length === 0
          ? t('agent.stale.membership')
          : (
            <>
              <strong>{stale.ids.length}</strong> {t('agent.stale.head')}
              {named.length > 0 && <em> — {named.join(', ')}</em>}
            </>
          )}
      </div>
      <div className="ac-stale-actions">
        <button className="ac-ghostbtn" disabled={busy}
          title={t('agent.stale.recheckHint')}
          onClick={() => void useAgent.getState().recheck()}>
          {t('agent.stale.recheck')}
        </button>
        {/* Offered only when the drift names specific elements. A membership
            change — the board gained or lost items — invalidates the plan as a
            whole, and there is no subset of it that was computed against the
            board as it now is. */}
        {!stale.membership && stale.ids.length > 0 && (
          <button className="ac-ghostbtn" disabled={busy}
            title={t('agent.stale.applyRestHint')}
            onClick={() => void useAgent.getState().applyWithoutStale()}>
            {t('agent.stale.applyRest')}
          </button>
        )}
        <button className="ac-apply" disabled={busy}
          title={t('agent.stale.reviseHint')}
          onClick={onRevise}>
          {t('agent.revise')}
        </button>
      </div>
    </div>
  );
}

/** Kinds that produce something able to hold other elements. Mirrors
 *  ActionKind.Container() on the server. */
const CONTAINER_KINDS = new Set(['create_board', 'create_column', 'create_todo']);

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

function PlanRow({ action, dropped, cascaded, depth, targets, boardId, stale }: {
  action: AgentAction;
  dropped: boolean;
  /**
   * How many further rows this one's removal took with it.
   *
   * Dropping a container drops everything the plan put inside it, and the list
   * said nothing: nine changes disappeared on one click and the only evidence
   * was that the list got shorter. A reviewer's drop was a bigger act than they
   * knew, and putting it back was a guess about what had gone.
   */
  cascaded: number;
  depth: number;
  targets: { id: string; label: string }[];
  boardId: string;
  /** A collaborator changed this element after the plan was made. */
  stale: boolean;
}) {
  // Hovering the row lights its ghost, hovering the ghost lights the row — and
  // a nested board's badge stands for every row filed inside it, so hovering
  // that lights all of them at once. Without the second half the badge would be
  // the one preview surface with no link back to the list it summarises.
  const hovered = useAgent(
    (s) => s.hoverSeq === action.seq || !!s.tileFocus?.seqs.includes(action.seq),
  );
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(action.title ?? action.text ?? '');
  const editable = action.title !== undefined || action.text !== undefined;

  /**
   * How much prose this row destroys.
   *
   * `set_note_text` and `write_document` replace a body wholesale, and the
   * review showed only the NEW text — the preview says what will exist, never
   * what will stop existing. So "tighten the second act of the treatment" could
   * read six pages, decide it had seen the whole thing, and propose a
   * replacement that deletes 97% of it behind a one-line row nobody could read
   * as destructive. The count is the smallest honest signal: the reviewer at
   * least sees the size of what is going.
   */
  const replacing = useBoard((s) => {
    if (action.kind !== 'set_note_text' && action.kind !== 'write_document') return 0;
    const existing = s.elements[action.elementId];
    const before = (existing?.content?.textPreview as string | undefined)?.length ?? 0;
    const after = (action.text ?? '').length;
    // Only when the replacement is materially shorter than what is there: an
    // edit that grows a note is not a deletion and does not need warning about.
    return before > 200 && after < before * 0.8 ? before : 0;
  });

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
      className={`ap-row${dropped ? ' dropped' : ''}${isDestructive(action.kind) ? ' danger' : ''}${hovered ? ' hot' : ''}${stale ? ' stale' : ''}`}
      style={{ paddingInlineStart: 8 + depth * 14 }}
      // AX31. Nesting was inline padding and nothing else — a fact conveyed
      // entirely in pixels. `aria-level` is the same number the indent already
      // computes, said in a way a screen reader can report.
      //
      // "Dropped" is NOT `aria-disabled` here: a `listitem` does not support
      // it, so it would be an attribute that lints clean in the source and is
      // exposed by nothing — the same class of lie as a write landing on a key
      // nothing reads. The state is carried by the sr-only word below and by
      // the row's own edit control, which is genuinely `disabled`.
      aria-level={depth + 1}
      data-dropped={dropped || undefined}
      // Hovering a row lights its ghost on the canvas: a line of text and a
      // rectangle in space should read as the same object. Focus is the same
      // relationship for anyone not using a mouse — without it, tabbing to a
      // plan row lit nothing on the canvas at all, and the product's own stated
      // design principle was implemented for exactly one input device.
      onMouseEnter={() => useAgent.getState().setHover(action.seq)}
      onMouseLeave={() => useAgent.getState().setHover(null)}
      onFocus={() => useAgent.getState().setHover(action.seq)}
      onBlur={() => useAgent.getState().setHover(null)}
      // Reaching the drop button should not be the only way to drop a row: it
      // is an 18px target that renders at opacity 0 until hovered.
      onKeyDown={(e) => {
        if (editing || (e.target as HTMLElement).tagName === 'INPUT') return;
        if (e.key === 'Delete' && !dropped) {
          e.preventDefault();
          useAgent.getState().adjust({ kind: 'drop', seq: action.seq });
        } else if ((e.key === 'u' || e.key === 'U') && dropped) {
          e.preventDefault();
          useAgent.getState().undoAdjust(action.seq);
        }
      }}
    >
      {stale && <span className="sr-only">{t('agent.stale.row')}. </span>}
      {/* Two facts that were carried by a CSS class alone. `.dropped` is a
          strikethrough and `.danger` is a red tint — neither is a word, and the
          second one is the difference between "adds a card" and "sends this to
          the trash". */}
      {dropped && <span className="sr-only">{t('agent.droppedRow')}. </span>}
      {isDestructive(action.kind) && <span className="sr-only">{t('agent.destructiveRow')}. </span>}
      <span className={`ap-kind k-${action.kind}`}>{kindLabel(action.kind, t)}</span>
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
          title={action.because || (editable ? t('agent.clickToEdit') : undefined)}
          onClick={() => { setDraft(action.title ?? action.text ?? ''); setEditing(true); }}>
          {action.title || action.text || action.summary}
          {/* Where it lands. Without this the canonical request — file these
              into columns — reviewed as a list of card names with no
              destination anywhere, which is the one fact needed to say yes. */}
          {/* AX28's audio half. The glyph is hidden and mirrored for sight; in
              speech "→" is announced as "right arrow", "rightwards arrow", or
              nothing at all depending on punctuation verbosity — so the ONE
              fact a reviewer needs, where this lands, was carried entirely by a
              character that does not reliably survive being spoken. The word
              goes in beside it. */}
          {action.destination && (
            <em className="ap-row-dest">
              <span className="dir-arrow" aria-hidden="true">→</span>
              <span className="sr-only">{t('agent.inside')} </span>
              {' '}{action.destination}
            </em>
          )}
          {replacing > 0 && (
            <em className="ap-row-loss">
              {t('agent.replaces')} {replacing} {t('agent.characters')}
            </em>
          )}
        </button>
      )}
      {/* Right card, wrong column used to mean deleting the change and doing it
          by hand. It can now simply go somewhere else. */}
      {!dropped && action.destination !== undefined && targets.length > 1 && (
        <select
          className="ap-row-dest-pick"
          // A `title` is not an accessible name (AX14's argument, and axe's):
          // it is a tooltip, exposed inconsistently and never at all to touch
          // AT. This select is the reparent control — where a change LANDS —
          // and it announced as "combo box" with no indication of which row it
          // belonged to. Named after its own row, because forty of these in one
          // plan named identically is the same failure at a different scale.
          title={t('agent.sendElsewhere')}
          aria-label={`${t('agent.sendElsewhere')} — ${action.title || action.text || action.summary}`}
          value={action.parentId ?? boardId}
          onChange={(e) => useAgent.getState().adjust({
            kind: 'reparent', seq: action.seq, value: e.target.value,
          })}
        >
          {targets.map((tg) => (
            <option key={tg.id} value={tg.id}>
              {tg.label || t('agent.ontoBoard')}
            </option>
          ))}
        </select>
      )}
      {cascaded > 0 && (
        <span className="ap-row-cascade">{t('agent.alsoRemoved').replace('{n}', String(cascaded))}</span>
      )}
      {dropped ? (
        <button className="ap-row-x" title={t('agent.putBack')} aria-label={t('agent.putBack')}
          onClick={() => useAgent.getState().undoAdjust(action.seq)}>
          <span aria-hidden="true">↩</span>
        </button>
      ) : (
        <button className="ap-row-x" title={t('agent.leaveOut')} aria-label={t('agent.leaveOut')}
          onClick={() => useAgent.getState().adjust({ kind: 'drop', seq: action.seq })}>
          <CloseIcon size={10} />
        </button>
      )}
    </li>
  );
}
