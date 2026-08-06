// The keymap, as one table.
//
// AX20, WCAG 2.1.4. `z` was a single-character shortcut on a `window` listener
// that re-framed and re-zoomed the entire canvas, guarded only by "are you in a
// text field". It was not remappable, not disableable and not focus-scoped —
// none of the three escapes the criterion offers. Speech-input users emit stray
// characters constantly, and every stray `z` moved the whole board (which,
// before AX19, was also an unguarded vestibular event).
//
// Space was the second instance and the subtler one: it was captured as a
// global pan modifier with the same single guard, and Space is the standard
// activation key for a button — so `spaceDown` flipped true whenever anybody
// activated any focused control anywhere on the page.
//
// The keymap was also spread across three files (App.tsx, BoardCanvas.tsx,
// useAgentShell.ts) with no shared vocabulary, which is why nobody noticed. It
// is one table now, so it can be shown to a person in Settings — a shortcut a
// user cannot enumerate is a shortcut they cannot avoid.
import { useSettings } from '../store/settingsStore';

export type ShortcutScope = 'global' | 'canvas';

export interface Shortcut {
  /** Stable id; also the i18n key suffix (`shortcut.<id>`). */
  id: string;
  /** Human-readable combo, already in the order people say it. */
  combo: string;
  /** `canvas` shortcuts only fire while focus is inside the board viewport. */
  scope: ShortcutScope;
  /**
   * True when the shortcut is a bare printable character with no modifier —
   * the exact class WCAG 2.1.4 governs, and the class the Settings toggle
   * switches off wholesale.
   */
  singleKey: boolean;
}

/** Every keyboard shortcut the product listens for, in the order a person
 *  would want to read them. */
export const SHORTCUTS: Shortcut[] = [
  { id: 'search', combo: 'Ctrl+F', scope: 'global', singleKey: false },
  { id: 'agent', combo: 'Ctrl+K', scope: 'global', singleKey: false },
  { id: 'capture', combo: 'Ctrl+Enter', scope: 'global', singleKey: false },
  { id: 'undo', combo: 'Ctrl+Z', scope: 'global', singleKey: false },
  { id: 'redo', combo: 'Ctrl+Shift+Z', scope: 'global', singleKey: false },
  { id: 'duplicate', combo: 'Ctrl+D', scope: 'global', singleKey: false },
  { id: 'copy', combo: 'Ctrl+C', scope: 'global', singleKey: false },
  { id: 'cut', combo: 'Ctrl+X', scope: 'global', singleKey: false },
  { id: 'selectAll', combo: 'Ctrl+A', scope: 'global', singleKey: false },
  { id: 'apply', combo: 'Ctrl+Enter', scope: 'global', singleKey: false },
  { id: 'dismiss', combo: 'Escape', scope: 'global', singleKey: false },
  { id: 'fitAll', combo: 'Shift+1', scope: 'canvas', singleKey: false },
  { id: 'fitAllLegacy', combo: 'Z', scope: 'canvas', singleKey: true },
  { id: 'pan', combo: 'Space', scope: 'canvas', singleKey: true },
];

/** The selector that defines "the canvas" for scoping purposes. */
export const CANVAS_SELECTOR = '.canvas-viewport';

/**
 * Whether a canvas-scoped shortcut may fire right now.
 *
 * The board viewport is focusable (AX1 made it so, and gave it a roving grid),
 * so "is focus inside the canvas" is a question with a real answer. `body`
 * counts: a person who has clicked the board and not yet tabbed anywhere has
 * `document.activeElement === body`, and refusing them the canvas keys would
 * break the ordinary case to fix the edge one.
 */
export function canvasHasFocus(doc: Document = document): boolean {
  const active = doc.activeElement;
  if (!active || active === doc.body) return true;
  return !!(active as HTMLElement).closest?.(CANVAS_SELECTOR);
}

/**
 * Whether bare single-character shortcuts are allowed at all.
 *
 * A person using speech input can turn every one of them off in Settings →
 * Accessibility and keep the modifier combos, which is the "disableable" limb
 * of 2.1.4. Defaults on, because for everyone else they are useful.
 */
export function singleKeyEnabled(): boolean {
  return useSettings.getState().settings.accessibility?.singleKeyShortcuts ?? true;
}
