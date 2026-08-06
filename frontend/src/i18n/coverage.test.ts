import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve } from 'node:path';
import type { Language } from '../api/types';
import { DICTS, KEYS } from './index';

// JN4 — the gate, not the sweep.
//
// Counted per file, `t(` appeared 18 times in AgentAsk and 25 in AgentDecide
// and ZERO times in BoardsDrawer, ShareDialog, TrashPanel, TemplatePicker,
// SearchOverlay, NotificationsBell, PasswordGate, ColumnView and DocumentCard.
// The journey an Arabic-first user actually walks — sign in, open Boards, pick
// a template, share with a producer, recover a deleted card — was English at
// every step, and the one perfectly translated surface was the agent, whose
// entry point in the rail was the single rail button whose label was not t().
// The agent had been polished to a standard the journey around it never
// reached, which is the most visible possible statement about what the product
// thinks matters.
//
// The sweep is an afternoon and will rot within one wave without this. So the
// deliverable is the gate: any string literal that reaches a person — JSX text,
// or a title / placeholder / aria-label / alt attribute — must come from the
// dictionary. It reads the TSX as text, the same way the Go side's boundary
// test greps handler signatures, because the fact being protected is a fact
// about the text.

const SRC = resolve(__dirname, '..');

function tsxUnder(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const path = resolve(dir, name);
    if (statSync(path).isDirectory()) { out.push(...tsxUnder(path)); continue; }
    if (name.endsWith('.tsx') && !name.includes('.test.')) out.push(path);
  }
  return out;
}

/** Every surface a person reads. */
function gatedFiles(): string[] {
  return [
    ...tsxUnder(resolve(SRC, 'components')),
    ...tsxUnder(resolve(SRC, 'canvas')),
    ...tsxUnder(resolve(SRC, 'agent')),
    resolve(SRC, 'App.tsx'),
  ];
}

/** Comments are prose about the code, not prose shown to anyone. */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/^[ \t]*\/\/.*$/gm, ' ')
    .replace(/([^:'"`\\])\/\/[^\n'"`]*$/gm, '$1');
}

/**
 * Characters that appear constantly in TypeScript and effectively never in a
 * sentence a person reads. Their presence is how a chunk of source between a
 * `>` and a `<` is told apart from JSX text without parsing the file.
 */
const CODEY = /[;=()[\]`"$\\|&#@]/;
const WORD = /[A-Za-z]{3}/;

/** A chunk opening with one of these is the tail of an expression, not prose:
 *  `<Icon />, CARD: <Note …>` yields ", CARD:" between two tags. */
const CONTINUES_CODE = /^[,:?.)\]}]/;

/**
 * Text that is punctuation, a glyph, or an identifier the reader never sees as
 * a sentence. Deliberately short: every addition here is a hole in the gate.
 */
const ALLOWED_TEXT = new Set([
  'Q',      // the brand mark on the boot screen and the topbar
  'Qomra',  // the product name, which is not translated
  'Note',   // its other half — the wordmark is rendered `Qomra<em>Note</em>`
  'QomraNote',
]);

const ATTR = /\b(?:title|placeholder|aria-label|aria-roledescription|alt)\s*=\s*(["'])([^"']*)\1/g;

function violations(src: string): string[] {
  const body = stripComments(src);
  const found: string[] = [];

  for (const m of body.matchAll(ATTR)) {
    const value = m[2].trim();
    if (!value) continue; // alt="" is a deliberate "this image carries nothing"
    if (!WORD.test(value)) continue;
    if (ALLOWED_TEXT.has(value)) continue;
    found.push(`${m[0]}`);
  }

  // The `[^=]` is what tells a closing tag from an arrow function: without it
  // every `(id: string) => Promise<void>` signature reads as the word "Promise"
  // shown to a person.
  for (const m of body.matchAll(/[^=]>([^<>{}]+)</g)) {
    const text = m[1].replace(/\s+/g, ' ').trim();
    if (!text || CODEY.test(text) || CONTINUES_CODE.test(text) || !WORD.test(text)) continue;
    if (ALLOWED_TEXT.has(text)) continue;
    found.push(text);
  }
  return found;
}

describe('the journey is translated, not just the agent', () => {
  it('no user-visible literal survives under components/, canvas/ or agent/', () => {
    const offenders: string[] = [];
    for (const path of gatedFiles()) {
      const bad = violations(readFileSync(path, 'utf8'));
      if (bad.length) offenders.push(`${path.slice(SRC.length + 1)}: ${bad.join(' | ')}`);
    }
    expect(offenders, 'these strings reach a person in English whatever their language').toEqual([]);
  });

  it('the gate can actually see a violation', () => {
    // A gate nobody has watched fail is a gate nobody knows works.
    expect(violations('<button title="Delete forever">Restore</button>')).toEqual([
      'title="Delete forever"',
      'Restore',
    ]);
    expect(violations("<button title={t('trash.restore')}>{t('trash.restore')}</button>")).toEqual([]);
  });
});

describe('both dictionaries answer every key', () => {
  it('ar is complete — a missing key silently renders English', () => {
    const missing: Record<Language, string[]> = { en: [], ar: [] };
    for (const lang of Object.keys(DICTS) as Language[]) {
      for (const key of KEYS) {
        if (!DICTS[lang][key]) missing[lang].push(key);
      }
    }
    expect(missing.ar, 'these keys fall back to English on an Arabic board').toEqual([]);
    expect(missing.en).toEqual([]);
  });
});
