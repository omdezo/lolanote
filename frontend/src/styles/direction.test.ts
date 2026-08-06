import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

// CV9 — the UI never went RTL.
//
// `setLanguage` stamped <html lang> and nothing anywhere wrote `dir`; a grep for
// `documentElement.dir` returned zero. So the complete `ar` dictionary shipped
// into a layout that never mirrors: topbar, tool rail, breadcrumbs, agent bar,
// review list, outcome card and ghost badges all stayed left-to-right. Per
// element CONTENT was beautifully bidi-aware; the chrome around it was not.
//
// The risk of this fix is not the mirroring — it is the exclusion boundary. A
// card's left/top are absolute board coordinates shared with every collaborator
// and stored on the server. If the canvas mirrored, one person switching the UI
// to Arabic would appear to move everybody's boards. So `.canvas-layer` pins
// `direction: ltr` and everything under it keeps physical properties on
// purpose, and this file exists to keep both halves of that true.

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');
const global = () => read('./global.css');
const settings = () => read('./settings.css');
const store = () => read('../store/settingsStore.ts');

describe('the document takes a direction, not just a language', () => {
  it('applySideEffects writes dir on the root', () => {
    const src = store();
    expect(src, 'theme, density, dot grid and accent were applied and direction was not')
      .toMatch(/root\.setAttribute\('dir',/);
    expect(src).toMatch(/lang === 'ar' \? 'rtl' : 'ltr'/);
  });

  it('and does it before the language listeners fire, so one render sees both', () => {
    const src = store();
    const body = src.slice(src.indexOf('function applySideEffects'));
    const dirAt = body.indexOf("setAttribute('dir'");
    const langAt = body.indexOf('setLanguage(');
    expect(dirAt).toBeGreaterThan(-1);
    expect(langAt).toBeGreaterThan(-1);
    expect(dirAt, 'a component re-rendered by the language change would read the old dir')
      .toBeLessThan(langAt);
  });
});

describe('the canvas is excluded, deliberately', () => {
  it('pins its own direction so board coordinates never mirror', () => {
    expect(global()).toMatch(/\.canvas-layer\s*\{[^}]*direction:\s*ltr/);
  });

  it('and keeps physical left/top, which is what the exclusion is for', () => {
    expect(global()).toMatch(/\.canvas-layer\s*\{[^}]*left:\s*0/);
  });
});

describe('the chrome mirrors', () => {
  // One representative rule per surface the person walks through. Each of these
  // was a physical property, and each is a place an Arabic session visibly
  // failed: the rail's border on the wrong edge, the panel docked on the wrong
  // side, the notification badge over the wrong corner.
  const mustBeLogical: Array<[string, RegExp]> = [
    ['the tool rail border', /\.rail\s*\{[^}]*border-inline-end/],
    ['the side panel dock', /\.side-panel\s*\{[^}]*inset-inline-end:\s*0/],
    ['the side panel border', /\.side-panel\s*\{[^}]*border-inline-start/],
    ['the zoom cluster', /\.zoom-cluster\s*\{[^}]*inset-inline-end/],
    ['the notification badge', /\.notif-badge\s*\{[^}]*inset-inline-end/],
    ['the notification dropdown', /\.notif-dropdown\s*\{[^}]*inset-inline-end/],
    ['the tools flyout', /\.flyout\s*\{[^}]*inset-inline-start/],
    ['a context menu row', /\.ctx-item\s*\{[^}]*text-align:\s*start/],
  ];
  for (const [what, re] of mustBeLogical) {
    it(`${what} is on the inline axis`, () => {
      expect(re.test(global()), `${what} still names a physical side`).toBe(true);
    });
  }

  it('the submenu opens toward the reading direction', () => {
    expect(settings()).toMatch(/\.ctx-submenu\s*\{[^}]*inset-inline-start/);
    expect(settings()).toContain('.ctx-submenu.flipped { inset-inline-start: auto; inset-inline-end:');
  });

  it('the settings rail divider and its rows follow the text', () => {
    expect(settings()).toMatch(/border-inline-end: 1px solid var\(--hairline\); padding: 16px 10px;/);
    expect(settings()).not.toMatch(/text-align: left/);
  });

  it('the switch knob travels the way the text does', () => {
    expect(settings(), 'the toggle slid right in a right-to-left UI')
      .toContain('[dir="rtl"] .switch.on::after');
  });

  it('the Boards drawer docks by class, not by an inline physical style', () => {
    const src = readFileSync(resolve(__dirname, '../components/panels/BoardsDrawer.tsx'), 'utf8');
    expect(src).toContain('side-panel start');
    expect(src, 'an inline left/right cannot be mirrored by any amount of dir')
      .not.toMatch(/left:\s*0,\s*right:/);
    expect(global()).toContain('.side-panel.start');
  });
});

describe('directions drawn as glyphs', () => {
  it('the destination arrow flips with the document', () => {
    expect(global()).toContain('[dir="rtl"] .dir-arrow');
    for (const name of ['../agent/AgentDecide.tsx', '../agent/AgentDone.tsx']) {
      // Comments talk about arrows; only rendered ones matter.
      const src = read(name).replace(/\/\*[\s\S]*?\*\//g, ' ').replace(/^[ \t]*\/\/.*$/gm, ' ');
      const classed = src.match(/dir-arrow" aria-hidden="true">→/g) ?? [];
      const bare = src.match(/(?<!dir-arrow" aria-hidden="true">)→/g) ?? [];
      expect(classed.length, `${name} renders no direction-aware arrow`).toBeGreaterThan(0);
      expect(bare, `${name} still hard-codes a westward arrow`).toEqual([]);
    }
  });
});
