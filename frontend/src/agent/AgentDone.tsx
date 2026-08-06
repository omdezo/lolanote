// Done — what happened, what it cost, and how to take it back.
import { useMemo, useState } from 'react';
import { useT } from '../i18n';
import { api } from '../api/client';
import { useBoard } from '../store/boardStore';
import { navigateToBoard } from '../store/navigation';
import type { AgentRun } from '../api/types';
import { PlanNotes } from './PlanNotes';
import { rollUpDestinations } from './GhostLayer';
import { isDestructive, kindLabel, outcomeText, useAgent } from './agentStore';

export function Done({ run }: { run: AgentRun }) {
  const busy = useAgent((s) => s.busy);
  const t = useT();
  const [details, setDetails] = useState(false);
  const { tone, title, detail } = outcomeText(run, t);
  const revertible = run.state === 'COMPLETED' || run.state === 'PARTIAL';
  // Anything that ended without doing what was asked can be run again from the
  // same request. Retyping a paragraph to recover from a provider timeout is
  // not a recovery path.
  const retryable = run.state === 'FAILED' || run.state === 'BUDGET_EXHAUSTED'
    || run.state === 'CANCELLED' || run.state === 'DENIED';
  // The server refuses a third pass at a failure it has already produced twice,
  // and says why in its own words. Once that has come back, Try again stops
  // being the obvious button: pressing it costs another pass to reach the same
  // place. Ask again — with a different request — becomes the primary move, and
  // Try again stays available as a ghost for somebody who means it anyway.
  const refused = useAgent((s) => s.retryRefused);
  // A run that landed changes and named what it did not finish can hand that
  // list to a new run. Without this the only way to say "keep going" was to
  // retype the request from memory, and the retyped one arrived knowing nothing
  // about what came before — which is how a truncated build got a second copy
  // built beside it.
  //
  // COMPLETED only, deliberately. A PARTIAL run's unmet entry is its whole
  // request restated ("those figures are not on this board"), so continuing it
  // would ask the same question again and get the same answer; a DISCARDED or
  // REVERTED one has nothing standing to build on.
  const continuable = run.state === 'COMPLETED' && (run.plan?.unmet?.length ?? 0) > 0;
  const failed = run.verdict?.criteria.filter((c) => !c.passed) ?? [];
  const actions = run.plan?.actions ?? [];

  return (
    <div className="agent-card">
      {/* When verification fails the board has already been restored — say so
          plainly rather than leaving the user to wonder what state they are in. */}
      {failed.length > 0 && (
        <ul className="ac-checks">{failed.slice(0, 2).map((c) => <li key={c.name}>{c.detail || c.name}</li>)}</ul>
      )}

      {/* Rendered, not toasted. The refusal's entire content is "pressing that
          button again will cost you another pass and reach the same place", and
          a message that vanishes after four seconds leaves the button behind. */}
      {refused && <div className="ac-checks" role="status"><p>{refused}</p></div>}

      {/* The agent's own account of what happened.
          The outcome line is generated from the run's STATE — "Done · 4 changes
          applied", "Nothing to do · No changes were needed" — and said nothing
          about the actual work. A run that declined for a good reason had that
          reason in plan.summary and nowhere the person would ever look. */}
      {run.plan?.summary && <div className="ac-summary">{run.plan.summary}</div>}

      {/* Whatever the run could not do stays visible after it finishes. It is
          most useful here: this is where a person decides whether to ask again. */}
      {run.plan && <PlanNotes plan={run.plan} />}

      {/* What it learned from being corrected — offered, never written. A rule
          the agent set for itself would be invisible, would compound silently,
          and could not be argued with. */}
      {run.plan?.proposedRule && <ProposedRule runId={run.id} text={run.plan.proposedRule} />}

      {/* Where the work actually went. The agent's centre of gravity moved into
          nested boards and both the review and the outcome surfaces stayed on
          the root canvas, so a run that wrote thirty elements into four
          sub-boards finished by showing four unchanged tiles — and verifying it
          meant opening four boards from memory. */}
      {revertible && <Destinations run={run} />}

      {details && revertible && <AppliedList run={run} />}

      {/* AX6: focus lands here when the run ends — the outcome, one Tab from
          Undo. `tabIndex={-1}` makes it a focus TARGET without adding a tab
          stop, so nobody using Tab meets a row that does nothing. */}
      <div className="ac-decide" tabIndex={-1} role="group" aria-label={`${title}. ${detail}`}>
        <span className={`ac-outcome-dot ${tone}`} aria-hidden="true" />
        <span className="ac-decide-text"><strong>{title}</strong> · {detail}</span>
        {run.usage?.costUsd > 0 && <span className="ac-badge">${run.usage.costUsd.toFixed(3)}</span>}
        {/* Undoing the whole run used to be the only option, so a run that got
            four things right and one wrong had to be thrown away entirely. */}
        {revertible && actions.length > 1 && (
          <button className="ac-ghostbtn" aria-expanded={details} title={t('agent.reviewHint')}
            onClick={() => setDetails(!details)}>
            {t(details ? 'agent.hide' : 'agent.review')} <span aria-hidden="true">{details ? '▾' : '▴'}</span>
          </button>
        )}
        {revertible && (
          <button className="ac-ghostbtn" disabled={busy} onClick={() => void useAgent.getState().revert()}>
            {t('agent.undoAll')}
          </button>
        )}
        {continuable && (
          <button
            className="ac-ghostbtn"
            disabled={busy}
            title={t('agent.continueHint')}
            onClick={() => void useAgent.getState().continueRun()}
          >
            {t('agent.continue')}
          </button>
        )}
        {retryable && refused ? (
          <>
            <button className="ac-ghostbtn" disabled={busy} onClick={() => void useAgent.getState().retry()}>
              {busy ? t('agent.thinking') : t('agent.tryAgain')}
            </button>
            <button className="ac-apply" onClick={() => { useAgent.getState().dismiss(); useAgent.getState().setOpen(true); }}>
              {t('agent.askAgain')}
            </button>
          </>
        ) : retryable ? (
          <button className="ac-apply" disabled={busy} onClick={() => void useAgent.getState().retry()}>
            {busy ? t('agent.thinking') : t('agent.tryAgain')}
          </button>
        ) : (
          <button className="ac-apply" onClick={() => { useAgent.getState().dismiss(); useAgent.getState().setOpen(true); }}>
            {t('agent.askAgain')}
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * "7 in Pre-Production · 5 in Casting" — each one a way of getting there.
 *
 * The same roll-up the review preview badges with, so the promise made before
 * Apply and the route offered after it are one claim rather than two. Selecting
 * on arrival is the point: without it the person lands on a board that looks
 * exactly as it did, with no indication of which of forty cards is the new one.
 */
function Destinations({ run }: { run: AgentRun }) {
  const elements = useBoard((s) => s.elements);
  const boardId = useBoard((s) => s.boardId);
  const t = useT();
  const actions = run.plan?.actions ?? [];
  const tiles = useMemo(
    () => rollUpDestinations(actions, elements, boardId),
    [actions, elements, boardId],
  );
  if (tiles.length === 0) return null;

  const go = async (tile: { id: string; elementIds: string[] }) => {
    await navigateToBoard(tile.id);
    const board = useBoard.getState();
    // Only what is genuinely there: the run may have filed into a column on
    // that board whose children arrive with it, and selecting ids the store
    // does not have would clear the person's selection for nothing.
    const here = tile.elementIds.filter((id) => board.elements[id] && !board.elements[id].deletedAt);
    if (here.length > 0) board.select(here);
  };

  return (
    <div className="ac-destinations">
      {tiles.map((tile) => (
        <button key={tile.id} className="ac-destination" onClick={() => void go(tile)}>
          <strong>{tile.seqs.length}</strong>
          <span dir="auto">{t('agent.inside')} {tile.label || t('app.untitled')}</span>
          <span className="dir-arrow" aria-hidden="true">→</span>
        </button>
      ))}
    </div>
  );
}

/** One sentence the agent suggests remembering about this board. */
function ProposedRule({ runId, text }: { runId: string; text: string }) {
  const t = useT();
  const boardId = useBoard((s) => s.boardId);
  const board = useBoard((s) => (s.boardId ? s.elements[s.boardId] : undefined));
  const [done, setDone] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  if (dismissed) return null;

  const existing = (board?.content?.agentInstructions as string | undefined) ?? '';

  // The verdict goes to the server on BOTH buttons, and it is the first thing
  // either of them does.
  //
  // This card is a two-button experiment the product runs on every corrected
  // run, and until now it scored neither answer: Save appended a sentence to
  // the board's rules through a transaction with no run link, and "Not now" set
  // a local flag and unmounted. So "do people want the rules the agent
  // proposes" — the direct measure of whether the memory work is worth building
  // — was unanswerable, and an accepted rule joined the rules string
  // indistinguishable from something the owner had typed themselves.
  //
  // Fire-and-forget: failing to record the answer must never stop somebody
  // saving a rule they asked for.
  const answer = (accepted: boolean) => {
    void api.agentAnswerRule(runId, accepted).catch(() => { /* the write below is the point */ });
  };

  const save = () => {
    answer(true);
    if (!boardId) return;
    const next = existing ? `${existing}
${text}` : text;
    // Through the ordinary transaction path, so it is undoable and syncs like
    // any other edit rather than being agent-only state.
    void useBoard.getState().commitTransaction([{
      elementId: boardId,
      action: 'update',
      changes: { content: { agentInstructions: next } },
      undoChanges: { content: { agentInstructions: existing } },
    }]);
    setDone(true);
  };

  return (
    <div className="ac-rule">
      <div className="ac-rule-head">{t('agent.remember')}</div>
      <p dir="auto">{text}</p>
      {done ? (
        <em className="ac-audit-note">{t('agent.ruleSaved')}</em>
      ) : (
        <div className="ac-rule-actions">
          <button className="ac-ghostbtn" onClick={() => { answer(false); setDismissed(true); }}>
            {t('agent.notNow')}
          </button>
          <button className="ac-apply" onClick={save}>{t('agent.saveRule')}</button>
        </div>
      )}
    </div>
  );
}

/** What the run actually did, each row undoable on its own. */
function AppliedList({ run }: { run: AgentRun }) {
  const busy = useAgent((s) => s.busy);
  const t = useT();
  const reverted = new Set(run.revertedElementIds ?? []);

  return (
    <ol className="ac-plan">
      {(run.plan?.actions ?? []).map((a) => {
        const undone = reverted.has(a.elementId);
        return (
          <li
            key={a.seq}
            className={`ap-row${undone ? ' dropped' : ''}${isDestructive(a.kind) ? ' danger' : ''}`}
          >
            <span className={`ap-kind k-${a.kind}`}>{kindLabel(a.kind, t)}</span>
            <span className="ap-row-text" dir="auto">
              {a.title || a.text || a.summary}
              {/* Same as the review list (AX28): the arrow is decoration, the
                  relationship is a word. */}
              {a.destination && (
                <em className="ap-row-dest">
                  <span className="dir-arrow" aria-hidden="true">→</span>
                  <span className="sr-only">{t('agent.inside')} </span>
                  {' '}{a.destination}
                </em>
              )}
            </span>
            {undone ? (
              <em className="ac-audit-note">{t('agent.out.reverted')}</em>
            ) : (
              <button
                className="ap-row-x"
                title={t('agent.undoThis')}
                // Per-action revert is the highest-resolution correction signal
                // in the product and it announced as an unpronounceable symbol.
                aria-label={t('agent.undoThis')}
                disabled={busy}
                // Anything this run put inside it goes too — leaving a note in a
                // container that no longer exists is not an undo.
                onClick={() => void useAgent.getState().revert([a.elementId])}
              >
                <span aria-hidden="true">↩</span>
              </button>
            )}
          </li>
        );
      })}
    </ol>
  );
}
