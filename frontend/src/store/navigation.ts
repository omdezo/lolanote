// Opening a board is two things that must happen together: swap the store's
// contents, and move the realtime room with it. Doing one without the other
// leaves you watching a board you are not subscribed to, or subscribed to a
// board you are not looking at.
//
// A shared function rather than a prop, because the agent's outcome card needs
// it too and threading `navigate` from the app root through the bar's state
// machine to a row inside the outcome card would put a UI concern in four
// components that have no other reason to know about routing.
import { useBoard } from './boardStore';
import { useView } from './viewStore';

export async function navigateToBoard(id: string): Promise<void> {
  if (!id || useBoard.getState().boardId === id) return;
  await useBoard.getState().openBoard(id);
  const { connectBoard } = await import('../realtime/socket');
  await connectBoard(id);
}

/**
 * Go to a board AND say which thing you went there for (JN11).
 *
 * The reminder sweeper carefully populates `elementId` on every notification it
 * writes, and the bell's `go()` threw it away: following "your reminder on
 * 'call the financier'" dropped you on a board of 200 elements with no
 * indication which one it meant. The information travelled the whole way and
 * was discarded at the last step.
 *
 * Selecting rather than only scrolling, because selection is what the rest of
 * the app already treats as "this one": it draws the ring, it feeds the action
 * bar, and it is what the roving canvas focus reads. `centreOn` is best-effort —
 * a board can legitimately arrive without the element (someone deleted it while
 * the notification sat unread), and landing on the board is still better than
 * refusing to navigate.
 */
export async function navigateToElement(boardId: string, elementId?: string): Promise<void> {
  await navigateToBoard(boardId);
  if (!elementId) return;
  const el = useBoard.getState().elements[elementId];
  if (!el) return;
  useBoard.getState().select([elementId]);
  useView.getState().setFocused(elementId);
}
