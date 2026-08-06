// FIRST: jsdom has neither matchMedia nor ResizeObserver, and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import type { ReactElement } from 'react';

import { useBoard } from '../store/boardStore';
import { flushSizeReports, useView } from '../store/viewStore';
import { useAgent } from '../agent/agentStore';
import { BoardCanvas } from './BoardCanvas';
import { LineLayer } from './LineLayer';
import { shellRenderCount } from './ElementShell';
import type { Op, QElement } from '../api/types';

// The whole point of these three items is that the code was correct and the
// product was slow, so every assertion here is on a COST — how many renders,
// how many store writes, how large a surface — measured off a real mount in a
// real DOM. A test that read the store's shape would have passed happily
// against the version where memo() was inoperative.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const el = (
  id: string, type: string, parentId: string,
  position = { x: 0, y: 0 }, content: Record<string, unknown> = {},
): QElement => ({
  id, type, content,
  location: { parentId, section: 'CANVAS', position, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const navigate = async () => { /* no navigation in these tests */ };

function render(node: ReactElement) {
  act(() => { root.render(node); });
  return container;
}

/** A board of n cards, laid out in a row. */
function boardOf(n: number): Record<string, QElement> {
  const elements: Record<string, QElement> = { b1: el('b1', 'BOARD', '') };
  for (let i = 0; i < n; i++) {
    elements[`c${i}`] = el(`c${i}`, 'CARD', 'b1', { x: i * 300, y: 0 });
  }
  return elements;
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
    presence: {},
    remoteEditing: {},
    boardStats: {},
    commitTransaction: async (_ops: Op[]) => { /* nothing lands in these tests */ },
  } as never);
  useView.setState({
    drag: null, lineDraft: null, labelFilter: new Set(),
    editingId: null, overlays: [], sizes: {}, panX: 0, panY: 0, scale: 1,
  });
  useAgent.setState({
    run: null, adjustments: [],
    capabilities: { enabled: false, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  });
  flushSizeReports();
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

// ---- SC5 ------------------------------------------------------------------
// ElementShell is wrapped in memo() and then subscribed to the WHOLE board
// store, which made the memo unreachable for any store change at all. Presence
// arrives at 20 Hz per peer and replaces the root state object, so on a large
// board with a few collaborators, pointer movement alone produced tens of
// thousands of component renders a second with nothing on the board changing.
describe('a collaborator pointing at something costs the board nothing', () => {
  it('a remote cursor re-renders zero cards', () => {
    useBoard.setState({ elements: boardOf(12) } as never);
    render(<BoardCanvas navigate={navigate} />);
    expect(container.querySelectorAll('.el').length, 'no cards mounted').toBe(12);

    const before = shellRenderCount();
    // Exactly what socket.ts does on a `presence.cursor` frame.
    act(() => {
      useBoard.getState().upsertPresence({
        clientId: 'peer-1', sub: 'sara', name: 'Sara', cursor: { x: 10, y: 10 },
      });
    });
    act(() => {
      useBoard.getState().upsertPresence({
        clientId: 'peer-1', sub: 'sara', name: 'Sara', cursor: { x: 40, y: 60 },
      });
    });
    expect(
      shellRenderCount() - before,
      'a peer moving their mouse reconciles every card on the board',
    ).toBe(0);
    // And the cursor itself did arrive — a zero that came from nothing
    // happening at all would prove nothing.
    expect(container.querySelector('.remote-cursor')).toBeTruthy();
  });

  it('a peer opening an editor re-renders only the card they opened', () => {
    useBoard.setState({ elements: boardOf(12) } as never);
    render(<BoardCanvas navigate={navigate} />);

    const before = shellRenderCount();
    act(() => { useBoard.getState().setRemoteEditing('c3', 'Sara', true); });
    expect(shellRenderCount() - before, 'setRemoteEditing re-rendered the whole board').toBe(1);
    expect(container.querySelector('.el.remote-edit')).toBeTruthy();
  });

  it('dragging one card re-renders one card', () => {
    useBoard.setState({ elements: boardOf(12) } as never);
    render(<BoardCanvas navigate={navigate} />);

    const before = shellRenderCount();
    act(() => { useView.getState().setDrag({ ids: ['c5'], dx: 4, dy: 4 }); });
    act(() => { useView.getState().setDrag({ ids: ['c5'], dx: 9, dy: 12 }); });
    // Two frames of one card: the shell that is moving, and nothing else. The
    // store used to hand every shell a fresh `drag` object per pointermove.
    expect(shellRenderCount() - before, 'a one-card drag reconciled the board').toBe(2);
  });
});

// ---- SC18 -----------------------------------------------------------------
// reportSize did `set(s => ({ sizes: {...s.sizes, [id]: box} }))` per shell, so
// mounting 1,000 elements copied 0, 1, 2 … 999 keys — half a million key copies
// and a thousand discarded objects — at exactly the moment the person is
// waiting for the board to paint, each one re-rendering the line layer.
describe('measuring a board costs one write, not one per element', () => {
  it('mounting N shells produces O(frames) writes to sizes, not O(N)', () => {
    let writes = 0;
    let last = useView.getState().sizes;
    const stop = useView.subscribe((s) => {
      if (s.sizes !== last) { last = s.sizes; writes += 1; }
    });

    useBoard.setState({ elements: boardOf(40) } as never);
    render(<BoardCanvas navigate={navigate} />);
    act(() => { flushSizeReports(); });
    stop();

    expect(container.querySelectorAll('.el').length).toBe(40);
    expect(writes, '40 mounted shells wrote to the size map 40 times').toBeLessThanOrEqual(2);
    expect(writes, 'nothing was recorded at all').toBeGreaterThan(0);
    expect(Object.keys(useView.getState().sizes).length).toBe(40);
  });

  it('a report that lands and reverts inside one frame does not leak the middle value', () => {
    const report = useView.getState().reportSize;
    act(() => { report('c0', 300, 100); flushSizeReports(); });
    expect(useView.getState().sizes.c0).toEqual({ w: 300, h: 100 });

    // Grow then shrink back before the frame flushes: the buffered 480 must not
    // survive as though it were the settled size.
    act(() => {
      report('c0', 480, 100);
      report('c0', 300, 100);
      flushSizeReports();
    });
    expect(useView.getState().sizes.c0).toEqual({ w: 300, h: 100 });
  });
});

// ---- SC17 -----------------------------------------------------------------
// The layer mounted a 200,000 × 200,000 pixel SVG — four orders of magnitude
// past any viewport — and a SECOND identical one whenever a line was selected.
describe('the connector surface is the size of the connectors', () => {
  const twoLines = () => ({
    b1: el('b1', 'BOARD', ''),
    a: el('a', 'CARD', 'b1', { x: 0, y: 0 }),
    b: el('b', 'CARD', 'b1', { x: 400, y: 0 }),
    l1: el('l1', 'LINE', 'b1', { x: 0, y: 0 }, { fromId: 'a', toId: 'b', endArrow: true }),
  });

  it('is bounded by the lines it draws', () => {
    useBoard.setState({ elements: twoLines() } as never);
    useView.setState({ sizes: { a: { w: 260, h: 120 }, b: { w: 260, h: 120 } } });
    render(<LineLayer />);

    const svg = container.querySelector('svg')!;
    const width = Number(svg.getAttribute('width'));
    expect(svg, 'the line layer drew nothing').toBeTruthy();
    // The whole diagram spans ~530px of canvas; anything near 200,000 is the
    // old fixed extent.
    expect(width).toBeGreaterThan(0);
    expect(width, 'the surface is still a virtual canvas rather than a diagram').toBeLessThan(2_000);
  });

  it('selecting a line does not lay a second full-canvas surface over the board', () => {
    useBoard.setState({ elements: twoLines(), selection: new Set(['l1']) } as never);
    useView.setState({ sizes: { a: { w: 260, h: 120 }, b: { w: 260, h: 120 } } });
    render(<LineLayer />);

    // Direct children only: the selected-line toolbar is full of icon <svg>s.
    const surfaces = [...container.querySelectorAll(':scope > svg')];
    expect(surfaces.length, 'the handle overlay did not render').toBe(2);
    for (const svg of surfaces) {
      expect(Number(svg.getAttribute('width'))).toBeLessThan(2_000);
      expect(Number(svg.getAttribute('height'))).toBeLessThan(2_000);
    }
  });

  it('still draws the line where the line is', () => {
    useBoard.setState({ elements: twoLines() } as never);
    useView.setState({ sizes: { a: { w: 260, h: 120 }, b: { w: 260, h: 120 } } });
    render(<LineLayer />);

    // Two paths per line (the fat invisible hit target and the stroke itself).
    // The quadratic is the tell — the arrowhead marker is also a <path>.
    const paths = [...container.querySelectorAll('path')].filter((p) => p.getAttribute('d')?.includes(' Q '));
    expect(paths.length, 'sizing the surface lost the connector').toBeGreaterThanOrEqual(2);
    // The visible stroke runs between the two cards' centres, trimmed to their
    // edges: it must start left of where the second card begins.
    const d = paths[0].getAttribute('d')!;
    const startX = Number(d.split(' ')[1]);
    expect(startX).toBeGreaterThan(0);
    expect(startX).toBeLessThan(400);
  });

  it('a board whose only connector points at a deleted card still renders', () => {
    useBoard.setState({
      elements: {
        b1: el('b1', 'BOARD', ''),
        l1: el('l1', 'LINE', 'b1', { x: 0, y: 0 }, { fromId: 'gone', toId: 'alsoGone' }),
      },
    } as never);
    expect(() => render(<LineLayer />)).not.toThrow();
    const svg = container.querySelector('svg');
    // An unbounded box would serialize as width="Infinity", which is a
    // rendering fault rather than an empty layer.
    if (svg) expect(Number.isFinite(Number(svg.getAttribute('width')))).toBe(true);
  });
});
