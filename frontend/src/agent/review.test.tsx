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
import { Decide } from './AgentDecide';
import { Done } from './AgentDone';
import { GhostLayer as GhostLayerUnderTest, rollUpDestinations } from './GhostLayer';
import { Working as WorkingUnderTest } from './AgentWorking';
import type { AgentAction, AgentPlan, AgentRun, QElement } from '../api/types';

// Navigating a board moves the realtime room with it, and there is no server
// here to move it to — a real connect would leave a reconnect timer running
// past the end of the suite.
vi.mock('../realtime/socket', () => ({
  connectBoard: async () => { /* no room in tests */ },
  disconnect: () => { /* nothing to close */ },
  sendCursor: () => { /* nothing to send */ },
  sendEditing: () => { /* nothing to send */ },
}));

// CV15, CV16 and LP3 are all one complaint from three directions: the decision
// surface hands a person a flat list and one button, and every mechanism the
// product relies on for safety assumes they read it. The assertions are on what
// is RENDERED, because the failure was never a wrong value — it was forty
// correct rows nobody could get through.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const el = (id: string, type: string, parentId: string, content: Record<string, unknown> = {}): QElement => ({
  id, type, content,
  location: { parentId, section: 'CANVAS', position: { x: 0, y: 0 }, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const action = (over: Partial<AgentAction> = {}): AgentAction => ({
  seq: 0, kind: 'create_note', elementId: 'n0', text: 'A card', summary: 'A card', ...over,
});

const run = (over: Partial<AgentRun> = {}): AgentRun => ({
  id: 'r1', state: 'PROPOSED',
  task: { intent: 'Organize', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0, calls: 1 },
  createdAt: '', updatedAt: '',
  ...over,
} as unknown as AgentRun);

function render(node: ReactElement) {
  act(() => { root.render(node); });
  return container;
}

/** Open the line-by-line list, which is one click away by design. */
function openReview() {
  const review = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Review'))!;
  act(() => { review.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
}

const chip = (label: string) =>
  [...container.querySelectorAll('.ac-plan-chip')].find((b) => b.textContent?.startsWith(label)) as HTMLElement;

const rowTexts = () => [...container.querySelectorAll('.ap-row')].map((r) => r.textContent ?? '');

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);

  useBoard.setState({
    boardId: 'b1', elements: { b1: el('b1', 'BOARD', '') },
    selection: new Set(), readOnly: false,
  } as never);
  useView.setState({ sizes: {}, overlays: [], drag: null, focusedId: null });
  useAgent.setState({
    run: null, events: [], adjustments: [], hoverSeq: null, tileFocus: null,
    recent: [], audit: [], drift: null, reach: null, active: null, stale: null,
    observedStaged: 0, busy: false, open: false,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 40, maxSteps: 12 } },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

// ---- CV15 -----------------------------------------------------------------
describe('the review list can be narrowed by kind', () => {
  const mixed = () => {
    const r = run({
      plan: {
        actions: [
          action({ seq: 0, kind: 'create_note', elementId: 'n1', text: 'Brand new' }),
          action({ seq: 1, kind: 'move_element', elementId: 'old-1', text: 'Something I placed', destination: 'Casting', parentId: 'col' }),
          action({ seq: 2, kind: 'delete_element', elementId: 'old-2', text: 'The contract' }),
        ],
      } as AgentPlan,
    });
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    return r;
  };

  it('offers the four questions a reviewer actually has', () => {
    mixed();
    const labels = [...container.querySelectorAll('.ac-plan-chip')].map((b) => b.textContent?.trim());
    expect(labels.join(' ')).toContain('All');
    expect(labels.join(' ')).toContain('New');
    expect(labels.join(' ')).toContain('Moves');
    expect(labels.join(' ')).toContain('Deletes');
  });

  it('starts on Deletes when the plan deletes anything', () => {
    mixed();
    // The warn banner already knows to raise the alarm and then handed over an
    // unfiltered list, which is the same as not raising it.
    expect(chip('Deletes').getAttribute('aria-pressed')).toBe('true');
    expect(rowTexts().join(' ')).toContain('The contract');
    expect(rowTexts().join(' ')).not.toContain('Brand new');
  });

  it('narrows to what the person asked for', () => {
    mixed();
    act(() => { chip('Moves').dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(rowTexts().join(' ')).toContain('Something I placed');
    expect(rowTexts().join(' ')).not.toContain('Brand new');

    act(() => { chip('All').dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(rowTexts()).toHaveLength(3);
  });

  it('starts on All when nothing is destructive', () => {
    const r = run({
      plan: { actions: [action({ seq: 0, text: 'Just a card' })] } as AgentPlan,
    });
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    expect(chip('All').getAttribute('aria-pressed')).toBe('true');
  });
});

// ---- LP3 ------------------------------------------------------------------
describe('review is proportional to risk', () => {
  /** A plan the size the product is heading toward: mostly new cards, with a
   *  handful of changes to material the person already placed. */
  const bigPlan = (): AgentRun => {
    const actions: AgentAction[] = [];
    for (let i = 0; i < 20; i++) {
      actions.push(action({ seq: i, kind: 'create_note', elementId: `new-${i}`, text: `New card ${i}` }));
    }
    actions.push(action({ seq: 20, kind: 'move_element', elementId: 'placed', text: 'Something I placed' }));
    actions.push(action({ seq: 21, kind: 'create_column', elementId: 'col-x', title: 'A new column' }));
    return run({ plan: { actions } as AgentPlan });
  };

  it('collapses the additive bulk and shows the surprising rows', () => {
    const r = bigPlan();
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', ''), placed: el('placed', 'CARD', 'b1') } } as never);
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();

    const rows = rowTexts();
    expect(rows.length, '22 rows is not a review, it is a wall').toBeLessThan(10);
    // The two that are not simply additive are both present.
    expect(rows.join(' '), 'a change to existing work was collapsed').toContain('Something I placed');
    expect(rows.join(' '), 'a new container that will hold existing work was collapsed').toContain('A new column');
    expect(container.textContent, 'the collapse does not say what it hid').toContain('more new items');
  });

  it('promotes a sample of the collapsed rows so the reviewer stays calibrated', () => {
    const r = bigPlan();
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    const shownNew = rowTexts().filter((x) => x.includes('New card')).length;
    expect(shownNew, 'nothing additive is ever seen, so nobody notices the day it stops being additive')
      .toBeGreaterThan(0);
  });

  it('never collapses a delete, whatever the plan looks like', () => {
    const actions: AgentAction[] = [];
    for (let i = 0; i < 20; i++) {
      actions.push(action({ seq: i, kind: 'create_note', elementId: `new-${i}`, text: `New card ${i}` }));
    }
    actions.push(action({ seq: 20, kind: 'delete_element', elementId: 'gone', text: 'The contract' }));
    const r = run({ plan: { actions } as AgentPlan });
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    act(() => { chip('All').dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(rowTexts().join(' ')).toContain('The contract');
  });

  it('opens the whole list when asked', () => {
    const r = bigPlan();
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    const more = [...container.querySelectorAll('.ac-plan-more button')][0] as HTMLElement;
    expect(more).toBeTruthy();
    act(() => { more.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    expect(rowTexts()).toHaveLength(22);
  });

  it('leaves a short plan entirely alone', () => {
    const r = run({
      plan: {
        actions: [0, 1, 2].map((i) => action({ seq: i, elementId: `n${i}`, text: `Card ${i}` })),
      } as AgentPlan,
    });
    useAgent.setState({ run: r });
    render(<Decide run={r} />);
    openReview();
    // Collapsing three rows behind a "+3" is theatre.
    expect(rowTexts()).toHaveLength(3);
  });
});

// ---- CV16 -----------------------------------------------------------------
describe('the outcome card takes you to the work', () => {
  const filedRun = () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        sub: el('sub', 'BOARD', 'b1', { title: 'Pre-Production' }),
        col: el('col', 'COLUMN', 'sub'),
      },
    } as never);
    return run({
      state: 'COMPLETED',
      plan: {
        actions: [
          action({ seq: 0, kind: 'create_note', elementId: 'n1', text: 'Assembly cut', parentId: 'col' }),
          action({ seq: 1, kind: 'create_note', elementId: 'n2', text: 'Colour grade', parentId: 'col' }),
          action({ seq: 2, kind: 'create_note', elementId: 'n3', text: 'On the root', parentId: 'b1' }),
        ],
      } as AgentPlan,
    });
  };

  it('names each sub-board the run wrote into, and how much went there', () => {
    const r = filedRun();
    useAgent.setState({ run: r });
    render(<Done run={r} />);
    const dests = [...container.querySelectorAll('.ac-destination')].map((b) => b.textContent);
    expect(dests.length, 'a run that filled a sub-board finished by showing an unchanged tile').toBe(1);
    expect(dests[0]).toContain('Pre-Production');
    expect(dests[0]).toContain('2');
  });

  it('navigates there and selects what the run put there', async () => {
    const r = filedRun();
    useAgent.setState({ run: r });
    const opened: string[] = [];
    useBoard.setState({
      openBoard: async (id: string) => {
        opened.push(id);
        useBoard.setState({
          boardId: id,
          elements: {
            sub: el('sub', 'BOARD', 'b1', { title: 'Pre-Production' }),
            col: el('col', 'COLUMN', 'sub'),
            n1: el('n1', 'CARD', 'col'),
            n2: el('n2', 'CARD', 'col'),
          },
        } as never);
      },
    } as never);
    render(<Done run={r} />);
    const row = container.querySelector('.ac-destination') as HTMLElement;
    await act(async () => {
      row.dispatchEvent(new MouseEvent('click', { bubbles: true }));
      await new Promise((res) => setTimeout(res, 20));
    });

    expect(opened, 'the row goes nowhere').toEqual(['sub']);
    expect([...useBoard.getState().selection].sort(), 'you arrive on a board that looks unchanged')
      .toEqual(['n1', 'n2']);
  });

  it('says nothing when everything landed on this canvas', () => {
    useBoard.setState({ elements: { b1: el('b1', 'BOARD', '') } } as never);
    const r = run({
      state: 'COMPLETED',
      plan: { actions: [action({ seq: 0, elementId: 'n1', text: 'Right here', parentId: 'b1' })] } as AgentPlan,
    });
    useAgent.setState({ run: r });
    render(<Done run={r} />);
    expect(container.querySelector('.ac-destination')).toBeNull();
  });
});

// ---- IN12 -----------------------------------------------------------------
// A run was an opaque wait followed by a wall of ghosts. The data was already
// on the wire: `action.staged` has carried seq, kind, elementId and parentId
// since staging shipped, and nothing on the canvas ever read one.
describe('the canvas shows the plan forming, not only the finished wall', () => {
  const staging = (parentIds: string[]) => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        archive: el('archive', 'BOARD', 'b1', { title: 'Archive' }),
        col: el('col', 'COLUMN', 'archive'),
      },
    } as never);
    useAgent.setState({
      run: run({ state: 'RUNNING' }),
      events: parentIds.map((parentId, i) => ({
        id: '', runId: 'r1', sequence: i, type: 'action.staged',
        message: `Note ${i}`, at: '',
        data: { seq: i, kind: 'create_note', elementId: `n${i}`, parentId },
      })) as never,
    });
  };

  it('badges the destination as each change is staged, before any plan exists', () => {
    staging(['col', 'col', 'col']);
    render(<GhostLayerUnderTest />);
    const badge = container.querySelector('.ghost-badge.streaming');
    expect(badge, 'the wait says nothing about where the run is putting things').toBeTruthy();
    expect(badge!.textContent, 'the count is not climbing with the run').toContain('+3');
    // The badge sits on the Archive tile, so the tile itself carries the name
    // visually; the control has to say it too or the only people who can act on
    // the early warning are the ones who can see the canvas.
    expect(container.querySelector('.ghost-count')?.getAttribute('aria-label')).toContain('Archive');
  });

  it('says nothing while the run is only reading', () => {
    staging([]);
    render(<GhostLayerUnderTest />);
    expect(container.querySelector('.ghost-badge')).toBeNull();
  });

  it('and does not draw a streaming badge once the plan is up for review', () => {
    staging(['col']);
    useAgent.setState({ run: run({ state: 'PROPOSED', plan: { actions: [] } as AgentPlan }) });
    render(<GhostLayerUnderTest />);
    expect(container.querySelector('.ghost-badge.streaming')).toBeNull();
  });
});

// ---- LP7 ------------------------------------------------------------------
// A person who typed "no, not the archive" watched the identical spinner,
// could not tell whether they were heard, typed it again, and had the third
// repetition refused by a cap they had never been shown.
describe('a steer is visibly received', () => {
  const withSteers = (events: unknown[]) => {
    useAgent.setState({ run: run({ state: 'RUNNING' }), events: events as never });
    render(<WorkingUnderTest />);
  };

  it('shows what you have already said to this run', () => {
    withSteers([
      { id: '', runId: 'r1', sequence: 1, type: 'run.steered', message: 'steered mid-run', at: '', data: { note: 'no, not the archive' } },
    ]);
    expect(container.textContent, 'a steer left no trace anywhere on screen').toContain('no, not the archive');
    expect(container.querySelector('.ac-steer-chip')?.textContent).toContain('Queued');
  });

  it('marks it heard once the run says it was delivered', () => {
    withSteers([
      { id: '', runId: 'r1', sequence: 1, type: 'run.steered', at: '', data: { note: 'use four columns' } },
      { id: '', runId: 'r1', sequence: 2, type: 'run.steer.delivered', at: '', data: { note: 'use four columns' } },
    ]);
    expect(container.querySelector('.ac-steer-chip.delivered')).toBeTruthy();
    expect(container.textContent).toContain('Heard');
  });

  it('claims no delivery a deployment has not reported', () => {
    // Queued is true and provable; "heard" would be a guess about the model.
    withSteers([
      { id: '', runId: 'r1', sequence: 1, type: 'run.steered', at: '', data: { note: 'stop at three' } },
    ]);
    expect(container.querySelector('.ac-steer-chip.delivered')).toBeNull();
  });

  it('nothing at all when nothing has been said', () => {
    withSteers([]);
    expect(container.querySelector('.ac-steers')).toBeNull();
  });
});

// The roll-up on its own: the preview badge and the outcome row are one claim,
// so they had better be one function.
describe('rolling changes up to the tile they land in', () => {
  it('counts through a column inside the sub-board, not just direct children', () => {
    const elements = {
      b1: el('b1', 'BOARD', ''),
      sub: el('sub', 'BOARD', 'b1', { title: 'Casting' }),
      col: el('col', 'COLUMN', 'sub'),
    } as Record<string, QElement>;
    const out = rollUpDestinations([
      action({ seq: 0, elementId: 'n1', parentId: 'col' }),
      action({ seq: 1, elementId: 'n2', parentId: 'col' }),
      action({ seq: 2, elementId: 'n3', parentId: 'b1' }),
    ], elements, 'b1');
    expect(out).toHaveLength(1);
    expect(out[0].label).toBe('Casting');
    expect(out[0].seqs).toEqual([0, 1]);
    expect(out[0].elementIds).toEqual(['n1', 'n2']);
  });

  it('resolves through a container the same plan is about to create', () => {
    const elements = {
      b1: el('b1', 'BOARD', ''),
      sub: el('sub', 'BOARD', 'b1', { title: 'Sound' }),
    } as Record<string, QElement>;
    const out = rollUpDestinations([
      action({ seq: 0, kind: 'create_column', elementId: 'newcol', title: 'Mix', parentId: 'sub' }),
      action({ seq: 1, elementId: 'n1', parentId: 'newcol' }),
    ], elements, 'b1');
    expect(out).toHaveLength(1);
    expect(out[0].seqs).toEqual([0, 1]);
  });
});
