// FIRST: matchMedia and ResizeObserver do not exist in jsdom and this app reads
// both at module scope. Import order is the mechanism — see the file.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { useAgent } from '../agent/agentStore';
import { useSettings } from '../store/settingsStore';
import { BoardCanvas } from './BoardCanvas';
import type { Op, QElement } from '../api/types';

// MO1 / MO2 / MO4 — what only breaks on a device.
//
// These three are one story. `touch-action: none` on the viewport is an ACTIVE
// instruction to suppress the browser's own pan and pinch, and nothing replaced
// them; the one gesture that did work under touch was rubber-band selection,
// bound to the exact motion every touch user makes to look around; and nothing
// anywhere registered `pointercancel`, so any gesture the system took over left
// the canvas stuck until reload.
//
// Every assertion is on what the handlers DO with a pointer event, because that
// is where the defects were. None of them is visible to a test that reads state.

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;
let committed: Op[][];

const el = (id: string, type: string, parentId: string, x = 0, y = 0): QElement => ({
  id, type, content: {},
  location: { parentId, section: 'CANVAS', position: { x, y }, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

const navigate = async () => { /* no navigation in these tests */ };

/** A PointerEvent jsdom will dispatch. jsdom has no PointerEvent constructor. */
function pointer(type: string, init: { id?: number; x?: number; y?: number; pointerType?: string; button?: number }) {
  const ev = new MouseEvent(type, {
    bubbles: true, cancelable: true,
    clientX: init.x ?? 0, clientY: init.y ?? 0, button: init.button ?? 0,
  });
  Object.defineProperty(ev, 'pointerId', { value: init.id ?? 1 });
  Object.defineProperty(ev, 'pointerType', { value: init.pointerType ?? 'touch' });
  return ev;
}

function viewport(): HTMLElement {
  return container.querySelector('.canvas-viewport') as HTMLElement;
}

function send(type: string, init: Parameters<typeof pointer>[1]) {
  act(() => { viewport().dispatchEvent(pointer(type, init)); });
}

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  committed = [];

  useBoard.setState({
    boardId: 'b1',
    elements: { b1: el('b1', 'BOARD', ''), c1: el('c1', 'CARD', 'b1', 40, 40) },
    selection: new Set(),
    presence: {},
    readOnly: false,
    remoteEditing: {},
    boardStats: {},
    commitTransaction: async (ops: Op[]) => { committed.push(ops); },
  } as never);
  useView.setState({
    panX: 0, panY: 0, scale: 1,
    drag: null, labelFilter: new Set(), editingId: null, overlays: [],
    sizes: {}, focusedId: null, drawMode: false, marqueeMode: false,
  });
  useAgent.setState({ run: null, adjustments: [], capabilities: null });
  act(() => { root.render(<BoardCanvas navigate={navigate} />); });
  // getBoundingClientRect is all zeros in jsdom, which is fine: every assertion
  // here is about deltas and about what did or did not get committed.
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

// ---- MO1 ------------------------------------------------------------------

describe('one finger on empty canvas pans', () => {
  it('moves the view instead of drawing a selection box', () => {
    send('pointerdown', { x: 100, y: 100 });
    send('pointermove', { x: 160, y: 130 });
    const v = useView.getState();
    expect({ x: v.panX, y: v.panY }, 'a phone could reach only the region the last desktop session left')
      .toEqual({ x: 60, y: 30 });
    expect(container.querySelector('.marquee'), 'the pan gesture drew a marquee').toBeNull();
  });

  it('and a mouse keeps rubber-band select exactly as it was', () => {
    send('pointerdown', { x: 10, y: 10, pointerType: 'mouse' });
    send('pointermove', { x: 90, y: 90, pointerType: 'mouse' });
    expect(container.querySelector('.marquee'), 'desktop marquee regressed').not.toBeNull();
    expect(useView.getState().panX, 'a mouse drag panned the canvas').toBe(0);
  });

  it('two fingers pinch about their midpoint', () => {
    send('pointerdown', { id: 1, x: 100, y: 100 });
    send('pointerdown', { id: 2, x: 200, y: 100 });
    // Fingers spread from 100px apart to 200px apart: twice the scale.
    send('pointermove', { id: 2, x: 300, y: 100 });
    expect(useView.getState().scale).toBeCloseTo(2, 5);
  });

  it('and the second finger abandons whatever the first started', () => {
    act(() => { useView.getState().setMarqueeMode(true); });
    send('pointerdown', { id: 1, x: 20, y: 20 });
    send('pointermove', { id: 1, x: 80, y: 80 });
    expect(container.querySelector('.marquee')).not.toBeNull();
    send('pointerdown', { id: 2, x: 200, y: 200 });
    expect(container.querySelector('.marquee'), 'a half-drawn marquee committed as the pinch began').toBeNull();
  });
});

// ---- MO2 ------------------------------------------------------------------

describe('marquee on touch is a mode, not the pan gesture', () => {
  it('is off by default, so dragging navigates rather than selecting thirty cards', () => {
    expect(useView.getState().marqueeMode).toBe(false);
  });

  it('draws the box once it is turned on', () => {
    act(() => { useView.getState().setMarqueeMode(true); });
    send('pointerdown', { x: 10, y: 10 });
    send('pointermove', { x: 90, y: 90 });
    expect(container.querySelector('.marquee')).not.toBeNull();
    expect(useView.getState().panX, 'marquee mode also panned').toBe(0);
  });

  it('and turning it on turns Draw off — one finger cannot do both', () => {
    act(() => { useView.getState().setDrawMode(true); });
    act(() => { useView.getState().setMarqueeMode(true); });
    expect(useView.getState().drawMode).toBe(false);
  });

  it('tap on empty canvas still deselects, which is what makes the mode legible', () => {
    act(() => { useBoard.getState().select(['c1']); });
    send('pointerdown', { x: 100, y: 100 });
    send('pointerup', { x: 102, y: 101 });
    expect(useBoard.getState().selection.size).toBe(0);
  });

  it('but a pan does not — moving the board is not a selection gesture', () => {
    act(() => { useBoard.getState().select(['c1']); });
    send('pointerdown', { x: 100, y: 100 });
    send('pointermove', { x: 200, y: 160 });
    send('pointerup', { x: 200, y: 160 });
    expect(useBoard.getState().selection.has('c1'), 'panning cleared the selection').toBe(true);
  });
});

// ---- MO4 ------------------------------------------------------------------

describe('a cancelled gesture commits nothing', () => {
  it('a cancelled marquee selects nobody', () => {
    act(() => { useView.getState().setMarqueeMode(true); });
    send('pointerdown', { x: 0, y: 0 });
    send('pointermove', { x: 300, y: 300 });
    send('pointercancel', { x: 300, y: 300 });
    expect(useBoard.getState().selection.size, 'the system took the gesture and the app selected anyway').toBe(0);
    expect(container.querySelector('.marquee'), 'the marquee stayed painted forever').toBeNull();
  });

  it('a cancelled stroke does not become a SKETCH', () => {
    act(() => { useView.getState().setDrawMode(true); });
    send('pointerdown', { x: 0, y: 0 });
    send('pointermove', { x: 20, y: 20 });
    send('pointermove', { x: 40, y: 10 });
    send('pointermove', { x: 60, y: 30 });
    send('pointercancel', { x: 60, y: 30 });
    expect(committed, 'an interrupted stroke wrote a phantom element into the undo history').toEqual([]);
  });

  it('and the same stroke on a clean pointerup does', () => {
    act(() => { useView.getState().setDrawMode(true); });
    send('pointerdown', { x: 0, y: 0 });
    send('pointermove', { x: 20, y: 20 });
    send('pointermove', { x: 40, y: 10 });
    send('pointermove', { x: 60, y: 30 });
    send('pointerup', { x: 60, y: 30 });
    expect(committed.length, 'drawing stopped working').toBe(1);
    expect(committed[0][0].action).toBe('create');
    expect(committed[0][0].changes?.type).toBe('SKETCH');
  });

  it('cancellation clears the pan so the canvas is not stuck', () => {
    send('pointerdown', { x: 100, y: 100 });
    send('pointermove', { x: 150, y: 150 });
    send('pointercancel', { x: 150, y: 150 });
    const before = useView.getState().panX;
    send('pointermove', { x: 400, y: 400 });
    expect(useView.getState().panX, 'the canvas kept following a finger that was gone').toBe(before);
  });
});

// ---- the source-level half of MO4 -----------------------------------------
// The three gestures that bind their listeners to `window` live in ElementShell
// and cannot be driven through this canvas, so they are pinned as text — the
// same way the Go side greps handler signatures.

describe('every window-bound gesture registers cancellation', () => {
  it('drag, resize and drag-to-connect all listen for pointercancel', async () => {
    const { readFileSync } = await import('node:fs');
    const { resolve } = await import('node:path');
    const src = readFileSync(resolve(__dirname, './ElementShell.tsx'), 'utf8');
    const ups = src.match(/addEventListener\('pointerup'/g) ?? [];
    const cancels = src.match(/addEventListener\('pointercancel'/g) ?? [];
    expect(ups.length, 'ElementShell stopped binding pointerup at all').toBe(3);
    expect(cancels.length, 'a gesture can still be orphaned by the system').toBe(ups.length);
  });
});

// keep the settings import honest — it is loaded for its DOM side effects on
// boot and the canvas reads doubleClickCreates through it.
void useSettings;
