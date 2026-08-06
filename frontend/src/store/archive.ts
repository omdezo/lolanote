// JN17 — a finished production could only be abandoned or destroyed.
//
// `grep -rn 'archive|Archive'` over the whole repository returned four hits and
// none of them was a feature: the word appears inside a PROMPT EXAMPLE
// ("Archive the stale stuff"), in an eval fixture, and twice as a Lucide glyph
// name. There is no archived flag, no filter, no section in the drawer and no
// agent verb — so the agent's own prompt uses as a worked example of a good
// request a thing the product has no primitive for.
//
// Journey 6 is the whole point of project software. When the film wraps the
// board is either left in MY BOARDS forever — competing with live work in the
// drawer, in search, in the agent's cross-board reads — or deleted, which
// starts a 90-day destruction clock and takes the entire subtree with it.
// Neither is what "we finished" means.
//
// A STATUS, NOT A PLACE. `content.archived` on the BOARD, written through the
// ordinary transaction path so it journals, inverts and broadcasts like every
// other edit — which is also what makes "unarchive" free rather than a second
// mechanism.
import { api } from '../api/client';
import type { QElement } from '../api/types';
import { clientId } from './boardStore';

export function isArchived(board: QElement): boolean {
  return board.content?.archived === true;
}

/**
 * Put a board to bed, or wake it up.
 *
 * `boardId` is passed as the transaction's own board rather than whatever is
 * currently open: this is reachable from the drawer, where the board being
 * archived is precisely the one you are NOT looking at, and stamping the
 * journal row with the open board would file the record in the wrong log —
 * which is JN9's bug arriving through a different door.
 */
export async function setArchived(board: QElement, archived: boolean): Promise<void> {
  await api.applyTransaction(board.id, clientId, [{
    elementId: board.id,
    action: 'update',
    changes: { content: { archived } },
    undoChanges: { content: { archived: !archived } },
  }]);
}
