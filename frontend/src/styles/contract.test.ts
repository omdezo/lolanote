import { describe, expect, it } from 'vitest';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { resolve } from 'node:path';
import { TEXT_PREVIEW_MAX, toTextPreview } from '../lib/textPreview';

// Some of what this wave fixed lives in files no component test can reach: the
// service worker (no window, no React), and CSS rules whose whole content is a
// media query or an opacity. Asserting on the source text is the honest way to
// pin them — the same shape as the Go side's boundary test, which greps handler
// signatures because the fact it is protecting is a fact about the text.

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8');
const sw = () => read('../../public/sw.js');
const global = () => read('./global.css');
const agentBar = () => read('./agent-bar.css');
const agent = () => read('./agent.css');

// ---- MO8 ------------------------------------------------------------------
// The branch deciding whether the app can ever open again wrote whatever came
// back, unconditionally, while the asset branch twelve lines above already
// guarded on res.ok. A captive portal answering 200 with somebody else's HTML
// became the offline shell forever, online and off.
describe('the service worker does not cache a hijacked navigation', () => {
  it('guards the shell write on status, type and redirect', () => {
    const src = sw();
    const guard = /if\s*\(\s*res\.ok\s*&&\s*res\.type === 'basic'\s*&&\s*!res\.redirected\s*\)/;
    expect(guard.test(src), 'the shell cache accepts any response again').toBe(true);
  });

  it('never puts the shell outside that guard', () => {
    const src = sw();
    // Every cache.put of '/' must sit inside the guarded block. There is one.
    const puts = src.match(/cache\.put\('\/'/g) ?? [];
    expect(puts.length).toBe(1);
  });

  it('names the shell cache off the build, so a deploy cannot inherit poison', () => {
    expect(sw()).toContain("searchParams.get('v')");
  });

  it('treats an old cached shell as suspect rather than as truth', () => {
    const src = sw();
    expect(src).toContain('MAX_SHELL_AGE_MS');
    expect(src).toContain("cache.delete('/')");
  });
});

// ---- MO3 ------------------------------------------------------------------
// The zoom cluster and its Fit button are the entire remaining navigation
// surface on a phone, and the agent bar's own mobile media query — the only
// width query in the product — drew a full-width shell straight over them.
describe('the agent bar does not bury the last navigation control', () => {
  it('reserves its height in a token the zoom cluster consumes', () => {
    expect(global()).toContain('--agent-bar-reserve');
    expect(global()).toMatch(/\.zoom-cluster\s*\{\s*bottom:\s*calc\(16px \+ var\(--agent-bar-reserve\)\)/);
  });

  it('and sets that token in the same query that causes the collision', () => {
    const src = agentBar();
    const mobile = src.slice(src.indexOf('@media (max-width: 720px)'));
    expect(mobile.slice(0, 900)).toContain('--agent-bar-reserve');
  });
});

// ---- AX8 ------------------------------------------------------------------
// `.ap-row-x` is the drop-this-action button in review and the undo-just-this
// button after apply. It was opacity:0 until hover, with no :focus-within and
// no coarse-pointer branch — so per-action review did not exist on a phone and
// was invisible to focus on a keyboard.
describe('destructive-adjacent controls are not hover-exclusive', () => {
  for (const [name, css] of [['agent-bar.css', agentBar], ['agent.css', agent]] as const) {
    it(`${name}: the drop control is visible before it is hovered`, () => {
      const src = css();
      const block = src.slice(src.indexOf('.ap-row-x {'));
      expect(block.slice(0, 300)).not.toMatch(/opacity:\s*0;/);
    });

    it(`${name}: and reveals on focus as well as on hover`, () => {
      expect(css()).toContain('.ap-row:focus-within .ap-row-x');
    });
  }

  it('a coarse pointer gets it unconditionally', () => {
    const src = global();
    const coarse = src.slice(src.indexOf('@media (pointer: coarse)'));
    expect(coarse).toContain('.ap-row-x, .ghost-chip-x { opacity: 1; }');
  });
});

// ---- AX26 -----------------------------------------------------------------
// The smallest targets were the highest-stakes: 18px to drop an agent action or
// undo one, 14px to resize, 15px to draw a connector. WCAG 2.5.8 asks 24.
describe('touch targets on a product that installs as a phone app', () => {
  it('expands the small circular controls without moving the design', () => {
    const src = global();
    const coarse = src.slice(src.indexOf('@media (pointer: coarse)'));
    for (const sel of ['.ap-row-x::before', '.task-check::before', '.connect-anchor::before']) {
      expect(coarse, `${sel} keeps a cursor-sized hit area on touch`).toContain(sel);
    }
    expect(coarse).toContain('inset: -13px');
  });

  it('flips the action bar below a card that is near the top of the screen', () => {
    const src = global();
    const coarse = src.slice(src.indexOf('@media (pointer: coarse)'));
    expect(coarse, '.el-actions sits at top:-42px and draws off-screen').toContain('.el-actions { top: auto;');
  });
});

// ---- AX25 -----------------------------------------------------------------
// Four media queries existed in 2,405 lines of CSS and three belonged to the
// agent bar. On a 375px phone the permanent rail took 20% of the width.
describe('the product reflows below a phone width', () => {
  it('turns the rail into a bottom bar', () => {
    const src = global();
    expect(src).toContain('@media (max-width: 640px)');
    const small = src.slice(src.indexOf('@media (max-width: 640px)'));
    expect(small).toContain('flex-direction: row');
  });

  it('stops the side panel and the modal from exceeding the screen', () => {
    const small = global().slice(global().indexOf('@media (max-width: 640px)'));
    expect(small).toContain('width: min(336px, 100%)');
    expect(small).toContain('max-width: 100%');
  });

  it('and a screen-reader-only utility exists at all', () => {
    expect(global(), 'glyph-only controls had nowhere to put a real name').toContain('.sr-only');
  });
});

// ---- AX25, clause 1.4.4 ---------------------------------------------------
// `body { font-size: 14px }` with several hundred more px sizes behind it meant
// the browser's own text setting did nothing whatsoever. Text-only resize is
// not page zoom, and it is the setting people who need larger text actually
// use — so a person who has set their default to 24px opened this app and got
// 14. The fix is only real if the root honours the browser AND nothing
// downstream re-pins a size in px.
describe('text resizes with the browser, not against it', () => {
  const styleSheets = () => [global(), agentBar(), agent(), read('./settings.css')];

  it('the root takes the browser size and applies density as a multiplier', () => {
    const src = global();
    expect(src).toMatch(/html\s*\{[^}]*font-size:\s*calc\(100%\s*\*\s*var\(--type-scale\)\)/);
    expect(src, 'compact density still overwrites the browser size outright')
      .toMatch(/\[data-density="compact"\]\s*\{[^}]*--type-scale:/);
  });

  it('no stylesheet pins a text size in px', () => {
    for (const [i, src] of styleSheets().entries()) {
      const pinned = src.match(/font-size:\s*[0-9.]+px/g) ?? [];
      expect(pinned, `stylesheet ${i} pins ${pinned.join(', ')} against the browser`).toEqual([]);
    }
  });

  it('the chrome that holds the text grows with it', () => {
    // 52px and 76px boxes around text that can double is text clipped in half.
    const src = global();
    expect(src).toMatch(/--topbar-h:\s*[0-9.]+rem/);
    expect(src).toMatch(/--rail-w:\s*[0-9.]+rem/);
  });

  it('and no component re-pins one inline either', () => {
    for (const name of [
      '../components/elements/ElementView.tsx',
      '../components/panels/SearchOverlay.tsx',
      '../components/panels/ShareDialog.tsx',
      '../components/panels/BoardsDrawer.tsx',
    ]) {
      // A bare number in a React style object is px, silently.
      expect(read(name), `${name} sets a numeric fontSize, which React writes as px`)
        .not.toMatch(/fontSize:\s*[0-9]/);
    }
  });
});

// ---- AX25, clause 1.4.10 --------------------------------------------------
// The standard names the target exactly: 320 CSS px wide — which is also 400%
// zoom on a 1280px display — and 256 tall. `body { overflow: hidden }` means
// anything pushed past the edge there is unreachable rather than merely off
// screen, so a topbar that does not wrap loses controls outright.
describe('the chrome reflows at the 320 x 256 floor', () => {
  const narrow = () => global().slice(global().indexOf('@media (max-width: 400px)'));
  const short = () => global().slice(global().indexOf('@media (max-height: 420px)'));

  it('wraps the topbar instead of pushing its controls off the edge', () => {
    expect(global(), 'no rule exists below the 640px phone break').toContain('@media (max-width: 400px)');
    const src = narrow();
    expect(src).toContain('flex-wrap: wrap');
    expect(src, 'the path back up the tree is truncated with no way to reach it')
      .toContain('overflow-x: auto');
  });

  it('gives the side panel the whole width rather than a fixed 336', () => {
    expect(narrow()).toContain('.side-panel { width: 100%; max-width: 100%; }');
  });

  it('lets a dialog taller than a landscape phone scroll inside itself', () => {
    expect(global()).toContain('@media (max-height: 420px)');
    expect(short(), 'a clipped modal simply has no confirm button').toContain('overflow-y: auto');
  });
});

// ---- MO5 ------------------------------------------------------------------
// `Math.min(pos.y, innerHeight - items.length * 34 - 16)` with no matching
// `Math.max`. A selected BOARD builds a 12–14 row menu; in landscape (667x375)
// that arithmetic yields y = MINUS 117, so Duplicate and Make synced copy were
// drawn above the top of the screen with no way to scroll to them. The guess
// was optimistic even in portrait — it counted no dividers.
describe('the context menu stays on the screen at both ends', () => {
  it('clamps upward as well as downward', () => {
    const src = read('../components/ui/ContextMenu.tsx');
    expect(src, 'a Math.min alone can only push a menu off the top')
      .toMatch(/Math\.max\(8,\s*Math\.min\(pos\.y/);
  });

  it('and measures its height instead of guessing it from the row count', () => {
    const src = read('../components/ui/ContextMenu.tsx');
    expect(src).toContain('setHeight(node.offsetHeight)');
  });

  it('a menu taller than the screen scrolls inside itself', () => {
    const block = global().slice(global().indexOf('.context-menu {'));
    const rule = block.slice(0, block.indexOf('}'));
    expect(rule).toContain('max-height: calc(100dvh - 16px)');
    expect(rule).toContain('overflow-y: auto');
  });
});

// ---- MO6 ------------------------------------------------------------------
// A position:fixed bottom element is placed against the LAYOUT viewport, which
// iOS does not shrink for the keyboard — and AgentAsk auto-focuses the composer
// 50ms after opening, so the keyboard rose over the field before the person
// acted. Android resizes by default, which made this a total iOS failure, a
// partial Android one, and a clean pass on every desktop emulator.
describe('the composer stays above the software keyboard', () => {
  it('the bar reserves the keyboard inset and the safe area', () => {
    const src = agentBar();
    expect(src).toMatch(/bottom: calc\(22px \+ var\(--kb-inset, 0px\) \+ env\(safe-area-inset-bottom/);
    const mobile = src.slice(src.indexOf('@media (max-width: 720px)'));
    expect(mobile.slice(0, 400), 'the phone breakpoint kept a bare 12px').toContain('var(--kb-inset, 0px)');
  });

  it('something actually publishes that variable', () => {
    const src = read('../lib/keyboardInset.ts');
    expect(src).toContain('visualViewport');
    expect(src).toContain("setProperty('--kb-inset'");
    expect(src, 'resize alone drifts: iOS scrolls the visual viewport instead').toContain("addEventListener('scroll'");
    expect(read('../main.tsx'), 'the listener ships and is never started').toContain('initKeyboardInset()');
  });

  it('and env() can resolve at all', () => {
    const html = readFileSync(resolve(__dirname, '../../index.html'), 'utf8');
    expect(html, 'without viewport-fit=cover every env() is zero').toContain('viewport-fit=cover');
    expect(html, 'Android needs telling, or the two platforms disagree').toContain('interactive-widget=resizes-content');
  });
});

// ---- MO7 ------------------------------------------------------------------
// Two compounding miscalibrations: a 42vh list measured against the LAYOUT
// viewport inside a ~330px visible strip, and a card anchored at the bottom
// growing upward with no cap — so the summary and the destructive-changes
// warning went off the top of the screen first, which is the wrong end to lose.
describe('the review card fits the screen it is reviewed on', () => {
  it('the card is capped by the visible viewport, keyboard included', () => {
    // `.agent-card` appears twice: once in the glass recipe it shares with the
    // pill, once as its own block. The cap belongs to the second.
    const block = agentBar().slice(agentBar().lastIndexOf('.agent-card {'));
    const rule = block.slice(0, block.indexOf('\n}'));
    expect(rule).toMatch(/max-height: calc\(100dvh[^;]*var\(--kb-inset, 0px\)/);
  });

  it('and the plan list flexes inside that cap rather than carrying its own', () => {
    const block = agentBar().slice(agentBar().indexOf('.ac-plan {'));
    const rule = block.slice(0, block.indexOf('\n}'));
    const decls = rule.replace(/\/\*[\s\S]*?\*\//g, '');
    expect(decls, 'a layout-viewport unit claimed 280px of a 330px strip').not.toContain('42vh');
    expect(decls).toContain('flex: 1 1 auto');
    expect(decls).toContain('min-height: 0');
  });

  it('framing measures the bar instead of assuming a desktop pill', () => {
    const src = read('../agent/useAgentShell.ts');
    expect(src).toContain("querySelector('.agent-shell')");
    expect(src, 'the real phone review card is 400-500px against a hardcoded 150')
      .toMatch(/shell\?\.offsetHeight/);
  });
});

// ---- JN5 ------------------------------------------------------------------
// Two writers of content.textPreview held contracts 40x apart, and the
// corrupting writer turned out to be the person opening the agent's work.
describe('textPreview has one contract', () => {
  it('both human writers go through the shared cap', () => {
    for (const name of ['NoteCard', 'DocumentCard']) {
      const src = read(`../components/elements/${name}.tsx`);
      expect(src, `${name} still caps by hand`).not.toContain('slice(0, 500)');
      expect(src, `${name} does not use the shared contract`).toContain('toTextPreview(');
    }
  });

  it('and the cap is a stated number, not a literal in two files', () => {
    expect(TEXT_PREVIEW_MAX).toBe(500);
    expect(toTextPreview('x'.repeat(900))).toHaveLength(500);
    expect(toTextPreview('short')).toBe('short');
  });
});

// ---- MO10 -----------------------------------------------------------------
// The manifest and head were brought up to the installability bar — `scope`,
// `id`, `lang`, `dir`, maskable and raster icons, apple-touch-icon, the three
// apple-mobile-web-app metas — and the icon FILES the new entries point at did
// not exist. A manifest that references four 404s installs worse than the
// single-SVG one it replaced: Android falls back to a white plinth, iOS falls
// back to a screenshot of the page. Metadata that names a file is a claim about
// the disk, and nothing was checking the disk.
describe('the PWA metadata points at files that exist', () => {
  const manifest = () => JSON.parse(read('../../public/manifest.webmanifest'));
  const html = () => read('../../index.html');

  it('ships every icon the manifest declares', () => {
    for (const icon of manifest().icons as Array<{ src: string }>) {
      const path = resolve(__dirname, '../../public', icon.src.replace(/^\//, ''));
      expect(existsSync(path), `${icon.src} is declared and missing`).toBe(true);
    }
  });

  it('ships a maskable icon, because without one Android draws a white plinth', () => {
    const maskable = (manifest().icons as Array<{ purpose?: string }>).filter((i) => i.purpose === 'maskable');
    expect(maskable.length).toBeGreaterThan(0);
  });

  it('ships the apple-touch-icon the head links, since iOS ignores the manifest', () => {
    const m = /rel="apple-touch-icon"\s+href="([^"]+)"/.exec(html());
    expect(m, 'no apple-touch-icon link — Add-to-Home-Screen takes a screenshot').toBeTruthy();
    expect(existsSync(resolve(__dirname, '../../public', m![1].replace(/^\//, '')))).toBe(true);
  });

  it('scopes the standalone window, so a LINK card cannot navigate the app away', () => {
    // Without `scope`, window.open from a link card can take the standalone
    // window to an arbitrary site with no chrome and no back button.
    expect(manifest().scope).toBe('/');
    expect(manifest().id).toBe('/');
  });

  it('declares a direction, for an app that is bilingual at the OS level too', () => {
    expect(manifest().dir).toBeTruthy();
    expect(manifest().lang).toBeTruthy();
  });
});

// ---- AX: every class the app RENDERS must exist in a stylesheet -------------
// The two skip links shipped as markup and translated strings with no CSS at
// all: `.skip-link` matched nothing anywhere, so both rendered as ordinary blue
// anchors stacked over the top-left of the app, on every load, for everyone.
// The accessibility feature was the most visible defect on the page.
//
// This is the frontend twin of the Go side's frontenddrift test: markup and
// style are two files nothing forces to agree, and the failure is silent in
// both directions.
describe('a class the app renders is a class a stylesheet defines', () => {
  const sheets = () => [global(), agentBar(), agent(), read('./settings.css')].join('\n');

  it('styles .skip-link, and hides it until it is focused', () => {
    const css = sheets();
    expect(css).toContain('.skip-link');
    // Hidden the way .sr-only hides — never display:none or visibility:hidden,
    // both of which would take the link out of the focus order and leave it
    // invisible AND unreachable, which is no skip link at all.
    // Anchored on the RULE, not the first mention: the first `.skip-link` in
    // the file is inside the comment explaining why the rule exists, and
    // matching that made the test pass on prose rather than on style.
    const at = css.search(/^\.skip-link\s*\{/m);
    expect(at).toBeGreaterThan(-1);
    const block = css.slice(at, at + 700);
    expect(block).toMatch(/clip-path:\s*inset\(50%\)/);
    expect(block).not.toMatch(/\.skip-link\s*\{[^}]*display:\s*none/);
    expect(block).not.toMatch(/\.skip-link\s*\{[^}]*visibility:\s*hidden/);
    // And it must come BACK on focus, or it is only hidden.
    expect(css).toMatch(/\.skip-link:focus/);
  });

  it('defines every class App.tsx renders for the skip targets', () => {
    const app = read('../App.tsx');
    for (const id of ['board-heading', 'agent-shell-anchor']) {
      // A skip link pointing at an id nothing carries scrolls nowhere and
      // focuses nothing — the same silent failure one layer along.
      expect(app.includes(`#${id}`)).toBe(true);
    }
  });
});

// A semantic list rendered without `list-style: none` is a browser doing
// exactly what it should — decimal markers, block layout, indentation — to
// markup that was added for a screen reader and never styled. It happened twice
// in one wave: the skip links above, and the breadcrumb, which shipped as
//     1. Home
//     >
//     2. Filmuuhjk
// stacked down the topbar. Both were accessibility fixes that became the most
// visible defect on the page.
//
// So this walks the components rather than trusting a list of known cases: any
// <ol> or <ul> given a className must have a rule for that class, and that rule
// must reset list-style. Finding the next one should not require a screenshot.

// A semantic list rendered without a `list-style: none` rule is a browser doing
// exactly what it should — decimal markers, block layout, indentation — to
// markup added for a screen reader and never styled. It happened twice in one
// wave: the skip links, and the breadcrumb, which shipped as
//     1. Home
//     >
//     2. Filmuuhjk
// stacked down the topbar. Both were accessibility fixes that became the most
// visible defect on the page, and both were found by a person looking at a
// screenshot rather than by anything here.
//
// So this walks the components instead of trusting a list of known cases.
describe('every semantic list the app renders is styled as a list', () => {
  const componentFiles = () => {
    const out: string[] = [];
    const walk = (dir: string) => {
      let entries: string[] = [];
      try { entries = readdirSync(resolve(__dirname, dir)); } catch { return; }
      for (const e of entries) {
        const full = `${dir}/${e}`;
        let isDir = false;
        try { isDir = statSync(resolve(__dirname, full)).isDirectory(); } catch { continue; }
        if (isDir) { walk(full); continue; }
        if (e.endsWith('.tsx') && !e.includes('.test.')) out.push(full);
      }
    };
    walk('..');
    return [...new Set(out)];
  };

  const classedLists = () => {
    const found: { file: string; cls: string }[] = [];
    for (const file of componentFiles()) {
      for (const m of read(file).matchAll(/<(?:ol|ul)\s[^>]*className="([A-Za-z0-9_-]+)"/g)) {
        found.push({ file, cls: m[1] });
      }
    }
    return found;
  };

  // The guard on the guard. A discovery walk that silently finds nothing passes
  // every assertion after it, and the first version of this test did exactly
  // that — it went green with the breadcrumb rule deleted.
  it('actually finds the lists it is meant to be checking', () => {
    expect(componentFiles().length).toBeGreaterThan(20);
    const lists = classedLists();
    expect(lists.length).toBeGreaterThan(4);
    expect(lists.map((l) => l.cls)).toContain('crumb-list');
  });

  it('gives each one a rule that removes the default markers', () => {
    const css = [global(), agentBar(), agent(), read('./settings.css')].join('\n');
    const offenders: string[] = [];
    for (const { file, cls } of classedLists()) {
      // Every rule mentioning the class, not just one anchored to a line
      // start: real stylesheets indent rules inside media queries and group
      // selectors with commas, and an over-strict match reports a styled class
      // as unstyled — which is a guard that cries wolf until it is deleted.
      const rule = new RegExp('\.' + cls + '(?![\w-])[^;{}]*\{([^}]*)\}', 'g');
      const blocks = [...css.matchAll(rule)].map((m) => m[1]);
      if (blocks.length === 0) {
        offenders.push(`${file}: .${cls} has no rule in any stylesheet`);
        continue;
      }
      // A list that never paints cannot paint a marker. LineLayer describes
      // its connectors in a screen-reader-only <ul>, and demanding a
      // list-style reset on something clipped to a 1px box would be asking for
      // a rule with no effect — the kind of noise that gets a guard switched
      // off. Judged by what the rule DOES, never by the class name.
      if (blocks.some((b) => /clip-path:\s*inset\(50%\)/.test(b))) continue;
      if (!blocks.some((b) => /list-style:\s*none/.test(b))) {
        offenders.push(`${file}: .${cls} never resets list-style`);
      }
    }
    expect(offenders).toEqual([]);
  });
});

// `width: 100%` on a flex CHILD is a flex-basis of the whole row, so any
// sibling with `flex: 1` (grow from a basis of zero) is left with nothing. The
// agent's standing-notes textarea did exactly this: its label rendered one word
// per line down a 40px gutter beside a box that was mostly empty.
//
// It is not a missing rule — every class involved had one — which is why the
// list guard above could never have caught it. The failure is two correct rules
// that are wrong together, and that only shows up on screen.
describe('a flex child does not starve its siblings', () => {
  // Comments stripped FIRST. Three tests in this file have now matched prose
  // instead of code — a rule's own explanation of what it avoids reads exactly
  // like the thing it avoids, and a guard that fires on its own documentation
  // is a guard nobody keeps.
  const codeOnly = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '');

  it('never sizes a settings control with a percentage basis', () => {
    const css = codeOnly(read('./settings.css'));
    // Every rule for a control that lives inside .settings-row. The row is
    // display:flex, so a percentage width here is a basis, not a width.
    const offenders: string[] = [];
    for (const m of css.matchAll(/\.(sr-[a-z-]+)\s*\{([^}]*)\}/g)) {
      const [, cls, block] = m;
      if (cls === 'sr-text' || cls === 'sr-label' || cls === 'sr-sub') continue;
      if (/width:\s*100%/.test(block) && !/flex-direction:\s*column/.test(block)) {
        offenders.push(`.${cls} takes width:100% as a flex basis and starves the label`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it('stacks the row that holds a multi-line control', () => {
    const css = codeOnly(read('./settings.css'));
    expect(css, 'a five-row textarea beside its label leaves the label no width')
      .toMatch(/\.settings-row:has\(\.sr-textarea\)\s*\{[^}]*flex-direction:\s*column/);
  });
});

// Attachment URLs stopped being public blob paths and became
// `/api/v1/attachments/:id/blob`, which asks the permission question on every
// read. That is the right design — it is what makes revoking access actually
// revoke it — and it broke every photograph in the product, because a browser
// sends no Authorization header for an <img>. Each one answered 403 and drew
// the broken-image glyph with its alt text where the picture should be.
//
// Nothing in the type system connects "this src needs a token" to "this element
// cannot send one", so it is asserted here.
describe('an image behind the permission check can actually authenticate', () => {
  const cards = () => read('../components/elements/cards.tsx');

  it('renders attachment sources through AuthedImage, never a bare <img>', () => {
    const src = cards();
    // c.url is the IMAGE element's attachment; tileImg is a board tile's.
    expect(src).not.toMatch(/<img[^>]*src=\{c\.url\}/);
    expect(src).not.toMatch(/<img[^>]*src=\{tileImg\}/);
    expect(src).toMatch(/<AuthedImage[^>]*src=\{c\.url\}/);
    expect(src).toMatch(/<AuthedImage[^>]*src=\{tileImg\}/);
  });

  it('and AuthedImage sends the token and releases the object URL', () => {
    const src = read('../components/elements/AuthedImage.tsx');
    expect(src, 'no Authorization header means the 403 comes straight back').toContain('Authorization');
    expect(src, 'a board scrolled for an hour would hold every image it drew')
      .toContain('revokeObjectURL');
    // A remote thumbnail is somebody else's host and must not be handed our token.
    expect(src).toMatch(/\^https\?:/);
  });
});
