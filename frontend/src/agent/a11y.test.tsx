// FIRST: the composer now reads the account's settings store, which resolves
// the OS theme with matchMedia at module scope — and jsdom has none. Import
// order is the mechanism; see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';
import axe from 'axe-core';

import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { DEFAULT_SETTINGS } from '../api/types';
import { Pill } from './AgentPill';
import { Observing } from './AgentObserving';
import { Ask } from './AgentAsk';
import { Working } from './AgentWorking';
import { Decide } from './AgentDecide';
import { Done } from './AgentDone';
import type { AgentPlan, AgentRun } from '../api/types';

// render.test.tsx already builds a real DOM for every agent surface and argues
// exactly the right thing — "typechecking proves the shapes line up; it does
// not prove a component survives being asked to draw itself" — and then stops
// one assertion short. This is the next term of the same argument:
//
//   a rendered result no assistive technology can perceive is the same class of
//   lie as a write that lands on a key nothing reads.
//
// The corpus is blind along several axes; this is the one nobody named. Every
// probe, every render test, every screenshot has been sighted. So: axe over the
// same container, per agent state, failing on serious and critical.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const plan = (over: Partial<AgentPlan> = {}): AgentPlan => ({
  summary: 'Two changes.',
  actions: [
    { seq: 0, kind: 'create_column', elementId: 'c1', title: 'Pricing', summary: 'Pricing' },
    { seq: 1, kind: 'move_element', elementId: 'card-1', text: 'A card', destination: 'Pricing', parentId: 'c1', summary: 'move' },
  ],
  ...over,
} as AgentPlan);

