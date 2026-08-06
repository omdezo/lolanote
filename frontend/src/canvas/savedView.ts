/**
 * Where you were looking, per board.
 *
 * The view defaulted to (0,0) at 100% and was never written down, so every
 * refresh returned you to the world origin — which is wherever the coordinate
 * space starts and almost never where your work is. On a board the agent had
 * laid out, "open my board" answered with an empty grey field.
 *
 * Kept in localStorage rather than on the element graph on purpose: this is a
 * property of THIS BROWSER looking at the board, not of the board. Two people
 * on one board are looking at different parts of it, and a viewport that
 * synced would drag one of them around while the other panned.
 */
export interface SavedView {
  panX: number;
  panY: number;
  scale: number;
}

const KEY = 'qomra.view';
/** Enough for a working session's worth of boards, bounded so a year of use
 *  cannot grow without limit. Oldest entries fall off first. */
const MAX_BOARDS = 40;

type Store = Record<string, SavedView & { at: number }>;

function load(): Store {
  try {
    const raw = localStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Store) : {};
  } catch {
    // A corrupt or unavailable store must not stop a board from opening. The
    // cost of failing here is that you get framed on your content instead of
    // restored, which is the better of the two fallbacks anyway.
    return {};
  }
}

export function readSavedView(boardID: string): SavedView | null {
  const entry = load()[boardID];
  if (!entry) return null;
  // A stored scale of zero would divide the whole canvas into nothing, and a
  // NaN pan puts every element at an unrenderable coordinate. Both are cheaper
  // to reject than to debug from a screenshot.
  if (!Number.isFinite(entry.panX) || !Number.isFinite(entry.panY)) return null;
  if (!Number.isFinite(entry.scale) || entry.scale <= 0) return null;
  return { panX: entry.panX, panY: entry.panY, scale: entry.scale };
}

export function saveView(boardID: string, view: SavedView): void {
  try {
    const store = load();
    store[boardID] = { ...view, at: Date.now() };
    const ids = Object.keys(store);
    if (ids.length > MAX_BOARDS) {
      ids.sort((a, b) => (store[a].at ?? 0) - (store[b].at ?? 0))
        .slice(0, ids.length - MAX_BOARDS)
        .forEach((id) => delete store[id]);
    }
    localStorage.setItem(KEY, JSON.stringify(store));
  } catch {
    // Quota, private mode, or a disabled store. Losing the position is a
    // smaller failure than refusing to let somebody pan.
  }
}

/** Forget a board's position — used when a board is deleted, so a recycled id
 *  cannot inherit somebody else's viewport. */
export function forgetSavedView(boardID: string): void {
  try {
    const store = load();
    if (!(boardID in store)) return;
    delete store[boardID];
    localStorage.setItem(KEY, JSON.stringify(store));
  } catch {
    /* see saveView */
  }
}
