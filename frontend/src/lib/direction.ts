// Per-element text direction (RTL/LTR) with first-strong-letter detection.
//
// Every text surface defaults to 'auto': the browser's native bidi algorithm
// (dir="auto" on inputs, unicode-bidi: plaintext on rich-text blocks) reads
// the first strongly-directional character — Arabic/Hebrew → RTL, Latin →
// LTR — live as you type, independently per paragraph/field. A manual
// override stored on the element (content.textDirection) forces the whole
// card one way until switched back.
import type { QElement } from '../api/types';

export type TextDirection = 'auto' | 'ltr' | 'rtl';

// elementDir reads the element's stored override, defaulting to 'auto'.
export function elementDir(element: QElement | undefined | null): TextDirection {
  const d = element?.content?.textDirection;
  return d === 'ltr' || d === 'rtl' ? d : 'auto';
}

// dirAttr is the value for a dir= attribute. 'auto' is a valid HTML value
// and gives native first-strong detection on inputs and text containers.
export function dirAttr(d: TextDirection): 'auto' | 'ltr' | 'rtl' {
  return d;
}

// cycleDir steps auto → rtl → ltr → auto (the format-bar button).
export function cycleDir(d: TextDirection): TextDirection {
  return d === 'auto' ? 'rtl' : d === 'rtl' ? 'ltr' : 'auto';
}

// Element types that carry user-visible text and get the direction control.
const TEXT_TYPES = new Set([
  'CARD', 'DOCUMENT', 'TASK_LIST', 'COLUMN', 'COMMENT_THREAD',
  'TABLE', 'IMAGE', 'LINK', 'FILE', 'BOARD', 'ALIAS', 'CLONE',
]);

export function hasTextDirection(element: QElement): boolean {
  return TEXT_TYPES.has(element.type);
}
