// The infinite canvas (§3.5): pan (wheel / middle-drag / space-drag), zoom
// (Ctrl+wheel toward the cursor, Z fits all with a GSAP tween), marquee
// multi-select, double-click note creation, file-drop uploads, whiteboard
// draw mode (§4.13), live remote cursors, and the SVG line layer.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import gsap from 'gsap';
import { api, uploadFile } from '../api/client';
import type { QElement } from '../api/types';
import { sendCursor } from '../realtime/socket';
import { createOp, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { ElementShell } from './ElementShell';
import { LineLayer } from './LineLayer';
import { FitIcon, MinusIcon, NoteIcon, BoardIcon, PlusIcon } from '../components/Icons';
import { useContextMenu } from '../components/ui/ContextMenu';
import { pasteAt } from '../store/clipboard';

interface Props { navigate: (boardId: string) => Promise<void> }

export function BoardCanvas({ navigate }: Props) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const { boardId, elements, commitTransaction, clearSelection, select, presence } = useBoard();
  const { panX, panY, scale, setView, lineMode, drawMode, toCanvas } = useView();
  const [marquee, setMarquee] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null);
  const [drawStroke, setDrawStroke] = useState<number[][] | null>(null);
  const panDrag = useRef<{ startX: number; startY: number; panX: number; panY: number } | null>(null);
  const spaceDown = useRef(false);

  const canvasElements = useMemo(
    () =>
      Object.values(elements).filter(
        (el) =>
          el.location.parentId === boardId &&
          el.location.section === 'CANVAS' &&
          !el.deletedAt &&
          el.type !== 'LINE',
      ),
    [elements, boardId],
  );

  // ---- zoom & pan ----

  const applyZoom = useCallback((factor: number, cx?: number, cy?: number) => {
    const v = useView.getState();
    const viewport = viewportRef.current;
    if (!viewport) return;
    const px = cx ?? viewport.clientWidth / 2;
    const py = cy ?? viewport.clientHeight / 2;
    const next = Math.min(3, Math.max(0.15, v.scale * factor));
    const wx = (px - v.panX) / v.scale;
    const wy = (py - v.panY) / v.scale;
    setView(px - wx * next, py - wy * next, next);
  }, [setView]);

  const onWheel = useCallback((e: React.WheelEvent) => {
    const rect = viewportRef.current!.getBoundingClientRect();
    if (e.ctrlKey || e.metaKey) {
      applyZoom(e.deltaY < 0 ? 1.12 : 0.89, e.clientX - rect.left, e.clientY - rect.top);
    } else {
      const v = useView.getState();
      setView(v.panX - e.deltaX, v.panY - e.deltaY, v.scale);
    }
  }, [applyZoom, setView]);

  const fitAll = useCallback(() => {
    const els = canvasElements;
    const viewport = viewportRef.current;
    if (!viewport || els.length === 0) return;
    const sizes = useView.getState().sizes;
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const el of els) {
      const s = sizes[el.id] ?? { w: el.location.width || 260, h: 140 };
      minX = Math.min(minX, el.location.position.x);
      minY = Math.min(minY, el.location.position.y);
      maxX = Math.max(maxX, el.location.position.x + s.w);
      maxY = Math.max(maxY, el.location.position.y + s.h);
    }
    const pad = 80;
    const vw = viewport.clientWidth, vh = viewport.clientHeight;
    const target = Math.min(3, Math.max(0.15, Math.min(vw / (maxX - minX + pad * 2), vh / (maxY - minY + pad * 2))));
    const tx = (vw - (maxX - minX) * target) / 2 - minX * target;
    const ty = (vh - (maxY - minY) * target) / 2 - minY * target;
    const from = { x: panX, y: panY, k: scale };
    gsap.to(from, {
      x: tx, y: ty, k: target, duration: 0.45, ease: 'power3.out',
      onUpdate: () => setView(from.x, from.y, from.k),
    });
  }, [canvasElements, panX, panY, scale, setView]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      if (inEditor) return;
      if (e.key === ' ') spaceDown.current = true;
      if (e.key.toLowerCase() === 'z' && !e.ctrlKey && !e.metaKey) fitAll();
    };
    const onKeyUp = (e: KeyboardEvent) => { if (e.key === ' ') spaceDown.current = false; };
    window.addEventListener('keydown', onKey);
    window.addEventListener('keyup', onKeyUp);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener('keyup', onKeyUp); };
  }, [fitAll]);

  // ---- pointer interactions on empty canvas ----

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    const viewport = viewportRef.current!;

    // Pointer capture keeps the gesture alive outside the viewport; guard it
    // because exotic pointer ids (tests, some pens) can reject capture.
    const capture = () => { try { viewport.setPointerCapture(e.pointerId); } catch { /* non-fatal */ } };

    // Draw mode captures strokes anywhere, including over cards (§4.13).
    if (drawMode && e.button === 0) {
      const pt = toCanvas(e.clientX, e.clientY, viewport);
      setDrawStroke([[pt.x, pt.y]]);
      capture();
      return;
    }

    if (e.target !== e.currentTarget) return; // element shells handle their own
    if (e.button === 1 || spaceDown.current) {
      panDrag.current = { startX: e.clientX, startY: e.clientY, panX, panY };
      capture();
      return;
    }
    if (e.button === 0) {
      clearSelection();
      const pt = toCanvas(e.clientX, e.clientY, viewport);
      setMarquee({ x0: pt.x, y0: pt.y, x1: pt.x, y1: pt.y });
      capture();
    }
  }, [panX, panY, clearSelection, toCanvas, drawMode]);

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    const viewport = viewportRef.current!;
    const pt = toCanvas(e.clientX, e.clientY, viewport);
    useView.setState({ lastPointer: pt });
    sendCursor(pt.x, pt.y);
    if (drawStroke) {
      setDrawStroke([...drawStroke, [pt.x, pt.y]]);
      return;
    }
    if (panDrag.current) {
      setView(
        panDrag.current.panX + (e.clientX - panDrag.current.startX),
        panDrag.current.panY + (e.clientY - panDrag.current.startY),
        scale,
      );
      return;
    }
    if (marquee) setMarquee({ ...marquee, x1: pt.x, y1: pt.y });
  }, [marquee, drawStroke, scale, setView, toCanvas]);

  const onPointerUp = useCallback(() => {
    panDrag.current = null;

    // Finish a draw-mode stroke: it becomes a SKETCH element at its bounds.
    if (drawStroke) {
      if (drawStroke.length > 2) {
        const xs = drawStroke.map((p) => p[0]);
        const ys = drawStroke.map((p) => p[1]);
        const pad = 8;
        const minX = Math.min(...xs) - pad, minY = Math.min(...ys) - pad;
        const w = Math.max(...xs) - minX + pad, h = Math.max(...ys) - minY + pad;
        const points = drawStroke.map(([x, y]) => [x - minX, y - minY]);
        void commitTransaction([
          createOp('SKETCH', boardId, {
            position: { x: minX, y: minY },
            width: w,
            content: { strokes: [{ points, color: '#1d1d1f', width: 2.5 }], canvasW: w, canvasH: h },
          }),
        ]);
      }
      setDrawStroke(null);
      return;
    }

    if (marquee) {
      const [mx0, mx1] = [Math.min(marquee.x0, marquee.x1), Math.max(marquee.x0, marquee.x1)];
      const [my0, my1] = [Math.min(marquee.y0, marquee.y1), Math.max(marquee.y0, marquee.y1)];
      if (mx1 - mx0 > 6 || my1 - my0 > 6) {
        const sizes = useView.getState().sizes;
        const hit = canvasElements
          .filter((el) => {
            const s = sizes[el.id] ?? { w: el.location.width || 260, h: 120 };
            const { x, y } = el.location.position;
            return x < mx1 && x + s.w > mx0 && y < my1 && y + s.h > my0;
          })
          .map((el) => el.id);
        if (hit.length) select(hit);
      }
      setMarquee(null);
    }
  }, [marquee, drawStroke, canvasElements, select, boardId, commitTransaction]);

  const onDoubleClick = useCallback((e: React.MouseEvent) => {
    if (e.target !== e.currentTarget || drawMode) return;
    const pt = toCanvas(e.clientX, e.clientY, viewportRef.current!);
    const op = createOp('CARD', boardId, {
      position: { x: pt.x, y: pt.y }, width: 300,
      content: { doc: null, textPreview: '' },
    });
    void commitTransaction([op]);
    useView.getState().setEditing(op.elementId);
  }, [boardId, commitTransaction, toCanvas, drawMode]);

  // ---- drop: OS files become IMAGE/FILE cards; URLs become link cards ----
  const onDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    const pt = toCanvas(e.clientX, e.clientY, viewportRef.current!);
    const files = Array.from(e.dataTransfer.files ?? []);
    let offset = 0;
    for (const file of files) {
      try {
        const { url, attachmentId } = await uploadFile(file);
        const isImage = file.type.startsWith('image/');
        const op = createOp(isImage ? 'IMAGE' : 'FILE', boardId, {
          position: { x: pt.x + offset, y: pt.y + offset },
          width: isImage ? 280 : 0,
          content: isImage
            ? { url, attachmentId, caption: '' }
            : { url, attachmentId, filename: file.name, mimeType: file.type, size: file.size },
        });
        await commitTransaction([op]);
        offset += 28;
      } catch (err) {
        console.error('upload failed', err);
      }
    }
    const uri = e.dataTransfer.getData('text/uri-list') || e.dataTransfer.getData('text/plain');
    if (files.length === 0 && uri && /^https?:\/\//.test(uri.trim())) {
      const meta = await api.resolveLink(uri.trim()).catch(() => null);
      const op = createOp('LINK', boardId, {
        position: pt, width: 260,
        content: meta
          ? { url: meta.url, title: meta.title, description: meta.description, thumbnailUrl: meta.thumbnailUrl, embedType: meta.embedType, showPreview: true, showDescription: true }
          : { url: uri.trim(), title: uri.trim(), showPreview: false, showDescription: false },
      });
      await commitTransaction([op]);
    }
  }, [boardId, commitTransaction, toCanvas]);

  // Right-click empty canvas → paste / select-all / new here.
  const onContextMenu = useCallback((e: React.MouseEvent) => {
    if (e.target !== e.currentTarget) return; // element shells open their own
    e.preventDefault();
    const state = useBoard.getState();
    if (state.readOnly) return;
    const pt = toCanvas(e.clientX, e.clientY, viewportRef.current!);
    useContextMenu.getState().open(e.clientX, e.clientY, [
      { label: 'New note here', icon: <NoteIcon size={15} />, onClick: () => {
        const op = createOp('CARD', boardId, { position: pt, width: 300, content: { doc: null, textPreview: '' } });
        void commitTransaction([op]);
        useView.getState().setEditing(op.elementId);
      } },
      { label: 'New board here', icon: <BoardIcon size={15} />, onClick: () => {
        void commitTransaction([createOp('BOARD', boardId, { position: pt, content: { title: 'New board' } })]);
      } },
      { label: 'Paste', onClick: () => void pasteAt(pt.x, pt.y), divider: true },
      { label: 'Select all', onClick: () => select(Object.values(useBoard.getState().elements).filter((el) => el.location.parentId === boardId && !el.deletedAt && el.type !== 'LINE').map((el) => el.id)) },
    ]);
  }, [boardId, commitTransaction, select, toCanvas]);

  const remoteCursors = Object.values(presence).filter((p) => p.cursor);
  const modeClass = drawMode ? ' draw-mode' : lineMode ? ' line-mode' : '';

  return (
    <div
      ref={viewportRef}
      className={`canvas-viewport${modeClass}`}
      style={{ backgroundPosition: `${panX}px ${panY}px`, backgroundSize: `${26 * scale}px ${26 * scale}px` }}
      onWheel={onWheel}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onDoubleClick={onDoubleClick}
      onContextMenu={onContextMenu}
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
    >
      <div className="canvas-layer" style={{ transform: `translate(${panX}px, ${panY}px) scale(${scale})` }}>
        <LineLayer />
        {canvasElements.map((el) => (
          <ElementShell key={el.id} element={el} navigate={navigate} viewportRef={viewportRef} />
        ))}
        {drawStroke && (
          <svg style={{ position: 'absolute', left: 0, top: 0, overflow: 'visible', pointerEvents: 'none' }}>
            <polyline
              points={drawStroke.map((p) => p.join(',')).join(' ')}
              fill="none" stroke="#1d1d1f" strokeWidth={2.5}
              strokeLinecap="round" strokeLinejoin="round"
            />
          </svg>
        )}
        {remoteCursors.map((p) => (
          <div key={p.clientId} className="remote-cursor" style={{ transform: `translate(${p.cursor!.x}px, ${p.cursor!.y}px)` }}>
            <div className="dot" />
            <div className="name">{p.name || 'Guest'}</div>
          </div>
        ))}
        {marquee && (
          <div
            className="marquee"
            style={{
              left: Math.min(marquee.x0, marquee.x1),
              top: Math.min(marquee.y0, marquee.y1),
              width: Math.abs(marquee.x1 - marquee.x0),
              height: Math.abs(marquee.y1 - marquee.y0),
            }}
          />
        )}
      </div>

      {(lineMode || drawMode) && (
        <div className="mode-banner">
          {lineMode ? 'Click two cards to connect them' : 'Draw anywhere on the board'}
          <button onClick={() => { useView.getState().setLineMode(false); useView.getState().setDrawMode(false); }}>
            Done · Esc
          </button>
        </div>
      )}

      <div className="zoom-cluster" onPointerDown={(e) => e.stopPropagation()}>
        <button onClick={() => applyZoom(0.85)} title="Zoom out"><MinusIcon size={15} /></button>
        <div className="zoom-value">{Math.round(scale * 100)}%</div>
        <button onClick={() => applyZoom(1.18)} title="Zoom in"><PlusIcon size={15} /></button>
        <button onClick={fitAll} title="Fit all (Z)"><FitIcon size={15} /></button>
      </div>

      {canvasElements.length === 0 && !drawMode && (
        <div className="hint-pill">Double-click anywhere to add a note · drop images to upload · Ctrl+scroll to zoom</div>
      )}
    </div>
  );
}

export type { QElement };
