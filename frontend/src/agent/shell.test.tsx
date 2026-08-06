// FIRST: jsdom has neither matchMedia nor ResizeObserver.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { useAgent } from './agentStore';
import { useAgentShell } from './useAgentShell';
import { useBoard } from '../store/boardStore';
import { ownsEscape, useView } from '../store/viewStore';
import { logout } from '../auth/keycloak';
import * as boardCache from '../lib/boardCache';
import type { AgentPlan, AgentRun, QElement } from '../api/types';

vi.mock('../api/client', () => ({
  api: {
    agentApply: vi.fn(),
    agentDiscard: vi.fn(async () => undefined),
    agentRun: vi.fn(),
    agentEvents: vi.fn(async () => []),
  },
  ApiError: class extends Error { status = 0; },
}));

vi.mock('keycloak-js', () => ({
  default: class {
    authenticated = true;
    subject = 'sub';
    tokenParsed = {};
    init = vi.fn(async () => true);
    login = vi.fn();
    logout = vi.fn();
    updateToken = vi.fn(async () => true);
  },
}));

import { api } from '../api/client';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement;
let root: Root;

const el = (id: string, type: string, parentId: string): QElement => ({
  id, type, content: {},
  location: { parentId, section: 'CANVAS', position: { x: 0, y: 0 }, index: 1, width: 260, height: 0 },
  createdBy: '', createdAt: '', updatedAt: '',
} as QElement);

function Harness() { useAgentShell(); return null; }

beforeEach(() => {
  container = document.createElement('div');
  container.className = 'canvas-viewport';
  document.body.appendChild(container);
  root = createRoot(container);
  useView.setState({ overlays: [] });
  useBoard.setState({
    boardId: 'b1',
    elements: { b1: el('b1', 'BOARD', '') },
    selection: new Set(),
    readOnly: false,
  } as never);
  useAgent.setState({
    run: null, events: [], adjustments: [], busy: false, open: false, stale: null,
    capabilities: { enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 } },
  });
  vi.clearAllMocks();
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
});

// ---- AX7 ------------------------------------------------------------------
// Escape at PROPOSED called discard() on a `window` listener, alongside App's
// panel-close listener on another `window` listener, neither stopping
// propagation. So with a proposal pending, opening Settings or Search and
// pressing Escape to close it ALSO destroyed the plan — paid for, terminal, no
// confirmation, no undo, and nothing announcing that it had happened.
describe('Escape does not destroy a plan', () => {
  const proposed = (): AgentRun => ({
    id: 'r1', state: 'PROPOSED',
    task: { intent: 'Tidy', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
    usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.31, calls: 4 },
    createdAt: '', updatedAt: '',
    plan: { actions: [] } as AgentPlan,
  } as unknown as AgentRun);

  it('leaves a proposal standing', () => {
    useAgent.setState({ run: proposed(), open: true });
    act(() => { root.render(<Harness />); });

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });

    expect(api.agentDiscard, 'Escape discarded a plan that cost money').not.toHaveBeenCalled();
    expect(useAgent.getState().run, 'the run was cleared by a reflex key').not.toBeNull();
  });

  it('and Ctrl+Enter still applies, because that one IS a decision', () => {
    (api.agentApply as ReturnType<typeof vi.fn>).mockResolvedValue(proposed());
    useAgent.setState({ run: proposed(), open: true });
    act(() => { root.render(<Harness />); });
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
    });
    expect(api.agentApply).toHaveBeenCalled();
  });
});

describe('the overlay stack', () => {
  it('gives Escape to whichever surface opened last', () => {
    const v = useView.getState();
    act(() => { v.pushOverlay('panel'); });
    expect(ownsEscape('panel')).toBe(true);

    act(() => { v.pushOverlay('context-menu'); });
    expect(ownsEscape('context-menu')).toBe(true);
    expect(ownsEscape('panel'), 'two surfaces both closed on one Escape').toBe(false);

    act(() => { v.popOverlay('context-menu'); });
    expect(ownsEscape('panel')).toBe(true);
  });

  it('re-opening a surface does not stack a second entry', () => {
    const v = useView.getState();
    act(() => { v.pushOverlay('panel'); v.pushOverlay('panel'); });
    expect(useView.getState().overlays).toEqual(['panel']);
  });

  it('nothing open means nobody is blocked', () => {
    expect(ownsEscape('anything')).toBe(true);
  });

  it('the agent shell takes its turn like every other surface', () => {
    useAgent.setState({ open: true });
    act(() => { root.render(<Harness />); });
    expect(useView.getState().overlays).toContain('agent');
  });
});

