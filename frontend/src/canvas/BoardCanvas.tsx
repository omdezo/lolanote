// The infinite canvas (§3.5): pan (wheel / middle-drag / space-drag), zoom
// (Ctrl+wheel toward the cursor, Z fits all with a GSAP tween), marquee
// multi-select, double-click note creation, file-drop uploads, whiteboard
// draw mode (§4.13), live remote cursors, and the SVG line layer.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { tweenValues } from '../lib/motion';
import { readSavedView, saveView } from './savedView';
import { canvasHasFocus, singleKeyEnabled } from '../lib/shortcuts';
import { api, uploadFile } from '../api/client';
import type { QElement } from '../api/types';
import { sendCursor } from '../realtime/socket';
import { createOp, moveOp, useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { setLastPointer, useView } from '../store/viewStore';
import { simplifyStroke, type StrokePoint } from '../lib/stroke';
import { nextInDirection, readingOrder, type Direction, type NavBox } from './spatialNav';
import { ElementShell } from './ElementShell';
import { LineLayer } from './LineLayer';
import { CloseIcon, FitIcon, MinusIcon, NoteIcon, BoardIcon, PlusIcon, SparkleIcon } from '../components/Icons';
import { presenceColor } from '../components/Topbar';
import { useT } from '../i18n';
import { useContextMenu } from '../components/ui/ContextMenu';
import { pasteAt } from '../store/clipboard';
import { useLabels } from '../store/labels';
import { GhostLayer, useProposedIds } from '../agent/GhostLayer';
import { useAgent } from '../agent/agentStore';

interface Props { navigate: (boardId: string) => Promise<void> }

/** One keyboard nudge, matching the grid the snap preference already rounds to
 *  — so a nudged card lands where a dragged one would. */
const NUDGE = 20;

export function BoardCanvas({ navigate }: Props) {
  const t = useT();
  const viewportRef = useRef<HTMLDivElement>(null);
  // Field selectors throughout. The viewport used to subscribe to both stores
  // wholesale AND write `lastPointer` into one of them on every pointermove, so
  // moving the mouse across empty canvas reconciled every memo'd child at
  // pointer rate — with nothing on the board changing and nothing on screen
  // reading the value being written.
  const boardId = useBoard((s) => s.boardId);
  const elements = useBoard((s) => s.elements);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const clearSelection = useBoard((s) => s.clearSelection);
  const select = useBoard((s) => s.select);
  const panX = useView((s) => s.panX);
  const panY = useView((s) => s.panY);
  const scale = useView((s) => s.scale);
  const setView = useView((s) => s.setView);
  const drawMode = useView((s) => s.drawMode);
  const toCanvas = useView((s) => s.toCanvas);
  const [marquee, setMarquee] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null);
  /**
   * The stroke being drawn right now, and its mirror for painting.
   *
   * The ref is the truth and the state is a copy taken once per frame. It used
   * to be state alone, appended with `setDrawStroke([...drawStroke, pt])` on
   * every pointermove — an O(n²) copy that got slower the longer you drew, so
   * the felt behaviour of the drawing tool degraded within one stroke.
   */
  const strokeRef = useRef<StrokePoint[] | null>(null);
  const strokeFrame = useRef<number | null>(null);
  const [drawStroke, setDrawStroke] = useState<StrokePoint[] | null>(null);
  const panDrag = useRef<{ startX: number; startY: number; panX: number; panY: number } | null>(null);
  const spaceDown = useRef(false);
  const marqueeMode = useView((s) => s.marqueeMode);

  /**
   * Every pointer currently down on the viewport, in client space.
   *
   * MO1. The canvas started a pan only on `e.button === 1 || spaceDown`, and
   * `.canvas-viewport { touch-action: none }` is an ACTIVE instruction to
   * suppress the browser's own pan and pinch — with nothing replacing them. So
   * a phone user saw whatever region the last desktop session left and was
   * architecturally unable to reach anything else: no scrollbars, no swipe, no
   * fallback. A filmmaker opening a shoot board on location could read four
   * cards and could not reach the fifth.
   *
   * Arbitration is by count and pointer type: one touch on empty canvas pans,
   * two pinch about their midpoint. Mouse and pen keep today's semantics
   * exactly. The invariant that matters is that `toCanvas` and `setView` stay
   * the only writers of pan/scale — the line layer, the ghosts, the remote
   * cursors and `visibleRegion()` all follow for free because of it.
   */
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ dist: number; scale: number } | null>(null);
  /** A tap that has not moved yet, so pointerup can tell tap from pan. */
  const tapStart = useRef<{ x: number; y: number } | null>(null);

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

  /**
   * Zoom to an absolute scale about a screen point.
   *
   * Pinch needs a continuous factor rather than applyZoom's fixed steps, and it
   * must anchor on the midpoint between the two fingers rather than on the
   * cursor — but the world-space maths is the same maths, so it lives here once
   * and applyZoom is expressed in terms of it.
   */
  const zoomTo = useCallback((next: number, px: number, py: number) => {
    const v = useView.getState();
    const k = Math.min(3, Math.max(0.15, next));
    const wx = (px - v.panX) / v.scale;
    const wy = (py - v.panY) / v.scale;
    setView(px - wx * k, py - wy * k, k);
  }, [setView]);

  const onWheel = useCallback((e: React.WheelEvent) => {
    const rect = viewportRef.current!.getBoundingClientRect();
    // Preference: plain wheel pans (default) or zooms; Ctrl/⌘ always zooms.
    const wheelZooms = useSettings.getState().settings.preferences.wheelMode === 'zoom';
    if (e.ctrlKey || e.metaKey || (wheelZooms && !e.shiftKey && e.deltaX === 0)) {
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
    // Reduced motion jumps to the frame rather than losing it: fit-all is an
    // ANSWER to "where is everything", and an answer that does not arrive is
    // not a kinder answer. See lib/motion.ts.
    tweenValues(from, { x: tx, y: ty, k: target }, {
      duration: 0.45, ease: 'power3.out',
      onUpdate: () => setView(from.x, from.y, from.k),
    });
  }, [canvasElements, panX, panY, scale, setView]);

  // WHERE YOU LAND WHEN A BOARD OPENS.
  //
  // Nothing framed anything. The view defaults to (0,0) at 100%, nothing was
  // ever persisted, and fitAll was reachable only by pressing Z — so opening a
  // board or refreshing the page dropped you at the world origin, which is
  // wherever the coordinate space happens to start and almost never where your
  // work is. On a board the agent had built, content sits wherever the packer
  // put it, and the answer to "open my board" was an empty grey field.
  //
  // Two behaviours, and the distinction is the point:
  //   - RETURNING to a board you have positioned yourself on restores exactly
  //     that. A refresh mid-thought must not re-frame and lose your place.
  //   - Arriving somewhere for the FIRST time frames the content, because
  //     "where is everything" is the question an opening board is answering.
  const framed = useRef<string | null>(null);
  useEffect(() => {
    if (!boardId || canvasElements.length === 0) return;
    if (framed.current === boardId) return;      // already answered for this board
    framed.current = boardId;

    const saved = readSavedView(boardId);
    if (saved) {
      setView(saved.panX, saved.panY, saved.scale);
      return;
    }
    // One frame's grace so the cards have measured themselves; fitAll reads
    // those sizes, and fitting against defaults puts everything slightly wrong.
    const id = requestAnimationFrame(() => fitAll());
    return () => cancelAnimationFrame(id);
  }, [boardId, canvasElements.length, fitAll, setView]);

  // Remember where you left off, per board. Written on a debounce because a
  // pan is a hundred state changes and localStorage is synchronous.
  useEffect(() => {
    if (!boardId) return;
    const id = window.setTimeout(() => saveView(boardId, { panX, panY, scale }), 400);
    return () => window.clearTimeout(id);
  }, [boardId, panX, panY, scale]);

  // AX20, WCAG 2.1.4. `z` and Space were single-character shortcuts on a
  // `window` listener guarded only by "are you in a text field" — not
  // remappable, not disableable, not focus-scoped, which is all three escapes
  // the criterion offers missed at once. Every stray character a speech-input
  // user emits re-framed and re-zoomed the entire board; and because Space is
  // the standard activation key for a button, `spaceDown` flipped true whenever
  // anybody activated any focused control anywhere on the page, arming a pan
  // that then started on the next click.
  //
  // Three fixes, and each is a separate limb of the criterion: they are scoped
  // to canvas focus, they can be switched off in Settings → Accessibility, and
  // fit-all gained a modifier combo (Shift+1) that is always available.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const inEditor = (e.target as HTMLElement)?.closest?.('input, textarea, [contenteditable="true"]');
      if (inEditor) return;
      // Shift+1 is not a single-character shortcut, so it is never scoped away
      // and never disabled — the keyboard route to fit-all always exists.
      if (e.key === '!' || (e.key === '1' && e.shiftKey)) { e.preventDefault(); fitAll(); return; }
      if (!singleKeyEnabled() || !canvasHasFocus()) return;
      if (e.key === ' ') spaceDown.current = true;
      if (e.key.toLowerCase() === 'z' && !e.ctrlKey && !e.metaKey) fitAll();
    };
    // Key-up is unconditional on purpose: if the guard flips between down and
    // up — focus moves, the setting changes — a one-way `true` would leave the
    // canvas armed to pan forever.
    const onKeyUp = (e: KeyboardEvent) => { if (e.key === ' ') spaceDown.current = false; };
    window.addEventListener('keydown', onKey);
    window.addEventListener('keyup', onKeyUp);
    return () => { window.removeEventListener('keydown', onKey); window.removeEventListener('keyup', onKeyUp); };
  }, [fitAll]);

  // ---- keyboard navigation of the board itself (AX1) ----

  /**
   * Every shell currently on screen, measured.
   *
   * Read from the DOM rather than from the element map because the map does not
   * know what is rendered: a card inside a collapsed column, or inside a column
   * at all, is a shell too, and the person tabbing through the board should
   * reach it exactly when they can see it. `getBoundingClientRect` is in screen
   * space, which is the right space for "which card is to the right of this
   * one" — the answer must match what the person is looking at, at whatever
   * zoom they are looking at it.
   */
  const shellBoxes = useCallback((): NavBox[] => {
    const layer = viewportRef.current?.querySelector('.canvas-layer');
    if (!layer) return [];
    return [...layer.querySelectorAll<HTMLElement>('.el[data-element-id]')].map((node) => {
      const r = node.getBoundingClientRect();
      return { id: node.getAttribute('data-element-id')!, x: r.left, y: r.top, w: r.width, h: r.height };
    });
  }, []);

  /**
   * Put focus on a card and, if it is off screen, bring the board to it.
   *
   * Not `scrollIntoView`: this viewport does not scroll — the canvas pans, and
   * the body is `overflow: hidden`. Arrowing to a card outside the viewport
   * would otherwise move focus to something the person cannot see, which is the
   * one failure mode a keyboard grid must not have.
   */
  const focusShell = useCallback((id: string) => {
    const viewport = viewportRef.current;
    const node = viewport?.querySelector<HTMLElement>(`.el[data-element-id="${id}"]`);
    if (!viewport || !node) return;
    useView.getState().setFocused(id);
    node.focus();

    const view = viewport.getBoundingClientRect();
    const box = node.getBoundingClientRect();
    const pad = 40;
    let dx = 0, dy = 0;
    if (box.left < view.left + pad) dx = view.left + pad - box.left;
    else if (box.right > view.right - pad) dx = view.right - pad - box.right;
    if (box.top < view.top + pad) dy = view.top + pad - box.top;
    else if (box.bottom > view.bottom - pad) dy = view.bottom - pad - box.bottom;
    if (dx || dy) {
      const v = useView.getState();
      v.setView(v.panX + dx, v.panY + dy, v.scale);
    }
  }, []);

  // Keep exactly one card in the tab order. When the board changes under it —
  // the focused card deleted, filed into a column, or the board swapped
  // entirely — the roving index would otherwise point at nothing and the whole
  // canvas would drop out of the tab order.
  useEffect(() => {
    const boxes = shellBoxes();
    const current = useView.getState().focusedId;
    if (boxes.length === 0) {
      if (current) useView.getState().setFocused(null);
      return;
    }
    if (!current || !boxes.some((b) => b.id === current)) {
      useView.getState().setFocused(readingOrder(boxes)[0].id);
    }
  }, [elements, shellBoxes]);

  /**
   * Arrows move; Ctrl+arrow nudges.
   *
   * The split matters: arrows cannot express a drag, so the keyboard's move is
   * by command rather than by pixel — one grid step, through the same
   * transaction path the pointer uses, so it is undoable and syncs like any
   * other edit. Everything larger than a nudge (into a column, onto a board,
   * connect to that card) is a typed verb the agent already has, which is the
   * point: the keyboard path and the agent path are the same path.
   */
  const onCanvasKeyDown = useCallback((e: React.KeyboardEvent) => {
    const target = e.target as HTMLElement;
    if (target.closest('input, textarea, select, [contenteditable="true"]')) return;
    const shell = target.closest('.el[data-element-id]') as HTMLElement | null;
    const fromId = shell?.getAttribute('data-element-id') ?? useView.getState().focusedId;
    if (!fromId) return;

    const DIRS: Record<string, Direction> = {
      ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down',
    };
    const dir = DIRS[e.key];

    if (dir && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      const state = useBoard.getState();
      if (state.readOnly) return;
      const ids = state.selection.has(fromId) && state.selection.size > 1
        ? Array.from(state.selection) : [fromId];
      const dx = dir === 'left' ? -NUDGE : dir === 'right' ? NUDGE : 0;
      const dy = dir === 'up' ? -NUDGE : dir === 'down' ? NUDGE : 0;
      const ops = ids
        .map((id) => state.elements[id])
        .filter((el): el is QElement =>
          // Only things with a coordinate of their own: a card inside a column
          // is ordered, not placed, and nudging it would write a position
          // nothing reads.
          !!el && !el.content?.locked && el.location.parentId === state.boardId)
        .map((el) => moveOp(el, {
          position: { x: el.location.position.x + dx, y: el.location.position.y + dy },
        }));
      if (ops.length) void state.commitTransaction(ops);
      return;
    }

    if (dir) {
      e.preventDefault();
      const next = nextInDirection(shellBoxes(), fromId, dir);
      if (next) focusShell(next);
      return;
    }

    if (e.key === 'Home' || e.key === 'End') {
      e.preventDefault();
      const order = readingOrder(shellBoxes());
      const pick = e.key === 'Home' ? order[0] : order[order.length - 1];
      if (pick) focusShell(pick.id);
      return;
    }

    // Space toggles this card's membership of the selection — the gesture the
    // pointer spells as shift-click, and the precondition for the floating
    // action bar, the context menu and every multi-card verb.
    if (e.key === ' ' && shell) {
      e.preventDefault();
      const state = useBoard.getState();
      if (state.selection.has(fromId)) {
        const rest = Array.from(state.selection).filter((id) => id !== fromId);
        state.select(rest);
      } else {
        state.select([fromId], true);
      }
    }
  }, [shellBoxes, focusShell]);

  // ---- pointer interactions on empty canvas ----

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    const viewport = viewportRef.current!;
    const coarse = e.pointerType === 'touch';

    // Pointer capture keeps the gesture alive outside the viewport; guard it
    // because exotic pointer ids (tests, some pens) can reject capture.
    const capture = () => { try { viewport.setPointerCapture(e.pointerId); } catch { /* non-fatal */ } };

    if (coarse) {
      pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
      // Second finger down: whatever the first one had started, abandon it
      // without committing and pinch instead. Abandoning rather than finishing
      // is the point — a half-drawn marquee that commits as the second finger
      // lands is a selection nobody asked for.
      if (pointers.current.size === 2) {
        panDrag.current = null;
        tapStart.current = null;
        setMarquee(null);
        strokeRef.current = null;
        setDrawStroke(null);
        const [a, b] = [...pointers.current.values()];
        pinch.current = { dist: Math.hypot(a.x - b.x, a.y - b.y) || 1, scale: useView.getState().scale };
        return;
      }
      if (pointers.current.size > 2) return; // a third finger is not a gesture
    }

    // Draw mode captures strokes anywhere, including over cards (§4.13).
    if (drawMode && e.button === 0) {
      const pt = toCanvas(e.clientX, e.clientY, viewport);
      strokeRef.current = [[pt.x, pt.y]];
      setDrawStroke(strokeRef.current);
      capture();
      return;
    }

    if (e.target !== e.currentTarget) return; // element shells handle their own
    if (e.button === 1 || spaceDown.current) {
      panDrag.current = { startX: e.clientX, startY: e.clientY, panX, panY };
      capture();
      return;
    }
    if (e.button !== 0) return;

    // MO2. On touch, the slot pan should occupy was occupied by the most
    // surprising alternative: one-finger drag on empty canvas rubber-band
    // selected, so trying to look around selected thirty cards and offered you
    // a Delete button. Marquee is still there — it is now a mode you turn on,
    // beside Draw. Deselect-on-tap stays, and is what makes the mode legible.
    if (coarse && !marqueeMode) {
      panDrag.current = { startX: e.clientX, startY: e.clientY, panX, panY };
      tapStart.current = { x: e.clientX, y: e.clientY };
      capture();
      return;
    }

    clearSelection();
    const pt = toCanvas(e.clientX, e.clientY, viewport);
    setMarquee({ x0: pt.x, y0: pt.y, x1: pt.x, y1: pt.y });
    capture();
  }, [panX, panY, clearSelection, toCanvas, drawMode, marqueeMode]);

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    const viewport = viewportRef.current!;

    if (e.pointerType === 'touch' && pointers.current.has(e.pointerId)) {
      pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pinch.current && pointers.current.size === 2) {
        const [a, b] = [...pointers.current.values()];
        const dist = Math.hypot(a.x - b.x, a.y - b.y) || 1;
        const rect = viewport.getBoundingClientRect();
        // Anchor on the midpoint, in viewport-local coordinates, so the two
        // fingers stay over the same board content while the scale changes.
        zoomTo(
          pinch.current.scale * (dist / pinch.current.dist),
          (a.x + b.x) / 2 - rect.left,
          (a.y + b.y) / 2 - rect.top,
        );
        return;
      }
    }

    const pt = toCanvas(e.clientX, e.clientY, viewport);
    setLastPointer(pt.x, pt.y);
    sendCursor(pt.x, pt.y);
    // Past a finger's slop this is a pan, not a tap, so pointerup must not also
    // clear the selection.
    if (tapStart.current && Math.hypot(e.clientX - tapStart.current.x, e.clientY - tapStart.current.y) > 8) {
      tapStart.current = null;
    }
    if (strokeRef.current) {
      strokeRef.current.push([pt.x, pt.y]);
      // One repaint per frame, not one per sample. The array itself is mutated
      // in place; the copy handed to React is what makes it a new value.
      if (strokeFrame.current === null) {
        strokeFrame.current = requestAnimationFrame(() => {
          strokeFrame.current = null;
          if (strokeRef.current) setDrawStroke(strokeRef.current.slice());
        });
      }
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
  }, [marquee, scale, setView, toCanvas]);

  /**
   * End whatever gesture is in flight.
   *
   * MO4. `pointercancel` is a mouse-era non-event and a touch-era certainty —
   * the browser fires it whenever it takes the gesture over: a system edge
   * swipe, an incoming call, the Android back gesture, an assistive overlay.
   * When it fires, `pointerup` never comes. Nothing anywhere in this codebase
   * registered it, so `panDrag`, `marquee`, `drawStroke`, `useView.drag` and
   * `lineDraft` all stayed set with their window listeners still bound, and the
   * canvas painted a frozen marquee or a card stuck under an invisible finger
   * until the page was reloaded.
   *
   * The invariant is the second half and the more important one: CANCELLATION
   * NEVER COMMITS A TRANSACTION. A lost gesture is a lost gesture; a phantom
   * edit in the undo history is a bug you find weeks later.
   */
  const endGesture = useCallback((e: React.PointerEvent, cancelled: boolean) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
    const wasTap = !!tapStart.current;
    tapStart.current = null;
    panDrag.current = null;

    // Tap on empty canvas deselects. It is what the marquee mode toggle reads
    // against — without it, turning selection off would also take away the way
    // to clear one.
    if (!cancelled && wasTap && e.target === e.currentTarget) clearSelection();

    // Finish a draw-mode stroke: it becomes a SKETCH element at its bounds.
    const raw = strokeRef.current;
    if (raw) {
      strokeRef.current = null;
      if (strokeFrame.current !== null) {
        cancelAnimationFrame(strokeFrame.current);
        strokeFrame.current = null;
      }
      if (!cancelled && raw.length > 2) {
        // Bounds off the RAW samples so the box is exactly what the person
        // drew; the stored geometry is the reduced one. Reducing first and
        // measuring after would shave the extremes off the frame.
        const xs = raw.map((p) => p[0]);
        const ys = raw.map((p) => p[1]);
        const pad = 8;
        const minX = Math.min(...xs) - pad, minY = Math.min(...ys) - pad;
        const w = Math.max(...xs) - minX + pad, h = Math.max(...ys) - minY + pad;
        const points = simplifyStroke(raw.map(([x, y]) => [x - minX, y - minY]));
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
      if (!cancelled && (mx1 - mx0 > 6 || my1 - my0 > 6)) {
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
  }, [marquee, canvasElements, select, boardId, commitTransaction, clearSelection]);

  const onPointerUp = useCallback((e: React.PointerEvent) => endGesture(e, false), [endGesture]);
  const onPointerCancel = useCallback((e: React.PointerEvent) => endGesture(e, true), [endGesture]);

  const onDoubleClick = useCallback((e: React.MouseEvent) => {
    if (e.target !== e.currentTarget || drawMode) return;
    // Preference: double-click creates a note (default), a board, or nothing.
    const creates = useSettings.getState().settings.preferences.doubleClickCreates;
    if (creates === 'none') return;
    const pt = toCanvas(e.clientX, e.clientY, viewportRef.current!);
    const op = creates === 'board'
      ? createOp('BOARD', boardId, { position: { x: pt.x, y: pt.y }, content: { title: 'New board' } })
      : createOp('CARD', boardId, { position: { x: pt.x, y: pt.y }, width: 300, content: { doc: null, textPreview: '' } });
    void commitTransaction([op]);
    if (creates === 'note') useView.getState().setEditing(op.elementId);
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
      { label: t('menu.newNote'), icon: <NoteIcon size={15} />, onClick: () => {
        const op = createOp('CARD', boardId, { position: pt, width: 300, content: { doc: null, textPreview: '' } });
        void commitTransaction([op]);
        useView.getState().setEditing(op.elementId);
      } },
      { label: t('menu.newBoard'), icon: <BoardIcon size={15} />, onClick: () => {
        void commitTransaction([createOp('BOARD', boardId, { position: pt, content: { title: t('menu.newBoard') } })]);
      } },
      ...(useAgent.getState().capabilities?.enabled
        ? [{
            label: state.selection.size > 0
              ? `${t('menu.askAboutN')} ${state.selection.size}`
              : t('menu.ask'),
            icon: <SparkleIcon size={15} />,
            onClick: () => useAgent.getState().setOpen(true),
            divider: true,
          }]
        : []),
      { label: t('menu.paste'), onClick: () => void pasteAt(pt.x, pt.y), divider: true },
      { label: t('menu.selectAll'), onClick: () => select(Object.values(useBoard.getState().elements).filter((el) => el.location.parentId === boardId && !el.deletedAt && el.type !== 'LINE').map((el) => el.id)) },
    ]);
  }, [boardId, commitTransaction, select, toCanvas, t]);

  // Cards a pending proposal would move dim in place; nothing has changed yet.
  const proposedIds = useProposedIds();
  const modeClass = drawMode ? ' draw-mode' : '';

  return (
    <div
      ref={viewportRef}
      className={`canvas-viewport${modeClass}`}
      style={{ backgroundPosition: `${panX}px ${panY}px`, backgroundSize: `${26 * scale}px ${26 * scale}px` }}
      // A canvas is not a document, and announcing it as one is why arrow keys
      // were being eaten by the reader's own browse mode before they could ever
      // reach the roving grid. `application` hands the keys through; the
      // roledescription is what stops "application" from being all a person
      // hears about where they have landed.
      role="application"
      aria-roledescription={t('a11y.boardCanvas')}
      aria-label={`${t('a11y.boardCanvas')}. ${t('a11y.canvasHint')}`}
      onKeyDown={onCanvasKeyDown}
      onWheel={onWheel}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerCancel}
      onDoubleClick={onDoubleClick}
      onContextMenu={onContextMenu}
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
    >
      <div className="canvas-layer" style={{ transform: `translate(${panX}px, ${panY}px) scale(${scale})` }}>
        <LineLayer />
        {canvasElements.map((el) => (
          <ElementShell
            key={el.id}
            element={el}
            navigate={navigate}
            viewportRef={viewportRef}
            proposedKind={proposedIds.get(el.id)}
          />
        ))}
        <GhostLayer />
        {drawStroke && (
          <svg style={{ position: 'absolute', left: 0, top: 0, overflow: 'visible', pointerEvents: 'none' }}>
            <polyline
              points={drawStroke.map((p) => p.join(',')).join(' ')}
              fill="none" stroke="#1d1d1f" strokeWidth={2.5}
              strokeLinecap="round" strokeLinejoin="round"
            />
          </svg>
        )}
        <RemoteCursors />
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

      {drawMode && (
        <div className="mode-banner">
          {t('canvas.drawBanner')}
          <button onClick={() => useView.getState().setDrawMode(false)}>
            {t('canvas.drawDone')}
          </button>
        </div>
      )}

      <LabelFilterBar elements={canvasElements} />

      {/* With pinch gone this cluster and its Fit button are the entire
          remaining navigation surface on a phone, and every control in it was
          an icon with no accessible name. */}
      <div className="zoom-cluster" role="group" aria-label={t('a11y.fitAll')} onPointerDown={(e) => e.stopPropagation()}>
        <button onClick={() => applyZoom(0.85)} title={t('a11y.zoomOut')} aria-label={t('a11y.zoomOut')}><MinusIcon size={15} /></button>
        <div className="zoom-value">{Math.round(scale * 100)}%</div>
        <button onClick={() => applyZoom(1.18)} title={t('a11y.zoomIn')} aria-label={t('a11y.zoomIn')}><PlusIcon size={15} /></button>
        <button onClick={fitAll} title={`${t('a11y.fitAll')} (Z)`} aria-label={t('a11y.fitAll')}><FitIcon size={15} /></button>
      </div>

      {canvasElements.length === 0 && !drawMode && useSettings.getState().settings.preferences.showHints && (
        <div className="hint-pill">{t('canvas.hint')}</div>
      )}
    </div>
  );
}

