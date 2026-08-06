// JN8 — the run ledger.
//
// `agentStore.loadRecent` asked the server for SIX runs, hardcoded at the call
// site, and the composer surfaced Undo on the two most recent COMPLETED ones.
// So after seven runs on a board — one working week — run #1 was unreachable
// from every surface in the app. `revert.go` is the product's whole answer to
// "what if it does something wrong", and the answer quietly stopped existing
// after six requests. Worse, it did so invisibly: nothing said "older runs are
// no longer undoable here", the list simply got shorter from the far end, so a
// person's model of the guarantee stayed wrong until the moment it mattered —
// which is Thursday, about Monday's reorganisation.
//
// Nothing about the guarantee ever expired. The inverse ops are in the journal
// and age costs nothing to replay. Six was a number somebody typed.
//
// Two clauses beyond "show more rows":
//   - Revert is live on ANY completed or partial run, regardless of age.
//   - HONESTY: a run whose revert is no longer possible renders as such WITH
//     the reason, rather than offering a button that 500s. Emptying the trash
//     (JN20) is the reachable way to get there, and it is a first-class button.
import { useEffect, useState } from 'react';
import type { AgentRun } from '../api/types';
import { api } from '../api/client';
import { useBoard } from '../store/boardStore';
import { useT } from '../i18n';
import { useAgent, plural } from './agentStore';

/** How many rows a person gets before asking for more. */
const PAGE = 12;

export type RevertState =
  | { kind: 'available' }
  | { kind: 'done' }
  | { kind: 'blocked'; reason: string }
  | { kind: 'none' };

/**
 * Whether this run can still be taken back, and if not, why not.
 *
 * The reason is the point. A row that simply lacks a button teaches nothing;
 * a row that says "3 of its items were permanently deleted" tells a person
 * both what happened and — for next time — what emptying the trash costs.
 */
export function revertStateOf(run: AgentRun): RevertState {
  if (run.state === 'REVERTED') return { kind: 'done' };
  if (run.state !== 'COMPLETED' && run.state !== 'PARTIAL') return { kind: 'none' };
  if (run.revertBlockedReason) return { kind: 'blocked', reason: run.revertBlockedReason };
  // Every element this run made has already been undone one at a time, which
  // is the same end state as a whole-run revert and must not offer the button
  // again — `allReverted` short-circuits server-side and the click does nothing.
  const made = (run.plan?.actions ?? []).map((a) => a.elementId).filter(Boolean) as string[];
  const undone = new Set(run.revertedElementIds ?? []);
  if (made.length > 0 && made.every((id) => undone.has(id))) return { kind: 'done' };
  return { kind: 'available' };
}

/** "3 days ago" in the coarsest unit that is still true. */
export function ageLabel(iso: string, now = Date.now()): string {
  const ms = now - new Date(iso).getTime();
  if (!Number.isFinite(ms) || ms < 0) return '';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function RunLedger({ onClose }: { onClose: () => void }) {
  const t = useT();
  const boardId = useBoard((s) => s.boardId);
  const [runs, setRuns] = useState<AgentRun[] | null>(null);
  const [limit, setLimit] = useState(PAGE);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!boardId) return;
    let live = true;
    api.agentRuns(boardId, limit)
      .then((list) => { if (live) setRuns(list ?? []); })
      .catch(() => { if (live) setRuns([]); });
    return () => { live = false; };
  }, [boardId, limit]);

  const undo = async (run: AgentRun) => {
    setBusy(true);
    try {
      // Through the store, so the outcome card, the board refresh and the
      // expectation probe all see it — the ledger is a second SURFACE on one
      // mechanism, not a second mechanism.
      useAgent.setState({ run });
      await useAgent.getState().revert();
      if (boardId) setRuns((await api.agentRuns(boardId, limit)) ?? []);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="ac-ledger">
      <div className="ac-ledger-head">
        <strong>{t('agent.ledger')}</strong>
        <button className="ac-ghostbtn" onClick={onClose}>{t('common.close')}</button>
      </div>
      {runs === null && <div className="ac-audit-note">{t('agent.thinking')}</div>}
      {runs !== null && runs.length === 0 && <div className="ac-audit-note">{t('agent.ledgerEmpty')}</div>}
      <ol className="ac-ledger-list">
        {(runs ?? []).map((run) => {
          const rs = revertStateOf(run);
          const ops = run.plan?.actions?.length ?? 0;
          return (
            <li key={run.id} className="ac-ledger-row">
              <span className="ac-ledger-intent" dir="auto" title={run.task.intent}>{run.task.intent}</span>
              <span className="ac-ledger-meta">
                {ageLabel(run.createdAt)}
                {ops > 0 && <> · {ops} {plural(ops, t('agent.change'), t('agent.changes'))}</>}
                {run.usage?.costUsd > 0 && <> · ${run.usage.costUsd.toFixed(3)}</>}
                {' · '}{run.state.toLowerCase()}
              </span>
              {rs.kind === 'available' && (
                <button className="ac-ghostbtn" disabled={busy} onClick={() => void undo(run)}>
                  {t('agent.undo')}
                </button>
              )}
              {rs.kind === 'done' && <em className="ac-audit-note">{t('agent.out.reverted')}</em>}
              {/* The honesty clause. A reason, not an absence. */}
              {rs.kind === 'blocked' && <em className="ac-audit-note">{t('agent.revertGone')} — {rs.reason}</em>}
            </li>
          );
        })}
      </ol>
      {runs !== null && runs.length >= limit && (
        <button className="ac-ghostbtn" onClick={() => setLimit(limit + PAGE)}>{t('agent.ledgerMore')}</button>
      )}
    </div>
  );
}
