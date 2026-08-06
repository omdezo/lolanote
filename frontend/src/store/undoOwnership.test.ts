import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';
import { expectRunRevert, forgetRunRevert, useBoard } from './boardStore';
import { api } from '../api/client';
import type { Op } from '../api/types';
// Warms the module registry: undo reaches the agent store through a dynamic
// import (the dependency runs agent → board, and this keeps it that way), and
// a cold import would resolve well after the assertions.
import '../agent/agentStore';

// JN12 / FR22. Two undo systems sat on the same ops and destroyed each other:
// the run kept RevertedElementIDs, Ctrl+Z posted a raw inverse the run never
// heard about, and openBoard wiped the local stack on every navigation —
// including stepping into a sub-board and straight back out. Which undo you got
// depended on where you had been, and the redo of a run-level revert put the
// work back on the board while the run still recorded it as gone.

const op = (id: string): Op => ({
  elementId: id,
  action: 'update',
  changes: { location: { parentId: 'col-1' } },
  undoChanges: { location: { parentId: 'board-1' } },
});

const boardView = (id: string, crumbs: Array<{ id: string; title: string }>) => ({
  board: {
    id, type: 'BOARD', content: { title: id },
    location: { parentId: crumbs[crumbs.length - 1]?.id ?? '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 },
    createdBy: '', createdAt: '', updatedAt: '',
  },
  breadcrumb: crumbs,
  role: 'owner',
});

beforeEach(() => {
  useBoard.setState({
    boardId: 'root', rootBoardId: '', elements: {}, selection: new Set(),
    undoStack: [], redoStack: [], readOnly: false,
  } as never);
  vi.spyOn(api, 'boardChildren').mockResolvedValue([]);
  vi.spyOn(api, 'boardUnsorted').mockResolvedValue([]);
  vi.spyOn(api, 'boardChildStats').mockResolvedValue({});
  vi.spyOn(api, 'applyTransaction').mockResolvedValue({} as never);
});

afterEach(() => { vi.restoreAllMocks(); });

describe('the local undo stack survives moving around one board tree', () => {
  it('stepping into a sub-board and back out keeps the history', async () => {
    vi.spyOn(api, 'board').mockImplementation(async (id: string) =>
      (id === 'root'
        ? boardView('root', [])
        : boardView('sub', [{ id: 'root', title: 'Root' }])) as never);

    await useBoard.getState().openBoard('root');
    useBoard.setState({ undoStack: [{ ops: [op('a')], boardId: 'root' }] } as never);

    await useBoard.getState().openBoard('sub');
    expect(
      useBoard.getState().undoStack,
      'walking into a column\'s board erased the history that was on screen a second ago',
    ).toHaveLength(1);

    await useBoard.getState().openBoard('root');
    expect(useBoard.getState().undoStack).toHaveLength(1);
  });

  it('going to a different tree does clear it', async () => {
    vi.spyOn(api, 'board').mockImplementation(async (id: string) =>
      (id === 'root' ? boardView('root', []) : boardView('other', [])) as never);

    await useBoard.getState().openBoard('root');
    useBoard.setState({ undoStack: [{ ops: [op('a')], boardId: 'root' }] } as never);

    await useBoard.getState().openBoard('other');
    expect(useBoard.getState().undoStack, 'undo reached across two unrelated boards').toHaveLength(0);
  });

  it('undoes against the board the ops belong to, not whatever is open now', async () => {
    vi.spyOn(api, 'board').mockImplementation(async (id: string) =>
      (id === 'root'
        ? boardView('root', [])
        : boardView('sub', [{ id: 'root', title: 'Root' }])) as never);

    await useBoard.getState().openBoard('root');
    useBoard.setState({ undoStack: [{ ops: [op('a')], boardId: 'root' }] } as never);
    await useBoard.getState().openBoard('sub');

    useBoard.getState().undo();
    const calls = vi.mocked(api.applyTransaction).mock.calls;
    const [postedBoard] = calls[calls.length - 1];
    expect(postedBoard, 'the inverse was aimed at a board that does not hold the element').toBe('root');
  });
});

describe('one op has one undo owner', () => {
  it('Ctrl+Z on an agent transaction reverts through the RUN, not a raw inverse', async () => {
    const revert = vi.spyOn(api, 'agentRevert').mockResolvedValue({ id: 'run-A', state: 'REVERTED' } as never);
    vi.spyOn(useBoard.getState(), 'refreshBoard').mockResolvedValue(undefined);

    useBoard.getState().adoptRemote([op('a'), op('b')], 'run-A');
    useBoard.getState().undo();
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));

    expect(
      vi.mocked(api.applyTransaction).mock.calls.length,
      'the raw inverse went out anyway, so the run still thinks its work stands',
    ).toBe(0);
    expect(revert).toHaveBeenCalledWith('run-A', ['a', 'b']);
  });

  it('refuses to redo a run-level revert instead of silently re-applying it', async () => {
    vi.spyOn(api, 'agentRevert').mockResolvedValue({ id: 'run-B', state: 'REVERTED' } as never);

    useBoard.getState().adoptRemote([op('a')], 'run-B');
    useBoard.getState().undo();
    await new Promise((r) => setTimeout(r, 0));

    expect(useBoard.getState().redoStack).toHaveLength(1);
    useBoard.getState().redo();
    // Nothing posted, and the entry stays put: re-applying would put the work
    // back while RevertedElementIDs still claimed it was gone, after which the
    // outcome card's Undo short-circuits and nothing can remove it.
    expect(vi.mocked(api.applyTransaction).mock.calls.length).toBe(0);
    expect(useBoard.getState().redoStack).toHaveLength(1);
  });

  it('does not adopt the revert transaction as a fresh undo step', () => {
    // The revert is itself origin=agent carrying the reverting user's id, so it
    // comes back over the socket looking exactly like the run's own work.
    expectRunRevert('run-C');
    useBoard.getState().adoptRemote([op('a')], 'run-C');
    expect(
      useBoard.getState().undoStack,
      'undoing a run left a step whose undo asks to revert an already-reverted run',
    ).toHaveLength(0);

    // The next genuine agent transaction for the same run is still adopted.
    useBoard.getState().adoptRemote([op('b')], 'run-C');
    expect(useBoard.getState().undoStack).toHaveLength(1);
  });

  it('a revert that never happened does not eat the run\'s next transaction', () => {
    expectRunRevert('run-D');
    forgetRunRevert('run-D');
    useBoard.getState().adoptRemote([op('a')], 'run-D');
    expect(
      useBoard.getState().undoStack,
      'a failed revert left an expectation that swallowed the next real change',
    ).toHaveLength(1);
  });

  it('a hand edit is still undone locally', () => {
    useBoard.getState().adoptRemote([op('a')]);
    useBoard.getState().undo();
    expect(vi.mocked(api.applyTransaction).mock.calls.length).toBe(1);
  });
});
