// FIRST: jsdom has neither matchMedia nor ResizeObserver, and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useAgent } from './agentStore';
import { useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { GhostLayer } from './GhostLayer';
import { Decide } from './AgentDecide';
import { Working } from './AgentWorking';
import { phraseKeyFor } from './AgentWorking';
import type { AgentAction, AgentEvent, AgentPlan, AgentRun, QElement } from '../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const el = (id: string, type: string, parentId: string, position = { x: 0, y: 0 }, width = 260): QElement => ({
  id, type, content: {},
  location: { parentId, section: 'CANVAS', position, index: 1, width, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const action = (over: Partial<AgentAction> = {}): AgentAction => ({
  seq: 0, kind: 'create_column', elementId: 'c1', title: 'Casting', summary: 'Casting', ...over,
});

const run = (over: Partial<AgentRun> = {}): AgentRun => ({
  id: 'r1', state: 'PROPOSED',
  task: { intent: 'Draw the flow', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0, calls: 1 },
  createdAt: new Date(Date.now() - 5000).toISOString(), updatedAt: '',
  ...over,
} as unknown as AgentRun);

function html(node: ReactElement): string {
  act(() => { root.render(node); });
  return container.innerHTML;
}

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);

  useBoard.setState({
    boardId: 'b1',
    elements: { b1: el('b1', 'BOARD', '') },
    selection: new Set(),
    readOnly: false,
  } as never);
  useView.setState({ sizes: {}, overlays: [], drag: null });
  useAgent.setState({
    run: null, events: [], adjustments: [], hoverSeq: null, tileFocus: null,
    recent: [], audit: [], drift: null, reach: null, active: null, stale: null,
    observedStaged: 0, busy: false, open: false,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 12 } },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.useRealTimers();
});

// ---- CV3 ------------------------------------------------------------------
// Connect actions get no Position from the server's layout pass and were then
// filtered out of the ghost layer, so the whole flow/tree capability previewed
// as loose boxes — with the edges that DECIDE where the boxes sit being the one
// thing the reviewer could not see.
describe('the preview draws the arrows', () => {
  it('between two boxes the same plan is placing', () => {
    useAgent.setState({
      run: run({
        plan: {
          actions: [
            action({ seq: 0, elementId: 'a', title: 'Script', position: { x: 0, y: 0, width: 240 } }),
            action({ seq: 1, elementId: 'b', title: 'Boards', position: { x: 400, y: 0, width: 240 } }),
            action({ seq: 2, kind: 'connect', elementId: 'l1', fromId: 'a', toId: 'b', summary: 'leads to' }),
          ],
        } as AgentPlan,
      }),
    });
    html(<GhostLayer />);
    expect(container.querySelectorAll('.ghost-line').length, 'the connector previewed as nothing').toBe(1);
  });

  it('between a box it is placing and an element already on the board', () => {
    useBoard.setState({
      elements: { b1: el('b1', 'BOARD', ''), live: el('live', 'CARD', 'b1', { x: 600, y: 200 }) },
    } as never);
    useAgent.setState({
      run: run({
        plan: {
          actions: [
            action({ seq: 0, elementId: 'a', position: { x: 0, y: 0, width: 240 } }),
            action({ seq: 1, kind: 'connect', elementId: 'l1', fromId: 'a', toId: 'live', summary: 'depends on' }),
          ],
        } as AgentPlan,
      }),
    });
    html(<GhostLayer />);
    expect(container.querySelectorAll('.ghost-line').length).toBe(1);
  });

  it('and nothing at all when an endpoint is not on this canvas', () => {
    useAgent.setState({
      run: run({
        plan: {
          actions: [
            action({ seq: 0, elementId: 'a', position: { x: 0, y: 0, width: 240 } }),
            // 'elsewhere' lives on a nested board: its coordinates belong to a
            // canvas that is not this one, so a line to it would be a promise
            // about a place that does not exist here.
            action({ seq: 1, kind: 'connect', elementId: 'l1', fromId: 'a', toId: 'elsewhere', summary: 'x' }),
          ],
        } as AgentPlan,
      }),
    });
    html(<GhostLayer />);
    expect(container.querySelectorAll('.ghost-line').length).toBe(0);
  });

  it('a plan that is nothing but arrows still renders a layer', () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        x: el('x', 'CARD', 'b1', { x: 0, y: 0 }),
        y: el('y', 'CARD', 'b1', { x: 400, y: 0 }),
      },
    } as never);
    useAgent.setState({
      run: run({
        plan: {
          actions: [action({ seq: 0, kind: 'connect', elementId: 'l1', fromId: 'x', toId: 'y', summary: 'blocks' })],
        } as AgentPlan,
      }),
    });
    html(<GhostLayer />);
    expect(container.querySelectorAll('.ghost-line').length, '"connect these six" previewed as an empty layer').toBe(1);
  });
});

