import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';
import { useBoard } from './boardStore';
import { useLabels } from './labels';
import { api } from '../api/client';
import type { QElement } from '../api/types';

// CV17. Hand tagging was the one write in the product that did not go through
// the transaction path.
//
// `attach`/`detach` called the REST endpoint and then hand-patched
// `useBoard.setState` directly. Nothing was journalled and nothing broadcast, so
// tagging three cards and pressing Ctrl+Z reached PAST the tagging to whatever
// you did before — silently — and a colleague's chips never moved at all.
//
// The agent has compiled `apply_label` into a real op with a full prior-array
// inverse since it shipped, which made it the only correct labeller in the app.
// "Still one undo" is a promise the human half of the label surface broke.

const card = (labelIds?: string[]): QElement => ({
  id: 'card-1', type: 'CARD',
  content: { title: 'the opening scene' },
  labelIds,
  location: { parentId: 'board-1', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 },
  createdBy: 'alice', createdAt: '', updatedAt: '',
} as unknown as QElement);

beforeEach(() => {
  useBoard.setState({
    boardId: 'board-1', rootBoardId: 'board-1', elements: { 'card-1': card(['old-1', 'old-2']) },
    selection: new Set(), undoStack: [], redoStack: [], readOnly: false,
  } as never);
  vi.spyOn(api, 'applyTransaction').mockResolvedValue({} as never);
});

afterEach(() => { vi.restoreAllMocks(); });

describe('tagging by hand is one undo, like every other edit', () => {
  it('attach commits a transaction rather than patching the store behind its back', async () => {
    await useLabels.getState().attach('card-1', 'new-1');

    expect(api.applyTransaction, 'attach did not reach the transaction path').toHaveBeenCalled();
    const ops = (api.applyTransaction as unknown as { mock: { calls: any[][] } }).mock.calls[0][2];
    expect(ops).toHaveLength(1);
    expect(ops[0].action).toBe('update');
    expect(ops[0].changes.labelIds).toEqual(['old-1', 'old-2', 'new-1']);

    // The stack is what Ctrl+Z reads. An empty one here is the whole bug.
    expect(useBoard.getState().undoStack).toHaveLength(1);
  });

  it('the inverse carries the WHOLE prior array, not the one label touched', async () => {
    await useLabels.getState().attach('card-1', 'new-1');
    const ops = (api.applyTransaction as unknown as { mock: { calls: any[][] } }).mock.calls[0][2];

    // A merge patch replaces `labelIds` wholesale. An undo naming only the
    // label just added would clear every other tag on the card — which is
    // worse than no undo, because it looks like it worked.
    expect(ops[0].undoChanges.labelIds).toEqual(['old-1', 'old-2']);
  });

  it('detach is the same mechanism in the other direction', async () => {
    await useLabels.getState().detach('card-1', 'old-1');
    const ops = (api.applyTransaction as unknown as { mock: { calls: any[][] } }).mock.calls[0][2];

    expect(ops[0].changes.labelIds).toEqual(['old-2']);
    expect(ops[0].undoChanges.labelIds).toEqual(['old-1', 'old-2']);
    expect(useBoard.getState().elements['card-1'].labelIds).toEqual(['old-2']);
  });

  it('re-attaching a label already on the card is not an edit', async () => {
    await useLabels.getState().attach('card-1', 'old-1');

    // A no-op that journals is an undo step that appears to do nothing when
    // pressed, which is how a person stops trusting Ctrl+Z.
    expect(api.applyTransaction).not.toHaveBeenCalled();
    expect(useBoard.getState().undoStack).toHaveLength(0);
  });
});
