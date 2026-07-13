// Clipboard: internal rich-element copy/cut/paste plus system-clipboard
// paste (image → upload, URL → link card, text → note).
//
// Copying elements mirrors a READABLE plain-text version to the system
// clipboard (so pasting into a note or any other app gives text, never JSON)
// while the full elements stay in the in-memory buffer; on canvas paste, a
// fingerprint match on the clipboard text selects the rich buffer. Copied
// boards paste as SHORTCUTS to the same board (an ALIAS — opening it lands
// on the exact same board), and copied columns/task lists bring their
// children along.
import { api, uploadFile } from '../api/client';
import type { Op, QElement } from '../api/types';
import { newObjectId } from '../lib/objectId';
import { createOp, deleteOp, useBoard } from './boardStore';
import { useView } from './viewStore';
import { toast } from '../components/ui/Toaster';

let buffer: QElement[] = [];
let mirrorText = ''; // what we last wrote to the system clipboard

// textOf renders one element as human text for the system clipboard.
function textOf(e: QElement): string {
  const c = e.content ?? {};
  return (c.title as string) || (c.textPreview as string) || (c.text as string)
    || (c.filename as string) || (c.caption as string) || (c.url as string) || '';
}

// withDescendants expands copied containers (columns, task lists — not
// boards) so their children travel with them.
function withDescendants(els: QElement[]): QElement[] {
  const state = useBoard.getState();
  const out = new Map<string, QElement>();
  const walk = (el: QElement) => {
    if (out.has(el.id)) return;
    out.set(el.id, el);
    if (el.type === 'COLUMN' || el.type === 'TASK_LIST') {
      Object.values(state.elements)
        .filter((child) => child.location.parentId === el.id && !child.deletedAt)
        .forEach(walk);
    }
  };
  els.forEach(walk);
  return Array.from(out.values());
}

export function copySelection() {
  const state = useBoard.getState();
  const els = Array.from(state.selection).map((id) => state.elements[id]).filter(Boolean) as QElement[];
  if (els.length === 0) return;
  buffer = withDescendants(els).map((e) => structuredClone(e));

  // Human-readable mirror: pasting into a note (or another app) gives text.
  mirrorText = els.map(textOf).filter(Boolean).join('\n');
  if (mirrorText) navigator.clipboard?.writeText(mirrorText).catch(() => undefined);

  toast.info(`Copied ${els.length} item${els.length === 1 ? '' : 's'}`);
}

export function cutSelection() {
  const state = useBoard.getState();
  const els = Array.from(state.selection).map((id) => state.elements[id]).filter(Boolean) as QElement[];
  if (els.length === 0) return;
  copySelection();
  // Boards are never destroyed by a cut — pasting produces a shortcut to the
  // same board, so deleting the original would orphan its content.
  const deletable = els.filter((e) => e.type !== 'BOARD' && e.type !== 'ALIAS');
  if (deletable.length) void state.commitTransaction(deletable.map((e) => deleteOp(e)));
  if (deletable.length !== els.length) toast.info('Boards paste as shortcuts — the original stays');
  state.clearSelection();
}

// pasteFromClipboardData is the native paste path (Ctrl/⌘+V fires a real
// ClipboardEvent — no permission prompts): files first, then our own copy
// fingerprint, then URL, then plain text.
export async function pasteFromClipboardData(data: DataTransfer, x: number, y: number): Promise<boolean> {
  const state = useBoard.getState();
  if (state.readOnly) return false;

  // 1) Files (screenshots, copied images, real files).
  if (data.files.length > 0) {
    await placeFiles(Array.from(data.files), x, y);
    return true;
  }

  const text = data.getData('text/plain');

  // 2) Our own copy: fingerprint match → paste the rich internal buffer.
  if (buffer.length && text && text === mirrorText) {
    await recreate(buffer, x, y);
    return true;
  }
  if (buffer.length && !text) {
    await recreate(buffer, x, y);
    return true;
  }

  // 3) URL → link card; text → note.
  if (text?.trim()) {
    await pasteText(text, x, y);
    return true;
  }
  return buffer.length > 0 ? (await recreate(buffer, x, y), true) : false;
}

// pasteAt is the programmatic path (context menu → Paste) where no
// ClipboardEvent exists; it asks the async Clipboard API, falling back to
// the internal buffer when the browser withholds permission.
export async function pasteAt(x: number, y: number) {
  const state = useBoard.getState();
  if (state.readOnly) return;

  try {
    const items = await navigator.clipboard?.read?.();
    if (items) {
      for (const item of items) {
        const imgType = item.types.find((t) => t.startsWith('image/'));
        if (imgType) {
          const blob = await item.getType(imgType);
          const file = new File([blob], `pasted-${Date.now()}.png`, { type: imgType });
          await placeFiles([file], x, y);
          return;
        }
      }
    }
  } catch { /* clipboard read blocked — fall through */ }

  try {
    const text = await navigator.clipboard?.readText?.();
    if (text) {
      if (buffer.length && text === mirrorText) { await recreate(buffer, x, y); return; }
      await pasteText(text, x, y);
      return;
    }
  } catch { /* ignore */ }

  if (buffer.length) await recreate(buffer, x, y);
}