// ---- AX9 ------------------------------------------------------------------
// "A line of text and a rectangle in space should read as the same object" is
// the product's own stated principle, and it was implemented for the mouse and
// for nothing else: hoverSeq had exactly one producer.
describe('the row-to-ghost binding has more than one producer', () => {
  it('focusing a plan row lights its ghost', () => {
    const r = run({
      plan: {
        actions: [
          action({ seq: 0, title: 'Casting' }),
          action({ seq: 1, kind: 'move_element', elementId: 'card-1', title: undefined, text: 'A card', destination: 'Casting', parentId: 'c1' }),
        ],
      } as AgentPlan,
    });
    useAgent.setState({ run: r });
    html(<Decide run={r} />);

    const review = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Review'))!;
    act(() => { review.dispatchEvent(new MouseEvent('click', { bubbles: true })); });

    const row = container.querySelector('.ap-row') as HTMLElement;
    expect(row, 'no plan row rendered').toBeTruthy();
    act(() => { row.dispatchEvent(new FocusEvent('focusin', { bubbles: true })); });
    expect(useAgent.getState().hoverSeq, 'tab to a plan row and nothing lights on the canvas').toBe(0);
  });

  it('a ghost can be reached by focus at all', () => {
    useAgent.setState({
      run: run({ plan: { actions: [action({ position: { x: 0, y: 0, width: 240 } })] } as AgentPlan }),
    });
    html(<GhostLayer />);
    const ghost = container.querySelector('.ghost-el') as HTMLElement;
    expect(ghost.getAttribute('tabindex')).toBe('0');
    act(() => { ghost.dispatchEvent(new FocusEvent('focusin', { bubbles: true })); });
    expect(useAgent.getState().hoverSeq).toBe(0);
  });

  it('the nested-board badge is a control, not a span with a click handler', () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        sub: el('sub', 'BOARD', 'b1', { x: 120, y: 60 }),
      },
    } as never);
    useAgent.setState({
      run: run({
        plan: { actions: [action({ seq: 0, elementId: 'n1', title: 'Inside', parentId: 'sub' })] } as AgentPlan,
      }),
    });
    html(<GhostLayer />);
    const badge = container.querySelector('.ghost-count');
    expect(badge?.tagName, 'the depth-preview pin is unreachable by keyboard').toBe('BUTTON');
    expect(badge?.getAttribute('aria-label')).toContain('+1');
  });
});

// ---- FR11 -----------------------------------------------------------------
// The server has broadcast the stale id list since exact-action binding
// shipped, and no frontend component has ever read a run event. The recovery
// experience was one toast naming nothing, over an Apply button that stays
// enabled and fails identically every time it is pressed.
describe('a stale plan says what changed, and stops offering Apply', () => {
  const staleRun = () => {
    const r = run({
      plan: { actions: [action({ seq: 0, elementId: 'casting', title: 'Casting' })] } as AgentPlan,
    });
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        casting: { ...el('casting', 'CARD', 'b1'), content: { title: 'Casting' } } as QElement,
      },
    } as never);
    useAgent.setState({ run: r, stale: { ids: ['casting'], membership: false } });
    return r;
  };

  it('names the elements a colleague moved', () => {
    const r = staleRun();
    html(<Decide run={r} />);
    expect(container.textContent).toContain('changed on the board while you were reviewing');
    expect(container.textContent, 'the notice names no element').toContain('Casting');
  });

  it('disables Apply rather than offering a button that always fails', () => {
    const r = staleRun();
    html(<Decide run={r} />);
    // The decision bar's Apply specifically. It commits the WHOLE plan, which
    // is the thing that cannot succeed while a row of it is stale — the notice
    // now carries its own Apply-the-rest, which can.
    const apply = container.querySelector('.ac-decide .ac-apply')!;
    expect(apply.hasAttribute('disabled')).toBe(true);
  });

  it('offers to leave out the stale rows and commit the rest', () => {
    const r = staleRun();
    html(<Decide run={r} />);
    const rest = [...container.querySelectorAll('.ac-stale button')]
      .find((b) => b.textContent?.includes('Apply the rest'))!;
    expect(rest, 'the only exits are re-check and re-plan the whole run').toBeTruthy();
    expect(rest.hasAttribute('disabled'), 'the one exit that can work is disabled').toBe(false);
  });

  it('does not offer Apply the rest when the whole board membership moved', () => {
    const r = run({ plan: { actions: [action()] } as AgentPlan });
    useAgent.setState({ run: r, stale: { ids: [], membership: true } });
    html(<Decide run={r} />);
    // No subset of the plan was computed against the board as it now is, so
    // there is no honest "rest" to apply.
    const rest = [...container.querySelectorAll('.ac-stale button')]
      .find((b) => b.textContent?.includes('Apply the rest'));
    expect(rest).toBeFalsy();
  });

  it('offers the two exits that actually work', () => {
    const r = staleRun();
    html(<Decide run={r} />);
    const labels = [...container.querySelectorAll('.ac-stale button')].map((b) => b.textContent);
    expect(labels.join(' ')).toContain('Re-check');
    expect(labels.join(' ')).toContain('Revise');
  });

  it('strikes the rows that no longer describe the board', () => {
    const r = staleRun();
    html(<Decide run={r} />);
    const review = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Review'))!;
    act(() => { review.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(container.querySelector('.ap-row.stale'), 'a stale row reads as ordinary').toBeTruthy();
  });

  it('membership drift, which names no element, still says what happened', () => {
    const r = run({ plan: { actions: [action()] } as AgentPlan });
    useAgent.setState({ run: r, stale: { ids: [], membership: true } });
    html(<Decide run={r} />);
    expect(container.textContent).toContain('gained or lost items');
  });
});