// ---- CV5 ------------------------------------------------------------------
// Every other creation path in the product selects its result. A run selected
// nothing, so after it finished, Ctrl+D, the floating action bar and "Ask Qomra
// about these" all targeted an empty selection.
describe('a run selects what it made', () => {
  const applied = (actions: AgentPlan['actions']): AgentRun => ({
    id: 'r1', state: 'COMPLETED',
    task: { intent: 'Build it', rootBoardId: 'b1', scope: 'board', autonomy: 'auto' },
    usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0, calls: 1 },
    createdAt: '', updatedAt: '',
    plan: { actions } as AgentPlan,
  } as unknown as AgentRun);

  it('puts the selection on the new elements once the refresh has settled', async () => {
    const result = applied([
      { seq: 0, kind: 'create_column', elementId: 'newcol', title: 'Casting', summary: '' },
      { seq: 1, kind: 'move_element', elementId: 'b1', summary: '' },
    ]);
    (api.agentApply as ReturnType<typeof vi.fn>).mockResolvedValue(result);
    useBoard.setState({
      refreshBoard: async () => {
        // The server's write lands here, not before: selecting first would name
        // an id the store does not have.
        useBoard.setState({
          elements: { ...useBoard.getState().elements, newcol: el('newcol', 'COLUMN', 'b1') },
        } as never);
      },
    } as never);
    useAgent.setState({ run: result });

    await act(async () => { await useAgent.getState().apply(); });
    expect([...useBoard.getState().selection]).toEqual(['newcol']);
  });

  it('a duplicate selects the COPY, never the source it names', async () => {
    const result = applied([
      { seq: 0, kind: 'duplicate', elementId: 'source', summary: '', copies: [{ newId: 'copy', sourceId: 'source', parentId: 'b1' }] },
    ]);
    (api.agentApply as ReturnType<typeof vi.fn>).mockResolvedValue(result);
    useBoard.setState({
      elements: { b1: el('b1', 'BOARD', ''), source: el('source', 'CARD', 'b1'), copy: el('copy', 'CARD', 'b1') },
      refreshBoard: async () => undefined,
    } as never);
    useAgent.setState({ run: result });

    await act(async () => { await useAgent.getState().apply(); });
    expect([...useBoard.getState().selection]).toEqual(['copy']);
  });

  it('leaves the selection alone when the plan wrote nothing to this canvas', async () => {
    const result = applied([
      { seq: 0, kind: 'create_note', elementId: 'inside', parentId: 'sub-board', summary: '' },
    ]);
    (api.agentApply as ReturnType<typeof vi.fn>).mockResolvedValue(result);
    useBoard.setState({
      selection: new Set(['b1']),
      refreshBoard: async () => undefined,
    } as never);
    useAgent.setState({ run: result });

    await act(async () => { await useAgent.getState().apply(); });
    expect([...useBoard.getState().selection]).toEqual(['b1']);
  });
});

// ---- DL7 ------------------------------------------------------------------
// clearBoardCache() had exactly one reference in the whole frontend: its own
// definition. So logging out on a shared edit suite, an agency machine or a
// client's laptop left up to fourteen days of full board content on the device.
describe('logging out takes the local copy with it', () => {
  it('clears the mirror before the redirect fires', async () => {
    const order: string[] = [];
    // Deliberately resolves a tick late: a logout that FIRES the clear without
    // awaiting it reads identically to one that waits, right up until the
    // navigation cancels the transaction on a real machine.
    vi.spyOn(boardCache, 'clearLocalData').mockImplementation(async () => {
      await new Promise((resolve) => { setTimeout(resolve, 0); });
      order.push('cleared');
    });
    await logout();
    order.push('redirected');
    expect(order, 'the navigation would kill the IndexedDB transaction').toEqual(['cleared', 'redirected']);
  });
});
