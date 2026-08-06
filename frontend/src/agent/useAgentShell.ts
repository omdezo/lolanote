// Global agent keyboard map and the canvas auto-frame.
//
// Framing is the difference between a preview and a rumour: the agent places
// new things below whatever is already on the board, so a user scrolled
// elsewhere would watch nothing happen. When a plan arrives the canvas tweens
// to show it, keeping clear of the bar at the bottom.
import { useEffect, useRef } from 'react';
import { tweenValues } from '../lib/motion';
import { ownsEscape, useView } from '../store/viewStore';
import { useBoard } from '../store/boardStore';
import { useAgent } from './agentStore';

/**
 * Fallback height reserved at the bottom of the canvas so framing never hides
 * behind the bar. Only a fallback now: the real figure is measured from the
 * shell, because 150 was taken off a desktop pill and the phone review card is
 * 400–500px, so framing centred the ghosts underneath the card reviewing them.
 */
export const BAR_RESERVE = 150;

/** What the bar is actually occupying right now, measured rather than assumed. */
function barReserve(): number {
  const shell = document.querySelector('.agent-shell') as HTMLElement | null;
  const measured = shell?.offsetHeight ?? 0;
  return measured > 0 ? measured + 24 : BAR_RESERVE;
}

/** The overlay id the agent shell owns on the Escape stack. */
const AGENT_OVERLAY = 'agent';

export function useAgentShell() {
  const state = useAgent((s) => s.run?.state);
  const plan = useAgent((s) => s.run?.plan);
  // Which run we have already framed for. A plan object is replaced on every
  // resync — and a resync happens on every state change and every event burst —
  // so keying the effect on the plan meant the canvas snapped back on top of
  // whatever the person had just panned to look at.
  const framed = useRef('');

  // The composer and the review card are overlays like any other, so Escape
  // resolves against the same stack every other surface uses.
  const open = useAgent((s) => s.open);
  const hasRun = useAgent((s) => !!s.run);
  useEffect(() => {
    const v = useView.getState();
    if (open || hasRun) v.pushOverlay(AGENT_OVERLAY);
    else v.popOverlay(AGENT_OVERLAY);
  }, [open, hasRun]);

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
      if (e.key === 'Escape' && !s.run && s.open && ownsEscape(AGENT_OVERLAY)) { s.setOpen(false); return; }
      if (inField) return;
      // Escape used to call discard() here. Two things were wrong with it and
      // both cost people work. The listener was on `window` alongside App's
      // panel-close listener, neither stopping propagation — so closing Settings
      // with Escape while a proposal was pending ALSO threw the proposal away,
      // terminally, with no confirmation. And even alone it was wrong:
      // discarding a plan that cost money is a decision, and decisions get the
      // Discard button, not the key a screen-reader user presses to get out of
      // anything. Escape now collapses the surface; the plan survives.
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && s.run?.state === 'PROPOSED') void s.apply();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // Framing on PROPOSED only meant the mode the composer advertises as "Do it
  // (Enter)" never framed at all: an auto-apply run never enters PROPOSED, so
  // the board changed off-screen — the agent places new work below what is
  // already there by design — and a person scrolled elsewhere watched nothing
  // happen and found strange cards later.
  useEffect(() => {
    if (state !== 'PROPOSED' && state !== 'COMPLETED') return;
    if (!plan) return;
    const runId = useAgent.getState().run?.id ?? '';
    if (framed.current === runId) return;
    framed.current = runId;
    frameProposal();
  }, [state, plan]);
}

/**
 * Tween the canvas to show a plan, clear of the bar.
 *
 * Exported so a person can ask for it. Framing on arrival is right — the agent
 * places new things below whatever is already there, so an unframed preview is
 * a rumour. Framing again on every resync is not: it landed on top of whatever
 * they had just panned over to check.
 */
export function frameProposal(): void {
  const plan = useAgent.getState().run?.plan;
  if (!plan) return;
  // A plan that only answers a question has no actions at all, and Go sends
  // an empty slice as null.
  //
  // A coordinate only means something on the canvas it was computed for, so a
  // plan that only fills nested boards must not drag the view to a position
  // belonging to somebody else's coordinate space — the same rule GhostLayer
  // applies before it draws anything.
  const boardId = useBoard.getState().boardId;
  const boxes = (plan.actions ?? [])
    .filter((a) => a.position && (!a.parentId || a.parentId === boardId))
    .map((a) => a.position) as Array<{ x: number; y: number; width: number }>;
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
  const h = Math.max(240, viewport.clientHeight - barReserve());
  const scale = Math.min(1.1, Math.max(0.3,
    Math.min(w / (maxX - minX + pad * 2), h / (maxY - minY + pad * 2))));
  const target = {
    panX: -(minX - pad) * scale + (w - (maxX - minX + pad * 2) * scale) / 2,
    panY: -(minY - pad) * scale + (h - (maxY - minY + pad * 2) * scale) / 2,
    scale,
  };
  const v = useView.getState();
  const from = { panX: v.panX, panY: v.panY, scale: v.scale };
  // Not cancelled on unmount any more: this now runs once per run rather than
  // on every plan object, so there is no later tween racing this one — and a
  // person who asked for it should get it even if the panel closes underneath.
  // AX19. This is the worst of the nine: an unrequested, full-viewport,
  // 0.55-second pan AND zoom fired on EVERY plan arrival — the textbook WCAG
  // 2.3.3 vestibular trigger, on the one screen a person has to concentrate on.
  //
  // But it does not simply get switched off under reduced motion, because the
  // FRAMING is information: the plan is off-screen and this is what brings it
  // into view. Only the traversal is decoration. tweenValues jumps to the
  // target frame instead, so a person who asked for no motion still ends up
  // looking at the thing they are being asked to approve.
  tweenValues(from, target, {
    duration: 0.55, ease: 'power3.inOut',
    onUpdate: () => useView.getState().setView(from.panX, from.panY, from.scale),
  });
}
