// FIRST: the bar reads the settings store, which resolves the OS theme with
// matchMedia at module scope, and jsdom has none. Import order is the mechanism.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { DEFAULT_SETTINGS } from '../api/types';
import { AgentBar } from './AgentBar';
import type { AgentPlan, AgentRun } from '../api/types';

// AX5 and AX6.
//
// The whole safety story — preview before commit, honest outcomes, per-action
// revert, cost visibility — was delivered as a visual mutation and nothing
// else. A repo-wide grep for aria-live returned zero, so a blind user was told
// nothing when the plan arrived, nothing while forty actions staged, nothing
// when the transaction committed. And the bar swapped six components on
// `run.state` while restoring focus to nowhere, so pressing Enter in the
// composer dropped focus onto <body> — which, with no landmarks and no
// headings, is a place with nothing to navigate back with.
//
// Asserted on the RENDERED DOM, because "the region exists" and "the region has
// the text in it at the moment the state changed" are different facts, and only
// the second one is announced.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const plan = (over: Partial<AgentPlan> = {}): AgentPlan => ({
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

const polite = () => container.querySelector('[role="status"][aria-live="polite"]');
const alert = () => container.querySelector('[role="alert"][aria-live="assertive"]');

beforeEach(() => {
  vi.useFakeTimers();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useSettings.setState({ settings: DEFAULT_SETTINGS, saveState: 'idle', sub: 'me' });
  useBoard.setState({ boardId: 'b1', readOnly: false, elements: {}, selection: new Set() } as never);
  useAgent.setState({
    run: null, events: [], adjustments: [], open: false, active: null, busy: false,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  } as never);
  act(() => { root.render(<AgentBar />); });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.useRealTimers();
});

describe('the run is announced, not only drawn', () => {
  it('both regions exist before there is anything to say', () => {
    // The mechanism, not a detail: a live region that appears at the same
    // moment its text does is one screen readers do not announce.
    expect(polite(), 'the polite region is mounted with the state component').not.toBeNull();
    expect(alert(), 'the assertive region is mounted with the state component').not.toBeNull();
    expect(alert()!.textContent).toBe('');
  });

  it('a proposal interrupts, once, with what it proposes', () => {
    act(() => { useAgent.setState({ run: run() } as never); });
    const said = alert()!.textContent ?? '';
    expect(said, 'the plan arrived in silence').toContain('Plan ready');
    expect(said, 'it did not say how much it proposes').toMatch(/2 changes/);
  });

  it('and prefers the sentence the plan carries over one composed from counts', () => {
    act(() => {
      useAgent.setState({ run: run({ plan: plan({ announce: '12 changes, 4 new, in Pre-Production' }) }) } as never);
    });
    expect(alert()!.textContent, 'AX38: the announcement belongs to the plan contract')
      .toContain('12 changes, 4 new, in Pre-Production');
  });

  it('applying, reverting and failing each get their own sentence', () => {
    const seen = new Set<string>();
    for (const state of ['COMPLETED', 'REVERTED', 'FAILED'] as const) {
      act(() => { useAgent.setState({ run: run({ id: `r-${state}`, state }) } as never); });
      const said = alert()!.textContent ?? '';
      expect(said.length, `${state} was silent`).toBeGreaterThan(0);
      seen.add(said);
    }
    expect(seen.size, 'two different outcomes announced identically').toBe(3);
  });

  it('the same state twice does not interrupt twice', () => {
    act(() => { useAgent.setState({ run: run({ state: 'COMPLETED' }) } as never); });
    const first = alert()!.textContent;
    act(() => { useAgent.setState({ run: run({ state: 'COMPLETED' }), busy: true } as never); });
    expect(alert()!.textContent).toBe(first);
  });

  it('progress is polite and throttled — forty actions is not forty interruptions', () => {
    act(() => {
      useAgent.setState({
        run: run({ state: 'RUNNING' }),
        events: [{ sequence: 1, message: 'Reading the board', state: 'RUNNING' }],
      } as never);
    });
    act(() => { vi.advanceTimersByTime(20); });
    expect(polite()!.textContent).toBe('Reading the board');

    // A burst inside the window collapses to the LAST message: the person wants
    // to know where the run is, not where it was when the window opened.
    act(() => {
      useAgent.setState({ events: [
        { sequence: 1, message: 'Reading the board', state: 'RUNNING' },
        { sequence: 2, message: 'Staged a column', state: 'RUNNING' },
      ] } as never);
    });
    act(() => {
      useAgent.setState({ events: [
        { sequence: 1, message: 'Reading the board', state: 'RUNNING' },
        { sequence: 2, message: 'Staged a column', state: 'RUNNING' },
        { sequence: 3, message: 'Staged a card', state: 'RUNNING' },
      ] } as never);
    });
    expect(polite()!.textContent, 'the throttle let a burst through').toBe('Reading the board');
    act(() => { vi.advanceTimersByTime(1600); });
    expect(polite()!.textContent).toBe('Staged a card');
  });
});

describe('focus goes where the decision is', () => {
  it('a proposal puts focus on Apply, not on <body>', () => {
    act(() => { useAgent.setState({ run: run() } as never); });
    const apply = container.querySelector('.ac-apply');
    expect(apply, 'the review card has no Apply button').not.toBeNull();
    expect(document.activeElement, 'focus fell to body the moment the plan arrived').toBe(apply);
  });

  it('a finished run puts focus on the outcome, one Tab from Undo', () => {
    act(() => { useAgent.setState({ run: run({ state: 'COMPLETED' }) } as never); });
    const outcome = container.querySelector('.ac-decide');
    expect(outcome).not.toBeNull();
    expect(outcome!.getAttribute('tabindex'), 'a focus target that is also a tab stop is a row that does nothing').toBe('-1');
    expect(document.activeElement).toBe(outcome);
  });
});
