import { describe, expect, it, beforeEach } from 'vitest';
import { useBoard } from './boardStore';
import type { Op } from '../api/types';

const op = (id: string): Op => ({
  elementId: id,
  action: 'update',
  changes: { location: { parentId: 'col-1' } },
  undoChanges: { location: { parentId: 'board-1' } },
});

// An agent run acting on your behalf is your edit, but it arrives over the
// socket like a collaborator's. It was therefore applied and never recorded, so
// Ctrl+Z reached PAST it and undid whatever you did before it — silently, while
// the auto-apply tooltip promised "still one undo".
describe('adoptRemote', () => {
  beforeEach(() => {
    useBoard.setState({ undoStack: [], redoStack: [] });
  });

  it('makes an already-applied remote change locally undoable', () => {
    useBoard.getState().adoptRemote([op('a'), op('b')]);

    const { undoStack } = useBoard.getState();
    expect(undoStack).toHaveLength(1);
    expect(undoStack[0].ops).toHaveLength(2);
  });

  it('does not re-apply the ops — they are already reduced', () => {
    // A second application would double every move. The guard is that
    // adoptRemote touches only the stacks, never the element graph.
    useBoard.setState({ elements: {} });
    useBoard.getState().adoptRemote([op('a')]);
    expect(Object.keys(useBoard.getState().elements)).toHaveLength(0);
  });

  it('clears redo, like any new edit', () => {
    useBoard.setState({ redoStack: [{ ops: [op('old')], boardId: 'board-1' }] });
    useBoard.getState().adoptRemote([op('a')]);
    expect(useBoard.getState().redoStack).toHaveLength(0);
  });

  it('ignores an empty transaction rather than pushing a no-op step', () => {
    // A revert that found nothing to reverse would otherwise leave a step that
    // consumes a Ctrl+Z and does nothing — which reads as undo being broken.
    useBoard.getState().adoptRemote([]);
    expect(useBoard.getState().undoStack).toHaveLength(0);
  });

  it('respects the stack bound so a long session cannot grow without limit', () => {
    for (let i = 0; i < 130; i++) useBoard.getState().adoptRemote([op(`e${i}`)]);
    expect(useBoard.getState().undoStack.length).toBeLessThanOrEqual(100);
  });
});
