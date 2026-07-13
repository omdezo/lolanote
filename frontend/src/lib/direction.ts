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

// ---- Arabic-Indic (Hindi) numerals -----------------------------------------
// The rule mirrors the direction rule: the FIRST strong letter of the field
// (or, in rich text, of the paragraph, falling back to the whole note)
// decides. Starts with Arabic → every digit typed there becomes ٠١٢٣٤٥٦٧٨٩;
// starts with Latin → digits stay 0-9. A manual direction override (RTL/LTR)
// forces the numeral system the same way it forces alignment. Existing text
// and pasted content are never rewritten retroactively.

const ARABIC_LETTER = /[ء-يٮ-ۓەۮۯۺ-ۿݐ-ݿࢠ-ࣿﭐ-﷿ﹰ-﻿]/;
const STRONG_LTR = /[A-Za-zÀ-ɏͰ-ϿЀ-ӿ]/;

// firstStrong returns the script of the first strong letter: true for
// Arabic, false for LTR, null when the text has none.
function firstStrong(text: string): boolean | null {
  for (const ch of text) {
    if (ARABIC_LETTER.test(ch)) return true;
    if (STRONG_LTR.test(ch)) return false;
  }
  return null;
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
// cannot do this). Whether a field is "Arabic" comes from its resolved
// direction: dir="auto" fields resolve RTL natively once their first strong
// letter is Arabic, and a manual RTL/LTR override wins either way. Digits
// are converted via execCommand so the native undo stack and caret survive.
// Rich-text editors (contenteditable) are skipped — ProseMirror hook below.
const SMART_INPUT_TYPES = new Set(['text', 'search', '']);

export function initSmartDigits(): void {
  document.addEventListener('beforeinput', (ev: InputEvent) => {
    if (ev.inputType !== 'insertText' || !ev.data || !/[0-9]/.test(ev.data)) return;
    const el = ev.target as HTMLInputElement | HTMLTextAreaElement | null;
    if (!el || (el as HTMLElement).isContentEditable) return;
    const isText = el instanceof HTMLTextAreaElement
      || (el instanceof HTMLInputElement && SMART_INPUT_TYPES.has(el.type));
    if (!isText) return;

    // Field starts with Arabic (dir=auto resolves rtl) or is forced RTL.
    let arabic = getComputedStyle(el).direction === 'rtl';
    if (!arabic && el.getAttribute('dir') !== 'ltr' && firstStrong(el.value) === null) {
      // Empty field: the insertion itself decides (e.g. autocomplete/IME
      // committing "مرحبا 5" in one batch).
      arabic = firstStrong(ev.data) === true;
    }
    if (!arabic) return;

    const converted = toArabicDigits(ev.data);
    if (converted === ev.data) return;
    ev.preventDefault();
    document.execCommand('insertText', false, converted);
  }, true);
}

// smartDigitsTextInput — ProseMirror handleTextInput for the Tiptap editors.
// The paragraph's first strong letter decides; an empty paragraph inherits
// the note's first strong letter (so numbered lines in an Arabic note get
// Arabic numerals); a forced element direction (dir on the card wrapper)
// overrides both. Typed loosely to stay free of prosemirror type imports.
export function smartDigitsTextInput(view: any, from: number, to: number, text: string): boolean {
  if (!/[0-9]/.test(text)) return false;

  let arabic: boolean;
  const forced = (view.dom as HTMLElement).closest('[dir="rtl"], [dir="ltr"]');
  if (forced) {
    arabic = forced.getAttribute('dir') === 'rtl';
  } else {
    const $from = view.state.doc.resolve(from);
    let ctx = firstStrong($from.parent.textContent as string);
    if (ctx === null) ctx = firstStrong(view.state.doc.textContent as string);
    if (ctx === null) ctx = firstStrong(text);
    arabic = ctx === true;
  }
  if (!arabic) return false;

  const converted = toArabicDigits(text);
  if (converted === text) return false;
  view.dispatch(view.state.tr.insertText(converted, from, to));
  return true;
}