/**
 * Everyone else's pointers, in their own component.
 *
 * `presence` is replaced wholesale at 20 Hz per peer. Read from the viewport it
 * re-rendered the viewport at that rate, and every card under it went through
 * reconciliation to be told nothing had changed. Isolated here, a cursor moving
 * costs exactly the cursors.
 */
function RemoteCursors() {
  const presence = useBoard((s) => s.presence);
  const cursors = Object.values(presence).filter((p) => p.cursor);
  if (cursors.length === 0) return null;
  return (
    // AX29. Every remote cursor is a positioned <div> CONTAINING A NAME, inside
    // the canvas layer, mutating on every peer pointer move. In browse mode a
    // screen reader walking the board found colleagues' names strewn through
    // the content, moving, with no way to tell them from cards — presence
    // over-present exactly where it means nothing. A cursor is a pointer
    // affordance and has no meaning to someone without a pointer, so the layer
    // is hidden and presence is exposed instead as the topbar summary, where it
    // is a fact rather than an interruption.
    <div aria-hidden="true">
      {cursors.map((p) => (
        <div key={p.clientId} className="remote-cursor" style={{ transform: `translate(${p.cursor!.x}px, ${p.cursor!.y}px)` }}>
          <div className="dot" style={{ background: presenceColor(p.sub || p.clientId) }} />
          <div className="name" style={{ background: presenceColor(p.sub || p.clientId) }}>{p.name || 'Guest'}</div>
        </div>
      ))}
    </div>
  );
}

