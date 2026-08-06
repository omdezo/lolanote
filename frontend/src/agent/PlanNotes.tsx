// What a plan does NOT do.
//
// Both halves of this already existed on the server and neither was ever
// rendered. `notes` is the harness speaking — budgets spent, step limits hit,
// references dropped. `unmet` is the agent speaking: parts of the request it
// did not carry out, with its reason.
//
// Showing neither is what makes a competent run read as a bad agent. A plan
// that quietly does four of the five things asked is indistinguishable from one
// that failed for no reason, and the user has no way to tell which — so they
// conclude the agent is unreliable rather than that it was constrained.
import { useT } from '../i18n';
import type { AgentPlan } from '../api/types';

export function PlanNotes({ plan }: { plan: AgentPlan }) {
  const t = useT();
  const unmet = plan.unmet ?? [];
  const notes = plan.notes ?? [];
  if (unmet.length === 0 && notes.length === 0) return null;

  return (
    <div className="ac-unmet">
      <div className="ac-unmet-head">{t('agent.notDone')}</div>
      <ul>
        {unmet.map((u) => (
          <li key={u.request}>
            <span dir="auto">{u.request}</span>
            {u.why && <em dir="auto"> — {u.why}</em>}
          </li>
        ))}
        {/* The harness's own notes read differently: they are about the run
            rather than about the request, so they are not dressed up as the
            agent's decisions. */}
        {notes.map((n) => (
          <li key={n} className="ac-unmet-sys" dir="auto">{n}</li>
        ))}
      </ul>
    </div>
  );
}
