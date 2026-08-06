// The accessibility panel was written and typechecked and NOBODY HAD SEEN IT
// DRAW ITSELF. Sixty-two i18n keys, four root-attribute channels and a shortcut
// table all compiled — which proves the shapes line up and proves nothing about
// whether the panel a person opens does what its labels say.
//
// So this file asserts the three things a compiler cannot: that the tab renders
// at all, that every control in it carries an accessible name (AX21's whole
// argument was that a named-nowhere switch is worse than no ARIA), and that
// choosing "Reduced" actually stamps `data-motion="reduced"` on the root and
// survives into the local mirror. The last one is the load-bearing case:
// motion sensitivity is the one preference whose failure mode is physical.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import axe from 'axe-core';

import { SettingsDialog } from './SettingsDialog';
import { useSettings } from '../../store/settingsStore';
import { useBoard } from '../../store/boardStore';
import { DEFAULT_SETTINGS } from '../../api/types';
import { t } from '../../i18n';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useBoard.setState({ user: { id: 'u1', keycloakSub: 's1', displayName: 'Amal', email: 'a@b.c', plan: 'free' } as never });
  useSettings.setState({ settings: { ...DEFAULT_SETTINGS } });
  localStorage.removeItem('qomra.accessibility');
  document.documentElement.removeAttribute('data-motion');
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

/** Open the dialog and switch to the Accessibility tab, as a person does. */
function openAccessibilityTab() {
  act(() => { root.render(<SettingsDialog onClose={() => {}} />); });
  const tab = [...container.querySelectorAll<HTMLButtonElement>('.settings-tab')]
    .find((b) => b.textContent?.includes(t('settings.accessibility')));
  expect(tab, 'the Accessibility tab is in the settings nav').toBeTruthy();
  act(() => { tab!.click(); });
}

describe('the accessibility panel', () => {
  it('renders every control the tab promises', () => {
    openAccessibilityTab();
    const body = container.querySelector('.settings-body')!;

    // Five choices and one switch: motion, contrast, text size, transparency,
    // announcement verbosity, single-key shortcuts.
    expect(body.querySelectorAll('[role="radiogroup"]').length).toBe(5);
    expect(body.querySelectorAll('[role="switch"]').length).toBe(1);
    // AX20's other half: the keymap a person cannot enumerate is one they
    // cannot avoid, so the table has to have rows in it.
    expect(body.querySelectorAll('.shortcut-table tbody tr').length).toBeGreaterThan(0);
  });

  it('names every control — no switch or radio announces as its role alone', () => {
    openAccessibilityTab();
    const body = container.querySelector('.settings-body')!;
    const unnamed: string[] = [];
    for (const el of body.querySelectorAll<HTMLElement>('[role="switch"], [role="radiogroup"], [role="radio"]')) {
      const byId = el.getAttribute('aria-labelledby');
      // getElementById rather than a selector: React's useId mints ids full of
      // colons, which are not valid in a CSS selector without escaping and
      // jsdom has no CSS.escape.
      const named = (byId && byId.split(/\s+/).every((id) => document.getElementById(id)))
        || !!el.getAttribute('aria-label')
        || (el.getAttribute('role') === 'radio' && !!el.textContent?.trim());
      if (!named) unnamed.push(el.outerHTML);
    }
    expect(unnamed.join('\n')).toBe('');
  });

  it('choosing Reduced stamps the root and survives into the mirror', () => {
    openAccessibilityTab();
    const body = container.querySelector('.settings-body')!;
    const reduced = [...body.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
      .find((b) => b.textContent === t('a11y.motionReduced'));
    expect(reduced, 'the Reduced option is rendered').toBeTruthy();

    act(() => { reduced!.click(); });

    expect(useSettings.getState().settings.accessibility.motion).toBe('reduced');
    // The attribute is what CSS and the tween wrapper read; the store agreeing
    // is not the same claim.
    expect(document.documentElement.getAttribute('data-motion')).toBe('reduced');
    // And the local mirror, because the server's typed decode drops what its
    // struct does not have and would otherwise undo this 600ms later.
    expect(JSON.parse(localStorage.getItem('qomra.accessibility') || '{}').motion).toBe('reduced');
  });

  it('has no serious or critical axe violations', async () => {
    openAccessibilityTab();
    const result = await axe.run(container, {
      rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
      resultTypes: ['violations'],
    });
    const bad = result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
    expect(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.html).join(' | ')}`).join('\n')).toBe('');
  });
});
