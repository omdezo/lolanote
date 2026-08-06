// The agent lives in one place: a bar at the bottom centre of the canvas.
//
// Why here. It is the one spot a person's eye returns to between actions, so
// the agent is always visible without ever being in the way — no hunting a rail
// icon, no panel eating a third of the board. It also keeps the *canvas* as the
// review surface: the plan appears as ghosts in place, and this bar is only
// where you ask and decide.
//
// Everything grows upward from the bar so the anchor never moves.
//
// This file is the state machine and nothing else. Each state renders from its
// own file — a run is a sequence of distinct screens, and one 700-line module
// meant a change to the outcome card risked the composer.
import { useEffect, useLayoutEffect, useRef } from 'react';
import { tweenFromTo } from '../lib/motion';
import { useBoard } from '../store/boardStore';
import { useAgent, WORKING } from './agentStore';
import { Pill } from './AgentPill';
import { Observing } from './AgentObserving';
import { Ask } from './AgentAsk';
import { Working } from './AgentWorking';
import { Decide } from './AgentDecide';
import { Done } from './AgentDone';
import { AgentLive } from './AgentLive';
import { useT } from '../i18n';

export { BAR_RESERVE, useAgentShell } from './useAgentShell';

export function AgentBar() {
  const t = useT();
  const enabled = useAgent((s) => s.capabilities?.enabled ?? false);
  const readOnly = useBoard((s) => s.readOnly);
  const run = useAgent((s) => s.run);
  const open = useAgent((s) => s.open);
  // A colleague's run on this board. Only rendered when we have nothing of our
  // own to show — your own review always wins the bar.
  const theirs = useAgent((s) => (s.active && !s.active.mine ? s.active : null));
  const ref = useRef<HTMLDivElement>(null);
  /** Whatever had focus when the composer opened, so discarding gives it back. */
  const returnFocus = useRef<HTMLElement | null>(null);

  useLayoutEffect(() => {
    if (ref.current) {
      tweenFromTo(ref.current, { y: 12, opacity: 0 }, { y: 0, opacity: 1, duration: 0.26, ease: 'power3.out' });
    }
  }, [!!run, open]);

  const state = run?.state;

  /**
   * AX6 — put focus where the screen's decision is.
   *
   * The bar swaps Observing/Pill/Ask/Working/Decide/Done on `run.state` and,
   * apart from Ask focusing its own textarea on mount, NOTHING restored focus
   * on unmount. Compose an intent, press Enter, focus is gone; gone again when
   * the plan arrives; gone again on apply. For a screen-reader user focus on
   * `<body>` is the top of a document with no landmarks and no headings — there
   * is literally nothing to navigate back with.
   *
   * The accidental mitigation was instructive: the ONLY reason a keyboard user
   * could act on a proposal at all is that Apply and Discard are bound to
   * `window` in useAgentShell rather than to any focusable control. That
   * accident was load-bearing, and it should not be.
   *
   * On PROPOSED focus lands on Apply — the decision the screen exists for —
   * while the assertive region speaks the summary. On a terminal state it lands
   * on the outcome row, one Tab from Undo. On close it goes back where it came
   * from.
   */
  useEffect(() => {
    if (open && !run) {
      const active = document.activeElement as HTMLElement | null;
      if (active && !ref.current?.contains(active)) returnFocus.current = active;
    }
  }, [open, run]);

  // A LAYOUT effect, not a passive one and not a frame later: a child's DOM is
  // in place before the parent's layout effect runs, so the control exists to
  // be focused, and focus lands before the browser paints rather than one
  // frame after — which is the difference between a cursor that moves and a
  // cursor that jumps.
  useLayoutEffect(() => {
    const shell = ref.current;
    if (!shell) return;
    if (state === 'PROPOSED') {
      shell.querySelector<HTMLElement>('.ac-apply')?.focus();
    } else if (state && !WORKING.includes(state)) {
      shell.querySelector<HTMLElement>('.ac-decide')?.focus();
    }
  }, [state]);

  // Nothing open and nothing running: hand focus back to whatever opened the
  // composer, but only if it is still on the page and focus is adrift on body.
  useEffect(() => {
    if (open || run) return;
    const target = returnFocus.current;
    returnFocus.current = null;
    if (!target || !target.isConnected) return;
    if (document.activeElement && document.activeElement !== document.body) return;
    target.focus();
  }, [open, run]);

  if (!enabled || readOnly) return null;

  return (
    // AX18. The agent is a distinct region of the page and the skip link's
    // destination, so it is a landmark rather than a floating div: a keyboard
    // user reaching it should not have to cross the whole canvas first.
    <aside className="agent-bar" id="agent-shell-anchor" aria-label={t('a11y.agentRegion')}>
      {/* Mounted for the whole life of the bar, outside the state swap: a live
          region that appears at the same moment its text does is one screen
          readers do not announce. */}
      <AgentLive />
      <div ref={ref} className={`agent-shell${run || open ? ' expanded' : ''}`}>
        {!run && !open && theirs && <Observing />}
        {!run && !open && !theirs && <Pill />}
        {!run && open && <Ask />}
        {run && WORKING.includes(run.state) && <Working />}
        {run?.state === 'PROPOSED' && <Decide run={run} />}
        {run && !WORKING.includes(run.state) && run.state !== 'PROPOSED' && <Done run={run} />}
      </div>
    </aside>
  );
}