// LabelFilterBar (§4.18): every label used on the current board renders as a
// toggleable chip — active labels keep their cards lit, everything else dims.
function LabelFilterBar({ elements }: { elements: QElement[] }) {
  const t = useT();
  const labels = useLabels((s) => s.labels);
  const labelFilter = useView((s) => s.labelFilter);
  const { toggleLabelFilter, clearLabelFilter } = useView.getState();

  const used = new Set<string>();
  for (const el of elements) for (const id of el.labelIds ?? []) used.add(id);
  const chips = labels.filter((l) => used.has(l.id));
  if (chips.length === 0) return null;

  return (
    <div className="label-filter-bar" onPointerDown={(e) => e.stopPropagation()}>
      {chips.map((l) => (
        <button
          key={l.id}
          className={`label-chip filter${labelFilter.has(l.id) ? ' on' : ''}`}
          style={{ background: l.color }}
          onClick={() => toggleLabelFilter(l.id)}
        >
          {l.name}
        </button>
      ))}
      {labelFilter.size > 0 && (
        <button className="label-filter-clear" title={t('a11y.clearFilter')} aria-label={t('a11y.clearFilter')}
          onClick={clearLabelFilter}>
          <CloseIcon size={12} />
        </button>
      )}
    </div>
  );
}

export type { QElement };
