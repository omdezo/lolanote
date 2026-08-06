// The one place a tween may start.
//
// AX19. Nine JS-driven GSAP animations ran in this product and not one of them
// read a media query. The team plainly knew the query — `prefers-reduced-motion`
// appears twice in the CSS — so this was a scoping error, not an unknown: the
// two worst offenders are the ones CSS can never reach, because they are
// viewport transforms driven from JS.
//
// `frameProposal()` pans AND zooms the entire canvas whenever a plan arrives:
// an unrequested, full-viewport, 0.55-second motion event, which is precisely
// the WCAG 2.3.3 vestibular trigger. A person with a vestibular disorder cannot
// use the agent's preview mode at all, and the setting they have already told
// their operating system about was never asked for.
//
// The crucial distinction, and the reason this is a wrapper rather than a
// guard at each call site: for the two camera tweens the FRAMING IS
// INFORMATION — the plan is off-screen and has to be brought into view — and
// only the traversal is decoration. So reduced motion JUMPS to the end state.
// It never skips it. A "reduced motion" that silently drops the framing would
// leave a person looking at an empty canvas being told a plan had arrived.
//
// The clause that keeps this true: this module is the ONLY entry point that may
// start a tween. `motion.test.ts` greps every source file for a bare `gsap.`
// call outside this file, because the tenth animation is what reintroduces the
// bug.
import gsap from 'gsap';
import { motionReduced } from '../store/settingsStore';

/** Anything gsap accepts as a target. */
type Target = gsap.TweenTarget;
type Vars = gsap.TweenVars;

/**
 * Animate DOM nodes from one state to another.
 *
 * Under reduced motion the elements are SET to the end state immediately:
 * opacity 1, no offset, no scale — the thing is simply there, which is what a
 * person who asked for no motion wants an appearing panel to do.
 */
export function tweenFromTo(target: Target, from: Vars, to: Vars): gsap.core.Tween | null {
  if (!target) return null;
  if (motionReduced()) {
    // Only the properties the tween would have ANIMATED are settled — duration,
    // ease, stagger and the callbacks are tween mechanics, not end state, and
    // handing them to gsap.set logs warnings and sets junk inline styles.
    gsap.set(target, endStateOf(from, to));
    to.onComplete?.();
    return null;
  }
  return gsap.fromTo(target, from, to);
}

/**
 * Animate a plain object's numeric fields, calling `onUpdate` as they move.
 *
 * This is the camera. `from` is mutated in place exactly as gsap would mutate
 * it, so the caller's `onUpdate` closure reads the same object either way and
 * the reduced-motion path is not a second code path with its own bugs.
 *
 * Under reduced motion the end values are written and `onUpdate` is called
 * ONCE — the viewport lands on the target frame with no traversal.
 */
export function tweenValues<T extends Record<string, number>>(
  from: T,
  to: Partial<T>,
  opts: { duration: number; ease?: string; onUpdate: () => void; onComplete?: () => void },
): gsap.core.Tween | null {
  if (motionReduced()) {
    Object.assign(from, to);
    opts.onUpdate();
    opts.onComplete?.();
    return null;
  }
  return gsap.to(from, {
    ...to,
    duration: opts.duration,
    ease: opts.ease,
    onUpdate: opts.onUpdate,
    onComplete: opts.onComplete,
  });
}

/** The tween properties that describe WHERE it ends, with the mechanics
 *  (duration, ease, stagger, callbacks) dropped. Every key the `from` vars
 *  touched is included even when `to` is silent about it, because gsap's
 *  fromTo restores unmentioned properties to their inline default. */
const MECHANICS = new Set([
  'duration', 'ease', 'stagger', 'delay', 'repeat', 'yoyo', 'overwrite',
  'onUpdate', 'onComplete', 'onStart', 'onInterrupt', 'paused', 'immediateRender',
]);

function endStateOf(from: Vars, to: Vars): Vars {
  const out: Vars = {};
  // Anything `from` displaced but `to` never mentions still has to come home;
  // the fade-in's `y: 14` with no `y` in `to` is the common shape.
  for (const k of Object.keys(from)) {
    if (MECHANICS.has(k)) continue;
    out[k] = k === 'opacity' ? 1 : k === 'scale' ? 1 : 0;
  }
  for (const k of Object.keys(to)) {
    if (MECHANICS.has(k)) continue;
    out[k] = to[k];
  }
  return out;
}

/**
 * The inline-axis sign for a horizontal offset.
 *
 * AX28. CV9 mirrored the chrome with `dir`, and three panels kept sliding in
 * from the wrong side, because a GSAP tween takes a number and a number has no
 * direction. `x: -340` means "from the left" in both languages. Multiply by
 * this and it means "from outside the leading edge" in either.
 */
export { isRTL } from '../store/settingsStore';
