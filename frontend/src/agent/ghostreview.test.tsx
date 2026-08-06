// AX31 — for a non-sighted reviewer the plan had no preview at all.
//
// The ghost layer is this product's central claim: "what you approve is
// positioned identically to what commits". That claim is made entirely in
// pixels. The fallback — the line-by-line list — was a SECONDARY surface behind
// a `▴` glyph button, collapsed by default, so the canonical review surface for
// assistive technology was the one that usually did not exist.
//
// The compiler clause is the reason this file exists: the list is now always
// rendered and merely hidden from sight, because "always rendered" implemented
// as a second component that appears when the first one does not is two
// renderers that will disagree within a wave.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { Decide } from './AgentDecide';
import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { DEFAULT_SETTINGS } from '../api/types';
import type { AgentRun } from '../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Two actions and no delete, so the list's own default filter — which leads
// with deletes when a plan has any, deliberately — does not narrow it.
const BUILD = [
  { seq: 0, kind: 'create_column', elementId: 'c1', title: 'Locations', summary: 'Locations' },
  { seq: 1, kind: 'create_card', elementId: 'k1', parentId: 'c1', title: 'Sunrise scout', summary: 'card' },
];
const DESTROY = [{ seq: 0, kind: 'delete_element', elementId: 'old', title: 'Old schedule', summary: 'delete' }];

const run = (actions: unknown[] = BUILD): AgentRun => ({
  id: 'r1', state: 'PROPOSED', rev: 1,
  task: { intent: 'File these', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.01, calls: 1 },
  createdAt: '', updatedAt: '',
  plan: { summary: 'Some changes.', actions },
} as unknown as AgentRun);

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useBoard.setState({
    boardId: 'b1',
    elements: { b1: { id: 'b1', type: 'BOARD', content: {}, location: { parentId: '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 } } } as never,
    selection: new Set(),
    readOnly: false,
  });
  useSettings.setState({ settings: { ...DEFAULT_SETTINGS } });
  useAgent.setState({
    run: run(), events: [], adjustments: [], hoverSeq: null, tileFocus: null,
    stale: null, busy: false, recent: [], audit: [],
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

const render = () => act(() => { root.render(<Decide run={useAgent.getState().run!} />); });

describe('the plan list is the canonical review surface', () => {
  it('is in the DOM even when the canvas preview is the visible one', () => {
    render();
    // Visually collapsed — and present anyway. Before this it was absent, so a
    // screen-reader user reviewing a plan had a summary sentence and nothing at
    // all to walk.
    expect(container.querySelectorAll('.ap-row').length).toBe(2);
  });

  it('states nesting as a level rather than as inline padding alone', () => {
    render();
    const rows = [...container.querySelectorAll('.ap-row')];
    // The card goes inside the column the same plan creates: depth is a fact
    // about the plan and it was conveyed only in pixels of padding.
    expect(rows[0].getAttribute('aria-level')).toBe('1');
    expect(rows[1].getAttribute('aria-level')).toBe('2');
  });

  it('names a destructive row with a word, not a red tint', () => {
    useAgent.setState({ run: run(DESTROY) });
    render();
    const destructive = [...container.querySelectorAll('.ap-row')]
      .find((r) => r.className.includes('danger'))!;
    expect(destructive, 'the delete row is classed danger').toBeTruthy();
    expect(destructive.textContent).toContain('destructive');
  });

  it('marks a dropped row as disabled AND says so', () => {
    useAgent.setState({ run: run(DESTROY), adjustments: [{ kind: 'drop', seq: 0 }] });
    render();
    const dropped = [...container.querySelectorAll('.ap-row')]
      .find((r) => r.className.includes('dropped'))!;
    // Not aria-disabled: a listitem does not support it, so the attribute
    // would lint clean and be exposed by nothing. The word is the mechanism.
    expect(dropped.getAttribute('data-dropped')).toBe('true');
    // A strikethrough is not a word. Putting it back is one key away, so the
    // row stays in the list and says what state it is in.
    expect(dropped.textContent).toContain('dropped from this plan');
  });

  it('names the reparent control after its own row', () => {
    render();
    for (const select of container.querySelectorAll('.ap-row-dest-pick')) {
      const label = select.getAttribute('aria-label') ?? '';
      expect(label.length, 'a title is a tooltip, not an accessible name').toBeGreaterThan(0);
    }
  });
});
