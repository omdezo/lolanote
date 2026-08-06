// Local board mirror (§9.6): boards render instantly from IndexedDB, then
// reconcile with the network — Milanote's "render from cache first" startup.
// One object store, keyed by board id, holding the exact payloads openBoard
// fetches. Dependency-free (raw IndexedDB, ~60 lines).
import type { BoardView, QElement } from '../api/types';

export interface BoardSnapshot {
  view: BoardView;
  children: QElement[];
  unsorted: QElement[];
  savedAt: number;
}

const DB_NAME = 'qomranote';
const STORE = 'boards';
const MAX_AGE_MS = 14 * 24 * 60 * 60 * 1000; // stale snapshots expire

let dbPromise: Promise<IDBDatabase | null> | null = null;

/**
 * Whose boards these are.
 *
 * Keys used to be the bare board id, so on a machine two people share — an edit
 * suite, an agency workstation — the second person's first paint of a board id
 * they had access to could come from the first person's snapshot. Namespacing
 * makes that impossible by construction rather than by remembering to clear.
 *
 * Empty until sign-in resolves, which is correct: an anonymous share-link
 * visitor and a signed-in owner are genuinely different readers.
 */
let owner = '';

/** Set once at boot, from the authenticated subject. */
export function setCacheOwner(sub: string): void {
  owner = sub;
}

const keyFor = (boardId: string) => (owner ? `${owner}:${boardId}` : boardId);

function db(): Promise<IDBDatabase | null> {
  if (!('indexedDB' in window)) return Promise.resolve(null);
  if (!dbPromise) {
    dbPromise = new Promise((resolve) => {
      const req = indexedDB.open(DB_NAME, 1);
      req.onupgradeneeded = () => req.result.createObjectStore(STORE);
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => resolve(null); // private mode etc. — cache is optional
    });
  }
  return dbPromise;
}

export async function loadBoardSnapshot(boardId: string): Promise<BoardSnapshot | null> {
  const d = await db();
  if (!d) return null;
  return new Promise((resolve) => {
    try {
      const req = d.transaction(STORE, 'readonly').objectStore(STORE).get(keyFor(boardId));
      req.onsuccess = () => {
        const snap = req.result as BoardSnapshot | undefined;
        resolve(snap && Date.now() - snap.savedAt < MAX_AGE_MS ? snap : null);
      };
      req.onerror = () => resolve(null);
    } catch {
      resolve(null);
    }
  });
}

/**
 * MO9. How many boards the mirror keeps.
 *
 * `MAX_AGE_MS` expires snapshots by age and RECLAIMS NOTHING under pressure:
 * a fortnight of work on a production with forty nested boards is forty full
 * payloads — element content, document bodies, table cells — all of them still
 * young, all of them still on disk. The origin quota on a phone is a fraction
 * of remaining device storage and browsers evict per-origin, all-or-nothing, so
 * the mirror growing unbounded is the mirror arranging its own deletion.
 *
 * Twenty-four is generous for "the boards I am actually working in" and small
 * enough that the mirror is never the reason the quota is hit.
 */
const MAX_BOARDS = 24;

export async function saveBoardSnapshot(boardId: string, snap: Omit<BoardSnapshot, 'savedAt'>): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    d.transaction(STORE, 'readwrite').objectStore(STORE).put({ ...snap, savedAt: Date.now() }, keyFor(boardId));
  } catch { /* quota/private mode — cache is best-effort */ }
  void pruneToRecent();
}

/**
 * Drop the least recently saved boards past the cap.
 *
 * Counted by key first, so the common case — under the cap, which is almost
 * always — reads no payloads at all. Only when the cap is exceeded does this
 * walk the values, which is the one place their `savedAt` lives.
 */
export async function pruneToRecent(max = MAX_BOARDS): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    const keys = await new Promise<IDBValidKey[]>((resolve) => {
      const req = d.transaction(STORE, 'readonly').objectStore(STORE).getAllKeys();
      req.onsuccess = () => resolve(req.result ?? []);
      req.onerror = () => resolve([]);
    });
    if (keys.length <= max) return;

    const stamped = await new Promise<Array<{ key: IDBValidKey; savedAt: number }>>((resolve) => {
      const out: Array<{ key: IDBValidKey; savedAt: number }> = [];
      const req = d.transaction(STORE, 'readonly').objectStore(STORE).openCursor();
      req.onsuccess = () => {
        const cursor = req.result;
        if (!cursor) { resolve(out); return; }
        out.push({ key: cursor.key, savedAt: (cursor.value as BoardSnapshot)?.savedAt ?? 0 });
        cursor.continue();
      };
      req.onerror = () => resolve(out);
    });

    stamped.sort((a, b) => b.savedAt - a.savedAt);
    const doomed = stamped.slice(max);
    if (doomed.length === 0) return;
    const tx = d.transaction(STORE, 'readwrite');
    const store = tx.objectStore(STORE);
    for (const entry of doomed) store.delete(entry.key);
  } catch { /* best-effort, exactly like every other write here */ }
}

/**
 * Wipe the mirror.
 *
 * Awaits the transaction rather than firing it: this is called immediately
 * before a logout redirect, and a navigation kills an IndexedDB transaction in
 * flight — so the version that returned early left the boards on disk exactly
 * as if it had never been called.
 */
export async function clearBoardCache(): Promise<void> {
  const d = await db();
  if (!d) return;
  await new Promise<void>((resolve) => {
    try {
      const tx = d.transaction(STORE, 'readwrite');
      tx.objectStore(STORE).clear();
      tx.oncomplete = () => resolve();
      tx.onerror = () => resolve();
      tx.onabort = () => resolve();
    } catch {
      resolve();
    }
  });
}

/**
 * Everything this app has left on the device, cleared in one place.
 *
 * The mirror holds `{view, children, unsorted}` for every board opened in the
 * last fortnight — full element content, document bodies, table cells — keyed
 * by board id in IndexedDB. `clearBoardCache` existed and had exactly one
 * reference in the whole frontend: its own definition. So a filmmaker in a
 * shared edit suite, on an agency machine, on a rented workstation, logged out
 * and the next person could open devtools and read the production boards, or
 * simply reopen the PWA offline and have them paint from cache.
 *
 * One function that owns the list, so a new local store is added here rather
 * than remembered in three places — and the account-deletion path, where the
 * user has just asked for everything to be destroyed and the most locally
 * accessible copy is what survives, gets the fix for free.
 */
export async function clearLocalData(): Promise<void> {
  await clearBoardCache();
}