const run = (over: Partial<AgentRun> = {}): AgentRun => ({
  id: 'r1', state: 'PROPOSED',
  task: { intent: 'Organize', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.012, calls: 1 },
  createdAt: '', updatedAt: '',
  plan: plan(),
  ...over,
} as unknown as AgentRun);

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);

  useBoard.setState({
    boardId: 'b1',
    elements: { b1: { id: 'b1', type: 'BOARD', content: {}, location: { parentId: '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 } } } as never,
    selection: new Set(),
    readOnly: false,
  });
  useAgent.setState({
    run: null, events: [], adjustments: [], hoverSeq: null,
    recent: [], audit: [], drift: null, reach: null, active: null,
    observedStaged: 0, busy: false, open: false,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  });
  // The composer is gated behind the first-run processing question (DL8), and
  // that gate replaces the whole card — so without this the composer audit
  // below would silently be auditing the consent screen instead.
  useSettings.setState({
    settings: {
      ...DEFAULT_SETTINGS,
      agent: { instructions: '', processingAcknowledgedAt: '2026-01-01T00:00:00Z' },
    },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

/**
 * Renders a surface and returns the violations a screen-reader user would
 * actually be stopped by.
 *
 * colour-contrast is excluded here and checked separately below: jsdom has no
 * layout engine, so axe cannot resolve a computed background and reports every
 * pair as "incomplete" — a check that can neither pass nor fail is worse than
 * no check, because it looks like one.
 */
async function violations(node: ReactElement): Promise<axe.Result[]> {
  act(() => { root.render(node); });
  const result = await axe.run(container, {
    rules: { 'color-contrast': { enabled: false }, region: { enabled: false } },
    resultTypes: ['violations'],
  });
  return result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
}

const describeViolations = (found: axe.Result[]) =>
  found.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.html).join(' | ')}`).join('\n');

describe('agent surfaces are perceivable', () => {
  const surfaces: [string, () => ReactElement][] = [
    ['the resting pill', () => <Pill />],
    ['the composer', () => <Ask />],
  ];

  for (const [name, render] of surfaces) {
    it(name, async () => {
      const found = await violations(render());
      expect(describeViolations(found)).toBe('');
    });
  }

  it('the first-run processing question', async () => {
    useSettings.setState({
      settings: { ...DEFAULT_SETTINGS, agent: { instructions: '', processingAcknowledgedAt: '' } },
    });
    expect(describeViolations(await violations(<Ask />))).toBe('');
  });

  it("a colleague's run", async () => {
    useAgent.setState({
      active: { state: 'RUNNING', mine: false, actor: 'Rana', startedAt: '' } as never,
      observedStaged: 3,
    });
    expect(describeViolations(await violations(<Observing />))).toBe('');
  });

  it('the working card', async () => {
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: [{ id: '', runId: 'r1', sequence: 1, type: 'action.staged', message: 'Column: Pricing', data: { kind: 'create_column' }, at: '' }] as never,
    });
    expect(describeViolations(await violations(<Working />))).toBe('');
  });

  it('the decision card', async () => {
    const r = run();
    useAgent.setState({ run: r });
    expect(describeViolations(await violations(<Decide run={r} />))).toBe('');
  });

  it('the outcome card', async () => {
    const r = run({ state: 'COMPLETED' });
    useAgent.setState({ run: r });
    expect(describeViolations(await violations(<Done run={r} />))).toBe('');
  });
});

// The clause AX33 exists for: a surface that renders a PLAN must state its
// accessible name and announce itself. Without this, AX5 regresses silently —
// the plan appears, the sighted review works perfectly, and a screen reader
// says nothing at all.
describe('a surface that renders a plan announces itself', () => {
  it('the working card carries a live region', async () => {
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: [{ id: '', runId: 'r1', sequence: 1, type: 'action.staged', message: 'Column: Pricing', data: { kind: 'create_column' }, at: '' }] as never,
    });
    act(() => { root.render(<Working />); });
    const live = container.querySelector('[aria-live], [role="status"], [role="alert"]');
    expect(live, 'a plan writing itself is announced to nobody').toBeTruthy();
    expect(live!.textContent?.trim().length ?? 0).toBeGreaterThan(0);
  });

  it('every control on the decision card has an accessible name', async () => {
    const r = run();
    useAgent.setState({ run: r });
    act(() => { root.render(<Decide run={r} />); });

    const unnamed = [...container.querySelectorAll('button, input, [role="switch"]')]
      .filter((c) => {
        const name = (c.getAttribute('aria-label')
          ?? c.getAttribute('title')
          ?? c.textContent
          ?? '').trim();
        return name === '';
      })
      .map((c) => c.outerHTML);
    expect(unnamed.join('\n'), 'controls a screen reader cannot name').toBe('');
  });
});

// AX15's contrast fixture, computed rather than guessed.
//
// Done here rather than through axe because jsdom cannot resolve a computed
// background colour, so the axe rule reports "incomplete" forever. The token
// pairs are the whole contract — every surface in the product draws its text
// from one of them — so checking the pairs directly is both the honest check
// and the complete one.
describe('the text tokens clear WCAG AA in both themes', () => {
  const light = { canvas: '#f5f5f7', surface: '#ffffff', ink: '#1d1d1f', ink2: '#48484d', ink3: '#86868b', accent: '#5e5ce6' };
  const dark = { canvas: '#1c1c1e', surface: '#2c2c2e', ink: '#f5f5f7', ink2: '#c9c9ce', ink3: '#8e8e93', accent: '#9d9bff' };

  const channel = (v: number) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  const luminance = (hex: string) => {
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
  };
  const ratio = (a: string, b: string) => {
    const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
    return (x + 0.05) / (y + 0.05);
  };

  for (const [theme, t] of [['light', light], ['dark', dark]] as const) {
    // 4.5:1 is the AA bar for body text. --ink-3 is used for secondary labels
    // at ~11px, which is body text by every definition that matters.
    it(`${theme}: primary and secondary ink on both surfaces`, () => {
      for (const bg of [t.canvas, t.surface]) {
        expect(ratio(t.ink, bg), `--ink on ${bg}`).toBeGreaterThanOrEqual(4.5);
        expect(ratio(t.ink2, bg), `--ink-2 on ${bg}`).toBeGreaterThanOrEqual(4.5);
      }
    });

    // 3:1 is the AA bar for large text and for non-text UI boundaries. --ink-3
    // is the muted label colour and does NOT clear 4.5 on white — stated as a
    // known floor rather than left as an unexamined assumption, so that raising
    // it later is a deliberate change with a test that moves.
    it(`${theme}: muted labels and the accent clear the 3:1 UI floor`, () => {
      expect(ratio(t.ink3, t.surface), '--ink-3 on the surface').toBeGreaterThanOrEqual(3);
      expect(ratio(t.accent, t.surface), '--accent on the surface').toBeGreaterThanOrEqual(3);
    });
  }
});
