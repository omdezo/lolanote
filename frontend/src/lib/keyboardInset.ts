// How much of the screen the software keyboard is covering, as a CSS variable.
//
// MO6. A `position: fixed` bottom element is positioned against the LAYOUT
// viewport, which iOS Safari does not shrink when the keyboard opens. The agent
// composer, the revise input, the plan-row rename field and every Tiptap card
// were therefore drawn underneath the keyboard the moment they took focus — and
// AgentAsk auto-focuses the composer 50ms after opening, so the keyboard rises
// over the field before the person has done anything. Android Chrome resizes
// the layout viewport by default, which is what makes this the worst kind of
// bug: a total iOS failure, a partial Android one, and a clean pass on every
// desktop emulator.
//
// One listener, one variable. Everything that anchors to the bottom edge reads
// `--kb-inset` and stops being a special case:
//
//     bottom: calc(12px + var(--kb-inset, 0px) + env(safe-area-inset-bottom));
//
// The same number feeds MO7's review-card height cap, because a card that grows
// upward from a bar the keyboard has pushed up must lose the height twice over.

/** The visual viewport, where the browser has one. */
type VV = { height: number; offsetTop: number;
  addEventListener(t: string, fn: () => void): void;
  removeEventListener(t: string, fn: () => void): void };

function visualViewport(): VV | null {
  return (window as unknown as { visualViewport?: VV }).visualViewport ?? null;
}

/**
 * How many pixels of the layout viewport the keyboard (or any other browser
 * widget) currently hides at the bottom. Zero when nothing is open, and zero
 * on every desktop browser.
 */
export function keyboardInset(): number {
  const vv = visualViewport();
  if (!vv) return 0;
  const hidden = window.innerHeight - vv.height - vv.offsetTop;
  // Sub-pixel noise and the elastic overscroll on iOS both produce small
  // non-zero values with no keyboard on screen. Below a finger's width it is
  // not a keyboard, and reacting to it would jitter the bar while scrolling.
  return hidden > 40 ? Math.round(hidden) : 0;
}

/** Publish the current inset. Exported so a test can drive it directly. */
export function applyKeyboardInset(): void {
  document.documentElement.style.setProperty('--kb-inset', `${keyboardInset()}px`);
}

/**
 * Start tracking. Idempotent, and a no-op where there is no visual viewport, so
 * the caller does not have to know which browser it is in.
 */
export function initKeyboardInset(): () => void {
  applyKeyboardInset();
  const vv = visualViewport();
  if (!vv) return () => { /* nothing was bound */ };
  vv.addEventListener('resize', applyKeyboardInset);
  // `scroll` as well as `resize`: iOS scrolls the visual viewport to reveal the
  // focused field rather than resizing again, and offsetTop is how that shows
  // up. Without it the bar corrects once and then drifts as the person types.
  vv.addEventListener('scroll', applyKeyboardInset);
  return () => {
    vv.removeEventListener('resize', applyKeyboardInset);
    vv.removeEventListener('scroll', applyKeyboardInset);
  };
}