// placeFiles uploads and drops files as IMAGE/FILE cards, cascading offsets.
async function placeFiles(files: File[], x: number, y: number) {
  const state = useBoard.getState();
  let offset = 0;
  for (const file of files) {
    try {
      const { url, attachmentId } = await uploadFile(file);
      const isImage = file.type.startsWith('image/');
      await state.commitTransaction([
        createOp(isImage ? 'IMAGE' : 'FILE', state.boardId, {
          position: { x: x + offset, y: y + offset },
          width: isImage ? 280 : 0,
          content: isImage
            ? { url, attachmentId, caption: '' }
            : { url, attachmentId, filename: file.name, mimeType: file.type, size: file.size },
        }),
      ]);
      offset += 28;
    } catch {
      toast.error(`Could not upload ${file.name}`);
    }
  }
}

async function pasteText(text: string, x: number, y: number) {
  const state = useBoard.getState();
  if (/^https?:\/\/\S+$/.test(text.trim())) {
    const meta = await api.resolveLink(text.trim()).catch(() => null);
    await state.commitTransaction([
      createOp('LINK', state.boardId, {
        position: { x, y }, width: 260,
        content: meta
          ? { url: meta.url, title: meta.title, description: meta.description, thumbnailUrl: meta.thumbnailUrl, embedType: meta.embedType, showPreview: true, showDescription: true }
          : { url: text.trim(), title: text.trim(), showPreview: false, showDescription: false },
      }),
    ]);
    return;
  }
  const paragraphs = text.split(/\n{2,}/).map((p) => p.trim()).filter(Boolean);
  await state.commitTransaction([
    createOp('CARD', state.boardId, {
      position: { x, y }, width: 300,
      content: {
        textPreview: text.slice(0, 500),
        doc: {
          type: 'doc',
          content: paragraphs.map((p) => ({
            type: 'paragraph',
            content: [{ type: 'text', text: p.replace(/\n/g, ' ') }],
          })),
        },
      },
    }),
  ]);
}

// recreate deep-clones a set of elements into the current board at a point,
// remapping ids (and internal parent refs for copied containers). Boards
// become SHORTCUTS to the original board — pasting a board never duplicates
// it; opening the pasted card lands on the exact same board.
async function recreate(els: QElement[], x: number, y: number) {
  const state = useBoard.getState();
  if (els.length === 0) return;
  const positioned = els.filter((e) => els.every((p) => p.id !== e.location.parentId));
  const minX = Math.min(...positioned.map((e) => e.location.position.x));
  const minY = Math.min(...positioned.map((e) => e.location.position.y));
  const idMap = new Map<string, string>();
  els.forEach((e) => idMap.set(e.id, newObjectId()));

  const ops: Op[] = [];
  for (const e of els) {
    const isRoot = !idMap.has(e.location.parentId);
    const parentId = idMap.get(e.location.parentId) ?? state.boardId;
    const position = isRoot
      ? { x: x + (e.location.position.x - minX), y: y + (e.location.position.y - minY) }
      : e.location.position;

    // LINE endpoints must both exist in the paste; remap or drop.
    if (e.type === 'LINE') {
      const fromId = idMap.get(e.content?.fromId);
      const toId = idMap.get(e.content?.toId);
      if (!fromId || !toId) continue;
      ops.push({
        elementId: idMap.get(e.id)!, action: 'create',
        changes: {
          type: 'LINE',
          location: { parentId, section: 'CANVAS', position, index: e.location.index, width: 0, height: 0 },
          content: { ...structuredClone(e.content), fromId, toId },
        },
        undoChanges: {},
      });
      continue;
    }

    const isBoard = e.type === 'BOARD';
    const isAlias = e.type === 'ALIAS';
    ops.push({
      elementId: idMap.get(e.id)!, action: 'create',
      changes: {
        type: isBoard || isAlias ? 'ALIAS' : e.type,
        location: {
          parentId, section: 'CANVAS', position,
          index: e.location.index, width: e.location.width, height: 0,
        },
        content: isBoard
          ? { targetBoardId: e.id, title: e.content?.title ?? 'Board' }
          : structuredClone(e.content),
      },
      undoChanges: {},
    });
  }
  if (ops.length === 0) return;
  await state.commitTransaction(ops);
  toast.success(`Pasted ${ops.length} item${ops.length === 1 ? '' : 's'}`);
}

export function clipboardHasContent(): boolean {
  return buffer.length > 0;
}