// ---- JN6 ------------------------------------------------------------------
// set_note_text replaces a body wholesale and the review row showed only the
// NEW text: the preview says what will exist, never what will stop existing.
describe('an edit that deletes prose says how much', () => {
  const withExisting = (existing: string, replacement: string) => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        doc: { ...el('doc', 'DOCUMENT', 'b1'), content: { textPreview: existing } } as QElement,
      },
    } as never);
    const r = run({
      plan: {
        actions: [action({ seq: 0, kind: 'set_note_text', elementId: 'doc', title: undefined, text: replacement })],
      } as AgentPlan,
    });
    useAgent.setState({ run: r });
    html(<Decide run={r} />);
    const review = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Review'))!;
    act(() => { review.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
  };

  it('warns when a long body is replaced by a short one', () => {
    withExisting('x'.repeat(480), 'A tighter second act.');
    expect(container.textContent, 'six pages can be deleted behind a one-line row').toContain('replaces');
    expect(container.textContent).toContain('480');
  });

  it('says nothing when the edit grows the note', () => {
    withExisting('x'.repeat(480), 'y'.repeat(900));
    expect(container.querySelector('.ap-row-loss')).toBeNull();
  });

  it('says nothing about a short note', () => {
    withExisting('A line.', 'Another line.');
    expect(container.querySelector('.ap-row-loss')).toBeNull();
  });
});

// ---- FR16 / DL9 -----------------------------------------------------------
describe('a run you can tell is alive', () => {
  const ev = (over: Partial<AgentEvent>): AgentEvent => ({
    id: '', runId: 'r1', sequence: 1, type: 'step.finished', message: 'step 3 — read_board',
    at: new Date().toISOString(), ...over,
  } as AgentEvent);

  it('shows where the run is, which the server has always sent', () => {
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: [ev({ data: { step: 8, calls: 1 } })],
    });
    html(<Working />);
    expect(container.textContent).toContain('step 9');
    expect(container.textContent, 'the denominator is known and was not shown').toContain('of 12');
  });

  it('shows what the run has spent while it is spending it', () => {
    useAgent.setState({
      run: run({ state: 'RUNNING', usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.42, calls: 3 } }),
      events: [ev({ data: { step: 1 } })],
    });
    html(<Working />);
    expect(container.textContent, 'the cost meter is hidden during the window the money is spent').toContain('$0.420');
  });

  it('says so when the run has stopped emitting, and offers to check', () => {
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: [ev({ at: new Date(Date.now() - 90_000).toISOString(), data: { step: 2 } })],
    });
    html(<Working />);
    expect(container.textContent).toContain('No word from the run for');
    const check = [...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Check');
    expect(check, 'a wedged run offers nothing but Stop').toBeTruthy();
    expect(container.querySelector('.dock-pulse.stalled'), 'a dead run keeps pulsing as though thinking').toBeTruthy();
  });

  it('a run that is still emitting is not accused of being silent', () => {
    useAgent.setState({ run: run({ state: 'RUNNING' }), events: [ev({ data: { step: 1 } })] });
    html(<Working />);
    expect(container.textContent).not.toContain('No word from the run');
  });

  // DL9: sending a storyboard or a contract to the model rendered as "Thinking…"
  it('naming a transmission as a transmission', () => {
    expect(phraseKeyFor({ message: 'step 3 — look_at' } as AgentEvent)).toBe('agent.work.looking');
    expect(phraseKeyFor({ message: 'step 4 — read_url' } as AgentEvent)).toBe('agent.work.fetching');
    // And the branch it used to fall through to.
    expect(phraseKeyFor({ message: 'step 5 — set_color' } as AgentEvent)).toBe('agent.work.thinking');
  });
});
