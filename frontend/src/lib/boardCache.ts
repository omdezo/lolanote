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
      const req = d.transaction(STORE, 'readonly').objectStore(STORE).get(boardId);
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

export async function saveBoardSnapshot(boardId: string, snap: Omit<BoardSnapshot, 'savedAt'>): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    d.transaction(STORE, 'readwrite').objectStore(STORE).put({ ...snap, savedAt: Date.now() }, boardId);
  } catch { /* quota/private mode — cache is best-effort */ }
}

export async function clearBoardCache(): Promise<void> {
  const d = await db();
  if (!d) return;
  try {
    d.transaction(STORE, 'readwrite').objectStore(STORE).clear();
  } catch { /* ignore */ }
}
