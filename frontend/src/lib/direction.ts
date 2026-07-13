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

// ---- Contextual Arabic-Indic (Hindi) numerals ------------------------------
// While typing inside Arabic text, the number row produces ٠١٢٣٤٥٦٧٨٩ instead
// of 0123456789 — decided per keystroke from the nearest strong letter around
// the caret (Word's "context" digit behavior). Existing text, pasted content,
// and URLs are never rewritten retroactively.

const ARABIC_LETTER = /[ء-يٮ-ۓەۮۯۺ-ۿݐ-ݿࢠ-ࣿﭐ-﷿ﹰ-﻿]/;
const STRONG_LTR = /[A-Za-zÀ-ɏͰ-ϿЀ-ӿ]/;

// strongContext returns the script of the nearest strong letter: true for
// Arabic, false for LTR, null when the text has none.
function strongContext(text: string, backwards: boolean): boolean | null {
  if (backwards) {
    for (let i = text.length - 1; i >= 0; i--) {
      const ch = text[i];
      if (ARABIC_LETTER.test(ch)) return true;
      if (STRONG_LTR.test(ch)) return false;
    }
  } else {
    for (const ch of text) {
      if (ARABIC_LETTER.test(ch)) return true;
      if (STRONG_LTR.test(ch)) return false;
    }
  }
  return null;
}

// convertDigitsContextual rewrites the digits of an insertion: each digit
// becomes Arabic-Indic when the nearest strong letter before it — inside the
// inserted text itself, else in the text before the caret, else after — is
// Arabic. Handles single keystrokes and batch insertions (IME, autocomplete)
// identically.
export function convertDigitsContextual(text: string, before: string, after = ''): string {
  if (!/[0-9]/.test(text)) return text;
  let ctx = strongContext(before, true);
  const afterCtx = ctx === null ? strongContext(after, false) : null;
  let out = '';
  for (const ch of text) {
    if (ARABIC_LETTER.test(ch)) ctx = true;
    else if (STRONG_LTR.test(ch)) ctx = false;
    if (ch >= '0' && ch <= '9' && (ctx ?? afterCtx) === true) {
      out += String.fromCharCode(ARABIC_ZERO + ch.charCodeAt(0) - 48);
    } else {
      out += ch;
    }
  }
  return out;
}

const ARABIC_ZERO = 0x0660;

// toArabicDigits maps 0-9 → ٠-٩ (other characters pass through).
export function toArabicDigits(s: string): string {
  return s.replace(/[0-9]/g, (d) => String.fromCharCode(ARABIC_ZERO + d.charCodeAt(0) - 48));
}

// normalizeDigits maps Arabic-Indic (٠-٩) and Persian (۰-۹) digits back to
// 0-9 so numeric parsing (table alignment, formulas) understands both.
export function normalizeDigits(s: string): string {
  return s
    .replace(/[٠-٩]/g, (d) => String.fromCharCode(d.charCodeAt(0) - ARABIC_ZERO + 48))
    .replace(/[۰-۹]/g, (d) => String.fromCharCode(d.charCodeAt(0) - 0x06f0 + 48));
}

// initSmartDigits installs ONE native beforeinput listener that covers every
// plain text field in the app (React's onBeforeInput rides the legacy
// textInput event and never sees native beforeinput, so per-component props
// cannot do this). Typed digits in an Arabic context become Arabic-Indic via
// execCommand, keeping the native undo stack and caret intact. Rich-text
// editors (contenteditable) are skipped — ProseMirror has its own hook below.
const SMART_INPUT_TYPES = new Set(['text', 'search', '']);

export function initSmartDigits(): void {
  document.addEventListener('beforeinput', (ev: InputEvent) => {
    if (ev.inputType !== 'insertText' || !ev.data || !/[0-9]/.test(ev.data)) return;
    const el = ev.target as HTMLInputElement | HTMLTextAreaElement | null;
    if (!el || (el as HTMLElement).isContentEditable) return;
    const isText = el instanceof HTMLTextAreaElement
      || (el instanceof HTMLInputElement && SMART_INPUT_TYPES.has(el.type));
    if (!isText) return;
    const start = el.selectionStart ?? el.value.length;
    const end = el.selectionEnd ?? start;
    const converted = convertDigitsContextual(ev.data, el.value.slice(0, start), el.value.slice(end));
    if (converted === ev.data) return;
    ev.preventDefault();
    document.execCommand('insertText', false, converted);
  }, true);
}

// smartDigitsTextInput — ProseMirror handleTextInput for the Tiptap editors.
// Typed loosely so this module stays free of prosemirror type imports.
export function smartDigitsTextInput(view: any, from: number, to: number, text: string): boolean {
  if (!/[0-9]/.test(text)) return false;
  const $from = view.state.doc.resolve(from);
  const parent = $from.parent;
  const before = parent.textBetween(0, $from.parentOffset, undefined, ' ');
  const $to = view.state.doc.resolve(to);
  const after = $to.parent === parent
    ? parent.textBetween(Math.min($to.parentOffset, parent.content.size), parent.content.size, undefined, ' ')
    : '';
  const converted = convertDigitsContextual(text, before, after);
  if (converted === text) return false;
  view.dispatch(view.state.tr.insertText(converted, from, to));
  return true;
}
