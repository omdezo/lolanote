// Clipboard: internal rich-element copy/cut/paste plus system-clipboard
// paste (image → upload, URL → link card, text → note). Internal copy also
// writes a JSON blob to the system clipboard so paste survives across tabs.
import { api, uploadFile } from '../api/client';
import type { Op, QElement } from '../api/types';
import { newObjectId } from '../lib/objectId';
import { createOp, deleteOp, useBoard } from './boardStore';
import { useView } from './viewStore';
import { toast } from '../components/ui/Toaster';

const MIME = 'application/x-qomranote';
let buffer: QElement[] = [];

export function copySelection() {
  const state = useBoard.getState();
  const els = Array.from(state.selection).map((id) => state.elements[id]).filter(Boolean) as QElement[];
  if (els.length === 0) return;
  buffer = els.map((e) => structuredClone(e));
  // Best-effort mirror to the system clipboard for cross-context paste.
  navigator.clipboard?.writeText(JSON.stringify({ [MIME]: buffer })).catch(() => undefined);
  toast.info(`Copied ${els.length} item${els.length === 1 ? '' : 's'}`);
}

export function cutSelection() {
  const state = useBoard.getState();
  const els = Array.from(state.selection).map((id) => state.elements[id]).filter(Boolean) as QElement[];
  if (els.length === 0) return;
  copySelection();
  void state.commitTransaction(els.map((e) => deleteOp(e)));
  state.clearSelection();
}

// pasteAt recreates the clipboard content at a canvas point. Order of
// preference: an image on the system clipboard, then QomraNote JSON, then a
// URL/text, then the internal buffer.
export async function pasteAt(x: number, y: number) {
  const state = useBoard.getState();
  if (state.readOnly) return;

  // 1) System clipboard: images and text.
  try {
    const items = await navigator.clipboard?.read?.();
    if (items) {
      for (const item of items) {
        const imgType = item.types.find((t) => t.startsWith('image/'));
        if (imgType) {
          const blob = await item.getType(imgType);
          const file = new File([blob], `pasted-${Date.now()}.png`, { type: imgType });
          const { url, attachmentId } = await uploadFile(file);
          await state.commitTransaction([
            createOp('IMAGE', state.boardId, { position: { x, y }, width: 280, content: { url, attachmentId, caption: '' } }),
          ]);
          return;
        }
      }
    }
  } catch { /* clipboard read blocked — fall through */ }

  // 2) System clipboard text: QomraNote JSON, URL, or plain text.
  try {
    const text = await navigator.clipboard?.readText?.();
    if (text) {
      const asJson = tryParseBuffer(text);
      if (asJson) { await recreate(asJson, x, y); return; }
      if (/^https?:\/\//.test(text.trim())) {
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
      if (text.trim() && buffer.length === 0) {
        await state.commitTransaction([
          createOp('CARD', state.boardId, {
            position: { x, y }, width: 300,
            content: { textPreview: text.slice(0, 500), doc: { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text }] }] } },
          }),
        ]);
        return;
      }
    }
  } catch { /* ignore */ }

  // 3) Internal buffer.
  if (buffer.length) await recreate(buffer, x, y);
}

function tryParseBuffer(text: string): QElement[] | null {
  try {
    const parsed = JSON.parse(text);
    if (parsed && Array.isArray(parsed[MIME])) return parsed[MIME] as QElement[];
  } catch { /* not ours */ }
  return null;
}

// recreate deep-clones a set of elements into the current board at a point,
// remapping ids (and internal parent refs for copied containers).
async function recreate(els: QElement[], x: number, y: number) {
  const state = useBoard.getState();
  if (els.length === 0) return;
  const minX = Math.min(...els.map((e) => e.location.position.x));
  const minY = Math.min(...els.map((e) => e.location.position.y));
  const idMap = new Map<string, string>();
  els.forEach((e) => idMap.set(e.id, newObjectId()));

  const ops: Op[] = els.map((e) => {
    const parentId = idMap.get(e.location.parentId) ?? state.boardId;
    return {
      elementId: idMap.get(e.id)!,
      action: 'create',
      changes: {
        type: e.type,
        location: {
          parentId,
          section: 'CANVAS',
          position: { x: x + (e.location.position.x - minX), y: y + (e.location.position.y - minY) },
          index: e.location.index,
          width: e.location.width,
          height: 0,
        },
        content: structuredClone(e.content),
      },
      undoChanges: {},
    };
  });
  await state.commitTransaction(ops);
  toast.success(`Pasted ${ops.length} item${ops.length === 1 ? '' : 's'}`);
}

export function clipboardHasContent(): boolean {
  return buffer.length > 0;
}
