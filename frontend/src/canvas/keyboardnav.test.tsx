// FIRST: jsdom has neither matchMedia nor ResizeObserver, and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { useAgent } from '../agent/agentStore';
import { BoardCanvas } from './BoardCanvas';
import { nextInDirection, readingOrder, type NavBox } from './spatialNav';
import type { Op, QElement } from '../api/types';

// AX1. Selection is the precondition for the action bar, the resize handle, the
// connect anchor and the whole context menu, and it was reachable only through
// onPointerDown or Ctrl+A. The last wave answered that with tabIndex={0} on
// every shell and said so — a 200-card board became 200 tab stops, and the
// question a person actually has on a canvas ("the card to the right of this
// one") still had no key that answered it.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;
let committed: Op[][];

const el = (id: string, type: string, parentId: string, position = { x: 0, y: 0 }): QElement => ({
  id, type, content: {},
  location: { parentId, section: 'CANVAS', position, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const navigate = async () => { /* no navigation in these tests */ };

function render(node: ReactElement) {
  act(() => { root.render(node); });
  return container;
}

/**
 * jsdom has no layout, so every getBoundingClientRect is a zero rect and the
 * spatial grid would have nothing to be spatial about. Each shell is given the
 * rect its stored position implies — which is what the browser would compute.
 */
function layOutShells() {
  for (const node of container.querySelectorAll<HTMLElement>('.el[data-element-id]')) {
    const id = node.getAttribute('data-element-id')!;
    const pos = useBoard.getState().elements[id]?.location.position ?? { x: 0, y: 0 };
    node.getBoundingClientRect = () => ({
      left: pos.x, top: pos.y, right: pos.x + 260, bottom: pos.y + 120,
      width: 260, height: 120, x: pos.x, y: pos.y, toJSON: () => ({}),
    }) as DOMRect;
  }
}

/** Four cards in a 2×2 grid: c-tl c-tr / c-bl c-br. */
function grid() {
  return {
    b1: el('b1', 'BOARD', ''),
    'c-tl': el('c-tl', 'CARD', 'b1', { x: 0, y: 0 }),
    'c-tr': el('c-tr', 'CARD', 'b1', { x: 400, y: 0 }),
    'c-bl': el('c-bl', 'CARD', 'b1', { x: 0, y: 300 }),
    'c-br': el('c-br', 'CARD', 'b1', { x: 400, y: 300 }),
  };
}

function press(key: string, opts: Partial<KeyboardEventInit> = {}) {
  const active = document.activeElement as HTMLElement;
  act(() => {
    active.dispatchEvent(new KeyboardEvent('keydown', {
      key, bubbles: true, cancelable: true, ...opts,
    }));
  });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  committed = [];

  useBoard.setState({
    boardId: 'b1', elements: grid(), selection: new Set(), readOnly: false,
    presence: {}, remoteEditing: {}, boardStats: {},
    commitTransaction: async (ops: Op[]) => { committed.push(ops); },
  } as never);
  useView.setState({
    drag: null, lineDraft: null, labelFilter: new Set(), editingId: null,
    overlays: [], sizes: {}, panX: 0, panY: 0, scale: 1, focusedId: null,
  });
  useAgent.setState({
    run: null, adjustments: [],
    capabilities: { enabled: false, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

describe('the board is one tab stop, not two hundred', () => {
  it('exactly one card is in the tab order', () => {
    render(<BoardCanvas navigate={navigate} />);
    const stops = [...container.querySelectorAll('.el[data-element-id]')]
      .filter((n) => n.getAttribute('tabindex') === '0');
    expect(stops.length, 'every card is its own tab stop').toBe(1);
    // And the rest are still reachable — by an arrow, not by forty Tabs.
    const reachable = [...container.querySelectorAll('.el[data-element-id]')]
      .filter((n) => n.getAttribute('tabindex') !== null);
    expect(reachable.length).toBe(4);
  });

  it('the tab stop follows focus, so Tab out and back returns where you were', () => {
    render(<BoardCanvas navigate={navigate} />);
    const br = container.querySelector<HTMLElement>('.el[data-element-id="c-br"]')!;
    act(() => { br.focus(); });
    expect(useView.getState().focusedId).toBe('c-br');
    expect(br.getAttribute('tabindex')).toBe('0');
  });

  it('a deleted focus target hands the tab stop to something that exists', () => {
    render(<BoardCanvas navigate={navigate} />);
    act(() => { useView.getState().setFocused('c-br'); });

    const remaining = { ...grid() };
    delete (remaining as Record<string, QElement>)['c-br'];
    act(() => { useBoard.setState({ elements: remaining } as never); });

    const focused = useView.getState().focusedId;
    expect(focused, 'the canvas dropped out of the tab order entirely').toBeTruthy();
    expect(focused).not.toBe('c-br');
  });
});

describe('arrows move between cards the way the eye does', () => {
  const start = (id: string) => {
    render(<BoardCanvas navigate={navigate} />);
    layOutShells();
    const node = container.querySelector<HTMLElement>(`.el[data-element-id="${id}"]`)!;
    act(() => { node.focus(); });
  };

  it('right goes to the card on the right, not the next one in the element map', () => {
    start('c-tl');
    press('ArrowRight');
    expect(document.activeElement?.getAttribute('data-element-id')).toBe('c-tr');
  });

  it('down goes to the card below', () => {
    start('c-tl');
    press('ArrowDown');
    expect(document.activeElement?.getAttribute('data-element-id')).toBe('c-bl');
  });

  it('stays put at the edge rather than teleporting across the board', () => {
    start('c-tl');
    press('ArrowLeft');
    expect(document.activeElement?.getAttribute('data-element-id')).toBe('c-tl');
  });

  it('End reaches the far corner of a wide board in one key', () => {
    start('c-tl');
    press('End');
    expect(document.activeElement?.getAttribute('data-element-id')).toBe('c-br');
    press('Home');
    expect(document.activeElement?.getAttribute('data-element-id')).toBe('c-tl');
  });

  it('Space selects — the precondition for every other verb on a card', () => {
    start('c-tr');
    press(' ');
    expect([...useBoard.getState().selection]).toEqual(['c-tr']);
    press(' ');
    expect([...useBoard.getState().selection], 'Space is a toggle, and did not toggle back').toEqual([]);
  });
});

describe('the keyboard can move a card, by command rather than by pixel', () => {
  it('Ctrl+arrow nudges by the grid step, through the ordinary transaction path', () => {
    render(<BoardCanvas navigate={navigate} />);
    layOutShells();
    const node = container.querySelector<HTMLElement>('.el[data-element-id="c-tl"]')!;
    act(() => { node.focus(); });

    press('ArrowRight', { ctrlKey: true });
    expect(committed.length, 'Ctrl+arrow wrote nothing').toBe(1);
    expect(committed[0][0].changes?.location.position).toEqual({ x: 20, y: 0 });
    // Undoable and syncable like any other edit: it is a move op, not a
    // direct write into the store.
    expect(committed[0][0].action).toBe('move');
    expect(committed[0][0].undoChanges?.location.position).toEqual({ x: 0, y: 0 });
  });

  it('a locked card is not nudged', () => {
    const locked = { ...grid() };
    locked['c-tl'] = { ...locked['c-tl'], content: { locked: true } };
    useBoard.setState({ elements: locked } as never);
    render(<BoardCanvas navigate={navigate} />);
    layOutShells();
    act(() => { container.querySelector<HTMLElement>('.el[data-element-id="c-tl"]')!.focus(); });

    press('ArrowLeft', { ctrlKey: true });
    expect(committed.length, 'a locked card moved from the keyboard').toBe(0);
  });

  it('a viewer cannot nudge anything', () => {
    useBoard.setState({ readOnly: true } as never);
    render(<BoardCanvas navigate={navigate} />);
    layOutShells();
    act(() => { container.querySelector<HTMLElement>('.el[data-element-id="c-tl"]')!.focus(); });

    press('ArrowDown', { ctrlKey: true });
    expect(committed.length).toBe(0);
  });
});

describe('the canvas says what it is', () => {
  it('announces as a board canvas with keys, not as an unnamed application', () => {
    render(<BoardCanvas navigate={navigate} />);
    const viewport = container.querySelector('.canvas-viewport')!;
    expect(viewport.getAttribute('role')).toBe('application');
    expect(viewport.getAttribute('aria-roledescription')).toContain('board');
    expect(viewport.getAttribute('aria-label')).toContain('Arrow keys');
  });
});

// The geometry on its own, where the answers are exact.
describe('choosing the card in a direction', () => {
  const boxes: NavBox[] = [
    { id: 'a', x: 0, y: 0, w: 200, h: 100 },
    { id: 'b', x: 400, y: 10, w: 200, h: 100 },
    { id: 'c', x: 380, y: 600, w: 200, h: 100 }, // right, but far below
    { id: 'd', x: 0, y: 300, w: 200, h: 100 },
  ];

  it('prefers the card along the row over the nearer one off it', () => {
    expect(nextInDirection(boxes, 'a', 'right')).toBe('b');
  });

  it('answers nothing when there is nothing that way', () => {
    expect(nextInDirection(boxes, 'a', 'up')).toBeNull();
  });

  it('falls back to the first card when the origin is gone', () => {
    expect(nextInDirection(boxes, 'vanished', 'right')).toBe('a');
  });

  it('reads in bands, so a card twenty pixels lower is still on the same row', () => {
    const order = readingOrder(boxes).map((b) => b.id);
    expect(order.slice(0, 2)).toEqual(['a', 'b']);
    expect(order[order.length - 1]).toBe('c');
  });
});
