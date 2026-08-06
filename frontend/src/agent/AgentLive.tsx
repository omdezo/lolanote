// The run, spoken.
//
// AX5. A repo-wide grep for `aria-live`, `role="status"` and `role="alert"`
// returned zero. Every trust property the agent's whole architecture exists to
// establish — preview before commit, honest outcomes, unmet requests,
// per-action revert, cost visibility, drift honesty — was delivered purely as a
// visual mutation. A blind user was told nothing when the plan arrived, nothing
// while forty actions staged, nothing when the transaction committed, nothing
// when a revert landed. The agent's entire safety story was invisible to the
// users for whom the agent matters most.
//
// Two regions with different urgency, because they answer different questions:
//
//   polite   — "what is it doing now", throttled to about one utterance every
//              1.5s so a forty-action run is not forty interruptions.
//   assertive — exactly FOUR transitions: a plan is ready, it applied, it was
//              reverted, it failed. Each is a decision point or a completed
//              write; nothing else is allowed to interrupt.
//
// Both regions are mounted by AgentBar unconditionally and never unmount. That
// is the mechanism, not a detail: a live region that appears at the same moment
// its text does is a region screen readers do not announce, which is exactly
// how an aria-live fix ships and does nothing.
import { useEffect, useRef, useState } from 'react';
import { useT } from '../i18n';
import { useAgent } from './agentStore';
import { announceVerbosity } from '../store/settingsStore';
import { outcomeText, plural } from './agentStore';

/** How long to hold a polite utterance before allowing the next one. */
const POLITE_MS = 1500;

/** The states worth interrupting a person for. */
const ANNOUNCED = new Set(['PROPOSED', 'COMPLETED', 'REVERTED', 'FAILED', 'DENIED', 'PARTIAL', 'SECURITY_QUARANTINED']);

export function AgentLive() {
  const t = useT();
  const run = useAgent((s) => s.run);
  const events = useAgent((s) => s.events);
  const [polite, setPolite] = useState('');
  const [assertive, setAssertive] = useState('');
  const lastPolite = useRef(0);
  const politeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const announcedState = useRef('');

  const state = run?.state;
  const lastMessage = events.length ? events[events.length - 1].message : '';

  // AX29. A COLLEAGUE's run rewriting the board you are on was announced by a
  // `title` attribute on a non-interactive div — which assistive technology
  // does not reliably expose at all. MP15 and MP6 both make "the board is in
  // motion" a safety statement, and it was the one statement in the agent's
  // whole story delivered by the least reliable mechanism in HTML. Their
  // arrival and their transition to PROPOSED come through the polite region;
  // their request and their plan deliberately do not (that is their business).
  const theirs = useAgent((s) => (s.active && !s.active.mine ? s.active : null));
  const [observed, setObserved] = useState('');
  const announcedTheirs = useRef('');
  useEffect(() => {
    if (!theirs) { announcedTheirs.current = ''; setObserved(''); return; }
    const key = `${theirs.id ?? theirs.startedAt}:${theirs.state}`;
    if (announcedTheirs.current === key) return;
    announcedTheirs.current = key;
    setObserved(`${theirs.actor ?? ''} ${t(theirs.state === 'PROPOSED' ? 'agent.theirRunProposed' : 'agent.theirRunWorking')}`);
  }, [theirs, t]);

  // ---- polite: progress ---------------------------------------------------
  useEffect(() => {
    if (!lastMessage) return;
    // AX30. "How much should this speak" is a question with no correct default:
    // for one person forty staged actions ARE the review, and for another they
    // are forty interruptions. `outcome` silences progress entirely and leaves
    // the four assertive transitions, which is the whole point — the person who
    // wants less noise still gets told when their board changed.
    const verbosity = announceVerbosity();
    if (verbosity === 'outcome') return;
    const now = Date.now();
    // `verbose` drops the throttle: somebody who asked to hear every staged
    // action is asking for exactly the thing the throttle was collapsing.
    const wait = verbosity === 'verbose' ? 0 : Math.max(0, POLITE_MS - (now - lastPolite.current));
    if (politeTimer.current) clearTimeout(politeTimer.current);
    // Always through a timer, even at wait 0, so a burst of staged events
    // collapses to its LAST message rather than reading the first one and
    // dropping the rest — the person wants to know where the run is, not where
    // it was when the throttle window opened.
    politeTimer.current = setTimeout(() => {
      lastPolite.current = Date.now();
      setPolite(lastMessage);
    }, wait);
    return () => { if (politeTimer.current) clearTimeout(politeTimer.current); };
  }, [lastMessage]);

  // ---- assertive: the four transitions ------------------------------------
  useEffect(() => {
    if (!run || !state || !ANNOUNCED.has(state)) {
      announcedState.current = '';
      return;
    }
    const key = `${run.id}:${state}`;
    if (announcedState.current === key) return;
    announcedState.current = key;

    if (state === 'PROPOSED') {
      const plan = run.plan;
      const n = plan?.actions?.length ?? 0;
      const created = plan?.actions?.filter((a) => a.kind.startsWith('create_')).length ?? 0;
      // AX38: the announcement belongs to the PLAN CONTRACT, not to React.
      // `announce` is the field that carries it; `summary` is the one the
      // server writes today. Composing from counts is the last resort, and it
      // is deliberately the branch a reader can tell apart in a transcript.
      const authored = plan?.announce || plan?.summary;
      setAssertive(authored
        ? `${t('agent.a11y.proposed')} ${authored}`
        : `${t('agent.a11y.proposed')} ${n} ${plural(n, t('agent.change'), t('agent.changes'))}`
          + (created > 0 ? `, ${created} ${t('agent.new')}` : ''));
      return;
    }
    const outcome = outcomeText(run, t);
    setAssertive(`${outcome.title}. ${outcome.detail}`);
  }, [run, state, t]);

  return (
    <>
      <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">{observed || polite}</div>
      <div className="sr-only" role="alert" aria-live="assertive" aria-atomic="true">{assertive}</div>
    </>
  );
}
