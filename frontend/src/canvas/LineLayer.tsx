// The SVG connection layer (§4.12). Lines anchor to element edges OR free
// canvas points, follow their cards when moved, curve via a draggable center
// handle, and carry optional labels and arrowheads. Creating one is a drag
// from a card's edge anchor (ghost rendered here); a selected line exposes
// endpoint handles (drag to reconnect or drop free), the curve handle
// (double-click straightens), and a floating toolbar: Color · Start · End ·
// Label · Dashed · Weight — Milanote's exact line controls.
import { useEffect, useMemo, useRef, useState } from 'react';
import type { QElement } from '../api/types';
import { deleteOp, updateOp, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { t as translate, useT } from '../i18n';
import { prompt } from '../components/ui/Prompt';
import { ColorIcon, DashIcon, LabelIcon, LineEndIcon, LineStartIcon, WeightIcon } from '../components/Icons';

/**
 * How far past the outermost connector the SVG surface reaches.
 *
 * This used to be a 100,000px half-extent, so the layer declared a 200,000 ×
 * 200,000 pixel box — four orders of magnitude past any viewport — and mounted
 * a SECOND identical one the moment a line was selected. The compositor was
 * being handed a layer whose declared bounds bore no relation to what was
 * drawn. The surface is now the union of the lines' own boxes plus this margin,
 * which is enough for arrowheads, labels, the curve handle and its halo;
 * `overflow: visible` still covers anything mid-drag that runs past it.
 */
const LINE_MARGIN = 64;

const LINE_COLORS = ['#8a86a0', '#1d1d1f', '#f5f5f7', '#5e5ce6', '#1c7ed6', '#0ca678', '#f2a20d', '#e8590c', '#e64980'];
const WEIGHTS = [1.5, 2.5, 4, 6];

// highlightConnectTarget lights up the card under the pointer while a line
// endpoint (or a new connection from a card anchor) is being dragged.
// Pass null (or an event over open canvas) to clear.
let lastConnectTarget: Element | null = null;
export function highlightConnectTarget(ev: PointerEvent | null, excludeId?: string) {
  const shell = ev
    ? document.elementFromPoint(ev.clientX, ev.clientY)?.closest('[data-element-id]')
    : null;
  const valid = shell && shell.getAttribute('data-element-id') !== excludeId ? shell : null;
  if (lastConnectTarget && lastConnectTarget !== valid) lastConnectTarget.classList.remove('connect-target');
  if (valid) valid.classList.add('connect-target');
  lastConnectTarget = valid;
}

interface Pt { x: number; y: number }
interface EndInfo extends Pt { w: number; h: number; free: boolean }

/** Everything a line needs drawn, once the trims and the bezier are solved. */
interface LineGeo { p0: Pt; p1: Pt; cx: number; cy: number; hx: number; hy: number }

/**
 * An accumulated bounding box turned into an SVG surface, with the margin.
 *
 * A box that never grew (no line resolved) degenerates to a 1×1 at the origin
 * rather than to Infinity — an `<svg width={Infinity}>` is a rendering fault,
 * not an empty layer, and this path is reached whenever every connector points
 * at something that has been deleted.
 */
function surfaceBox(minX: number, minY: number, maxX: number, maxY: number) {
  if (!Number.isFinite(minX) || !Number.isFinite(minY)) return { x: 0, y: 0, w: 1, h: 1 };
  return {
    x: minX - LINE_MARGIN,
    y: minY - LINE_MARGIN,
    w: Math.max(1, maxX - minX + LINE_MARGIN * 2),
    h: Math.max(1, maxY - minY + LINE_MARGIN * 2),
  };
}

type HandleDrag =
  | { lineId: string; kind: 'from' | 'to'; x: number; y: number }
  | { lineId: string; kind: 'curve'; value: number }
  | { lineId: string; kind: 'body'; dx: number; dy: number };

export function LineLayer() {
  // `tr` rather than `t`: this file already uses `t` as a bezier parameter.
  const tr = useT();
  // Field selectors, not the whole store: this layer subscribed wholesale, so a
  // collaborator's cursor at 20 Hz recomputed every bezier on the board.
  const boardId = useBoard((s) => s.boardId);
  const elements = useBoard((s) => s.elements);
  const selection = useBoard((s) => s.selection);
  const select = useBoard((s) => s.select);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const sizes = useView((s) => s.sizes);
  const drag = useView((s) => s.drag);
  const lineDraft = useView((s) => s.lineDraft);
  const [handleDrag, setHandleDrag] = useState<HandleDrag | null>(null);
  /**
   * Solved geometry from the previous pass, keyed by line id.
   *
   * `drag` gets a fresh identity on every pointermove, so this component
   * re-rendered at pointer rate and recomputed every line — two trims (each a
   * `Math.hypot` and two divisions) plus a bezier — for 200 lines while the
   * card being dragged touched none of them. The signature below is exactly the
   * inputs the solve reads, so a line whose endpoints did not move reuses its
   * answer and the drag costs only the lines actually attached to what moved.
   */
  const geoCache = useRef(new Map<string, { sig: string; geo: LineGeo }>());

  const lines = useMemo(
    () =>
      Object.values(elements).filter(
        (el) => el.type === 'LINE' && el.location.parentId === boardId && !el.deletedAt,
      ),
    [elements, boardId],
  );

  // Delete key removes selected lines (cards handle their own delete via the
  // action bar; lines have no shell).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Delete' && e.key !== 'Backspace') return;
      if ((e.target as HTMLElement)?.closest('input, textarea, [contenteditable="true"]')) return;
      const state = useBoard.getState();
      const ops = Array.from(state.selection)
        .map((id) => state.elements[id])
        .filter((el): el is QElement => !!el)
        .map((el) => deleteOp(el));
      if (ops.length) {
        void state.commitTransaction(ops);
        state.clearSelection();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const center = (el: QElement): EndInfo => {
    const s = sizes[el.id] ?? { w: el.location.width || 260, h: 120 };
    const dragging = drag && drag.ids.includes(el.id);
    return {
      x: el.location.position.x + s.w / 2 + (dragging ? drag.dx : 0),
      y: el.location.position.y + s.h / 2 + (dragging ? drag.dy : 0),
      w: s.w, h: s.h, free: false,
    };
  };

  // resolveEnd: an endpoint is either a live element or a free canvas point.
  const resolveEnd = (line: QElement, side: 'from' | 'to'): EndInfo | null => {
    if (handleDrag && handleDrag.lineId === line.id && handleDrag.kind === side) {
      return { x: handleDrag.x, y: handleDrag.y, w: 0, h: 0, free: true };
    }
    const id = line.content?.[side === 'from' ? 'fromId' : 'toId'];
    if (id) {
      const el = elements[id];
      if (el && !el.deletedAt) return center(el);
      return null; // connected element gone
    }
    const pt = line.content?.[side === 'from' ? 'fromPoint' : 'toPoint'];
    if (pt && typeof pt.x === 'number') {
      // A body drag shifts free endpoints live (connected ends stay anchored).
      const body = handleDrag?.lineId === line.id && handleDrag.kind === 'body' ? handleDrag : null;
      return { x: pt.x + (body?.dx ?? 0), y: pt.y + (body?.dy ?? 0), w: 0, h: 0, free: true };
    }
    return null;
  };

  // startBodyDrag moves a line by its body — free endpoints translate with
  // the pointer (a fully free line moves whole, like any card); connected
  // endpoints stay anchored to their cards.
  const startBodyDrag = (e: React.PointerEvent, line: QElement) => {
    const hasFree = !line.content?.fromId || !line.content?.toId;
    if (!hasFree) return;
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    if (!viewport) return;
    const start = useView.getState().toCanvas(e.clientX, e.clientY, viewport);
    let moved = false;

    const onMove = (ev: PointerEvent) => {
      const pt = useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);
      const dx = pt.x - start.x, dy = pt.y - start.y;
      if (!moved && Math.hypot(dx, dy) > 4) moved = true;
      if (moved) setHandleDrag({ lineId: line.id, kind: 'body', dx, dy });
    };
    const onUp = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      setHandleDrag(null);
      if (!moved) return;
      const pt = useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);
      const dx = pt.x - start.x, dy = pt.y - start.y;
      const patch: Record<string, any> = {};
      const fp = line.content?.fromPoint, tp = line.content?.toPoint;
      if (!line.content?.fromId && fp) patch.fromPoint = { x: fp.x + dx, y: fp.y + dy };
      if (!line.content?.toId && tp) patch.toPoint = { x: tp.x + dx, y: tp.y + dy };
      if (Object.keys(patch).length) {
        void useBoard.getState().commitTransaction([updateOp(line, { content: patch })]);
      }
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  // trim pulls an element endpoint back to the card edge along the axis.
  const trim = (p: EndInfo, q: Pt): Pt => {
    if (p.free) return p;
    const dx = q.x - p.x, dy = q.y - p.y;
    const len = Math.hypot(dx, dy) || 1;
    const rx = p.w / 2, ry = p.h / 2;
    const t = Math.min(rx / Math.abs(dx / len) || Infinity, ry / Math.abs(dy / len) || Infinity);
    return { x: p.x + (dx / len) * Math.min(t, len / 2), y: p.y + (dy / len) * Math.min(t, len / 2) };
  };

  // startHandleDrag wires an endpoint/curve handle to the pointer. Endpoints
  // commit a reconnect (drop on a card) or a free point; the curve handle
  // commits content.curve. All through updateOp → undoable. While an
  // endpoint travels, the card under the pointer lights up as a drop target.
  const startHandleDrag = (e: React.PointerEvent, line: QElement, kind: 'from' | 'to' | 'curve', geo: { p0: Pt; p1: Pt }) => {
    e.stopPropagation();
    e.preventDefault();
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    if (!viewport) return;
    const toCanvas = (ev: PointerEvent) => useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);

    const onMove = (ev: PointerEvent) => {
      const pt = toCanvas(ev);
      if (kind !== 'curve') highlightConnectTarget(ev, line.id);
      if (kind === 'curve') {
        // Signed distance from the chord, doubled so the curve passes
        // through the pointer (quadratic bezier midpoint = ½ control offset).
        const mx = (geo.p0.x + geo.p1.x) / 2, my = (geo.p0.y + geo.p1.y) / 2;
        const nx = -(geo.p1.y - geo.p0.y), ny = geo.p1.x - geo.p0.x;
        const nl = Math.hypot(nx, ny) || 1;
        const dist = ((pt.x - mx) * nx + (pt.y - my) * ny) / nl;
        setHandleDrag({ lineId: line.id, kind: 'curve', value: dist * 2 });
      } else {
        setHandleDrag({ lineId: line.id, kind, x: pt.x, y: pt.y });
      }
    };
    const onUp = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      highlightConnectTarget(null);
      const state = useBoard.getState();
      const pt = toCanvas(ev);
      setHandleDrag(null);
      if (kind === 'curve') {
        const mx = (geo.p0.x + geo.p1.x) / 2, my = (geo.p0.y + geo.p1.y) / 2;
        const nx = -(geo.p1.y - geo.p0.y), ny = geo.p1.x - geo.p0.x;
        const nl = Math.hypot(nx, ny) || 1;
        const dist = ((pt.x - mx) * nx + (pt.y - my) * ny) / nl;
        void state.commitTransaction([updateOp(line, { content: { curve: Math.round(dist * 2) } })]);
        return;
      }
      // Endpoint drop: a card under the pointer reconnects; open canvas
      // leaves a free point.
      const shell = document.elementFromPoint(ev.clientX, ev.clientY)?.closest('[data-element-id]');
      const targetId = shell?.getAttribute('data-element-id');
      const otherId = line.content?.[kind === 'from' ? 'toId' : 'fromId'];
      const patch: Record<string, any> = {};
      if (targetId && targetId !== otherId && state.elements[targetId]?.type !== 'LINE') {
        patch[kind === 'from' ? 'fromId' : 'toId'] = targetId;
        patch[kind === 'from' ? 'fromPoint' : 'toPoint'] = null;
      } else {
        patch[kind === 'from' ? 'fromId' : 'toId'] = null;
        patch[kind === 'from' ? 'fromPoint' : 'toPoint'] = { x: pt.x, y: pt.y };
      }
      void state.commitTransaction([updateOp(line, { content: patch })]);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  // Ghost line while dragging a new connection off a card's anchor.
  const draftSource = lineDraft ? elements[lineDraft.sourceId] : null;

  const selectedLines = lines.filter((l) => selection.has(l.id));
  const soloLine = selectedLines.length === 1 ? selectedLines[0] : null;

  if (lines.length === 0 && !draftSource) return null;

  // Assigned inside the render map (TS's flow analysis can't see it, hence
  // the cast at the usage site below).
  let toolbar: { line: QElement; x: number; y: number } | null = null;
  // Handles render in a separate overlay svg ABOVE the element shells —
  // otherwise a handle sitting at a card's edge is unclickable (the card,
  // painted later, would swallow the pointer).
  const handleNodes: JSX.Element[] = [];
  const handleBox = { minX: Infinity, minY: Infinity, maxX: -Infinity, maxY: -Infinity };

  // Solve one line, or hand back the answer from last frame if none of its
  // inputs moved. Everything the solve reads goes in the signature.
  const solve = (line: QElement, from: EndInfo, to: EndInfo, curve: number): LineGeo => {
    const sig = `${from.x},${from.y},${from.w},${from.h},${from.free ? 1 : 0}`
      + `|${to.x},${to.y},${to.w},${to.h},${to.free ? 1 : 0}|${curve}`;
    const hit = geoCache.current.get(line.id);
    if (hit && hit.sig === sig) return hit.geo;
    const p0 = trim(from, to);
    const p1 = trim(to, from);
    const mx = (p0.x + p1.x) / 2, my = (p0.y + p1.y) / 2;
    const nx = -(p1.y - p0.y), ny = p1.x - p0.x;
    const nl = Math.hypot(nx, ny) || 1;
    const cx = mx + (nx / nl) * curve, cy = my + (ny / nl) * curve;
    // The bezier's actual midpoint (t = 0.5) — where the handle sits.
    const geo: LineGeo = {
      p0, p1, cx, cy,
      hx: 0.25 * p0.x + 0.5 * cx + 0.25 * p1.x,
      hy: 0.25 * p0.y + 0.5 * cy + 0.25 * p1.y,
    };
    geoCache.current.set(line.id, { sig, geo });
    return geo;
  };

  // The union of what is actually drawn, so the surface below is sized to the
  // diagram rather than to a made-up 200,000px square.
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  const grow = (x: number, y: number) => {
    if (x < minX) minX = x;
    if (y < minY) minY = y;
    if (x > maxX) maxX = x;
    if (y > maxY) maxY = y;
  };

  const rendered = lines.map((line) => {
    const from = resolveEnd(line, 'from');
    const to = resolveEnd(line, 'to');
    if (!from || !to) return null;
    const curve = handleDrag?.lineId === line.id && handleDrag.kind === 'curve'
      ? handleDrag.value
      : line.content?.curve ?? 0;
    const { p0, p1, cx, cy, hx, hy } = solve(line, from, to, curve);
    grow(p0.x, p0.y);
    grow(p1.x, p1.y);
    grow(cx, cy);
    const selectedLine = selection.has(line.id);
    const color = line.content?.color ?? '#8a86a0';
    const weight = line.content?.weight ?? 2;
    const dashed = !!line.content?.dashed;

    if (soloLine?.id === line.id) toolbar = { line, x: hx, y: hy };

    const node = (
      <g key={line.id} style={{ pointerEvents: 'auto', cursor: 'pointer' }}>
        <path
          d={`M ${p0.x} ${p0.y} Q ${cx} ${cy} ${p1.x} ${p1.y}`}
          fill="none"
          stroke="transparent"
          strokeWidth={16}
          onPointerDown={(e) => {
            e.stopPropagation();
            select([line.id], e.shiftKey);
            startBodyDrag(e, line);
          }}
          onDoubleClick={async (e) => {
            e.stopPropagation();
            const label = await prompt({ title: translate('dlg.lineLabel'), defaultValue: line.content?.label ?? '', placeholder: translate('dlg.lineLabelHint'), confirmLabel: translate('dlg.setLabel') });
            if (label !== null) void commitTransaction([updateOp(line, { content: { label } })]);
          }}
        />
        <path
          d={`M ${p0.x} ${p0.y} Q ${cx} ${cy} ${p1.x} ${p1.y}`}
          fill="none"
          stroke={selectedLine ? 'var(--accent)' : color}
          strokeWidth={weight}
          strokeDasharray={dashed ? `${weight * 3.5} ${weight * 2.8}` : undefined}
          markerEnd={line.content?.endArrow ? 'url(#qn-arrow)' : undefined}
          markerStart={line.content?.startArrow ? 'url(#qn-arrow)' : undefined}
          style={{ pointerEvents: 'none' }}
        />
        {line.content?.label && (
          <text className="line-label" x={hx} y={hy - 10}>{line.content.label}</text>
        )}
      </g>
    );

    if (selectedLine) {
      // Each handle carries an invisible halo so it's easy to grab.
      const handle = (kind: 'from' | 'to' | 'curve', x: number, y: number) => (
        <g key={`${line.id}-${kind}`}>
          <circle
            className="line-handle-halo"
            cx={x} cy={y} r={14}
            onPointerDown={(e) => startHandleDrag(e, line, kind, { p0, p1 })}
            onDoubleClick={kind === 'curve' ? (e) => {
              e.stopPropagation();
              void commitTransaction([updateOp(line, { content: { curve: 0 } })]);
            } : undefined}
          />
          <circle
            className={`line-handle${kind === 'curve' ? ' curve' : ''}`}
            cx={x} cy={y} r={kind === 'curve' ? 6 : 7}
            style={{ pointerEvents: 'none' }}
          />
        </g>
      );
      handleNodes.push(handle('from', p0.x, p0.y), handle('to', p1.x, p1.y), handle('curve', hx, hy));
      for (const [hxp, hyp] of [[p0.x, p0.y], [p1.x, p1.y], [hx, hy]]) {
        if (hxp < handleBox.minX) handleBox.minX = hxp;
        if (hyp < handleBox.minY) handleBox.minY = hyp;
        if (hxp > handleBox.maxX) handleBox.maxX = hxp;
        if (hyp > handleBox.maxY) handleBox.maxY = hyp;
      }
    }

    return node;
  });

  // The draft connector is drawn on the same surface, so its far end has to be
  // inside the box or the ghost line vanishes the moment it leaves the diagram.
  if (draftSource && lineDraft) {
    const from = center(draftSource);
    grow(from.x, from.y);
    grow(lineDraft.x, lineDraft.y);
  }

  const box = surfaceBox(minX, minY, maxX, maxY);
  const handles = surfaceBox(handleBox.minX, handleBox.minY, handleBox.maxX, handleBox.maxY);

  return (
    <>
      {/* AX27. The connector graph rendered as bare `<path>` elements with no
          accessible representation at all — so for a non-sighted reader a
          workflow diagram was a set of unrelated cards, and the relationships
          the diagram exists to state were simply absent. Lines carry a `label`
          and endpoints; the linear form of that is one sentence per edge, which
          is also exactly the shape `edgesAmong` was written to produce on the
          agent side and which nothing has ever read. One structure, two
          readers.

          Outside the <svg>s on purpose: SVG child roles are inconsistently
          exposed, and a plain list is a thing every screen reader can walk. */}
      {lines.length > 0 && (
        <ul className="sr-only" aria-label={tr('a11y.connections')}>
          {lines.map((line) => {
            const a = elements[line.content?.fromId]?.content;
            const b = elements[line.content?.toId]?.content;
            const nameOf = (c: any) => c?.title || c?.textPreview || c?.filename || tr('search.untitled');
            if (!a && !b) return null;
            return (
              <li key={`edge-${line.id}`}>
                {`${nameOf(a)} ${tr('a11y.connectsTo')} ${nameOf(b)}${line.content?.label ? `, ${line.content.label}` : ''}`}
              </li>
            );
          })}
        </ul>
      )}
      <svg
        style={{ position: 'absolute', left: box.x, top: box.y, overflow: 'visible', pointerEvents: 'none' }}
        width={box.w}
        height={box.h}
        viewBox={`${box.x} ${box.y} ${box.w} ${box.h}`}
      >
        <defs>
          <marker id="qn-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="context-stroke" />
          </marker>
        </defs>
        {rendered}
        {draftSource && lineDraft && (() => {
          const from = center(draftSource);
          const p0 = trim(from, lineDraft);
          return (
            <path
              d={`M ${p0.x} ${p0.y} L ${lineDraft.x} ${lineDraft.y}`}
              fill="none" stroke="var(--accent)" strokeWidth={2}
              strokeDasharray="6 5" markerEnd="url(#qn-arrow)"
            />
          );
        })()}
      </svg>
      {/* A second surface, not a `<g>` in the first: handles sit at a card's
          edge and have to paint ABOVE the shells, while the line body paints
          below them. It is now sized to the handles themselves rather than
          being a second 200,000px square laid over the whole canvas. */}
      {handleNodes.length > 0 && (
        <svg
          style={{ position: 'absolute', left: handles.x, top: handles.y, overflow: 'visible', pointerEvents: 'none', zIndex: 25 }}
          width={handles.w}
          height={handles.h}
          viewBox={`${handles.x} ${handles.y} ${handles.w} ${handles.h}`}
        >
          {/* While a handle drag is in flight the whole overlay goes
              pointer-transparent — otherwise the traveling handle sits on
              top of the drop card and steals elementFromPoint. */}
          <g style={{ pointerEvents: handleDrag ? 'none' : 'auto' }}>{handleNodes}</g>
        </svg>
      )}
      {(() => {
        const tb = toolbar as { line: QElement; x: number; y: number } | null;
        return tb ? <LineToolbar line={tb.line} x={tb.x} y={tb.y} /> : null;
      })()}
    </>
  );
}

// ---- floating line toolbar: Color · Start · End · Label · Dashed · Weight ----

function LineToolbar({ line, x, y }: { line: QElement; x: number; y: number }) {
  const t = useT();
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [colorsOpen, setColorsOpen] = useState(false);
  const c = line.content ?? {};
  const set = (patch: Record<string, unknown>) =>
    void commitTransaction([updateOp(line, { content: patch })]);

  const cycleWeight = () => {
    const cur = (c.weight as number) ?? 2;
    const idx = WEIGHTS.findIndex((w) => w >= cur - 0.01);
    set({ weight: WEIGHTS[(idx + 1) % WEIGHTS.length] });
  };

  return (
    <div className="line-toolbar" style={{ left: x + 18, top: y - 20 }} onPointerDown={(e) => e.stopPropagation()}>
      <button title={t('line.color')} onClick={() => setColorsOpen(!colorsOpen)}>
        <span className="lt-ico"><ColorIcon size={15} /></span><span>{t('line.color')}</span>
      </button>
      {colorsOpen && (
        <div className="lt-colors">
          {LINE_COLORS.map((hex) => (
            <button
              key={hex}
              className={`lt-swatch${(c.color ?? '#8a86a0') === hex ? ' on' : ''}`}
              style={{ background: hex }}
              onClick={() => { set({ color: hex }); setColorsOpen(false); }}
            />
          ))}
        </div>
      )}
      <button title={t('line.startHint')} className={c.startArrow ? 'on' : ''} onClick={() => set({ startArrow: !c.startArrow })}>
        <span className="lt-ico"><LineStartIcon size={15} /></span><span>{t('line.start')}</span>
      </button>
      <button title={t('line.endHint')} className={c.endArrow ? 'on' : ''} onClick={() => set({ endArrow: !c.endArrow })}>
        <span className="lt-ico"><LineEndIcon size={15} /></span><span>{t('line.end')}</span>
      </button>
      <button
        title={t('line.label')}
        className={c.label ? 'on' : ''}
        onClick={() => {
          void (async () => {
            const label = await prompt({ title: t('dlg.lineLabel'), defaultValue: c.label ?? '', placeholder: t('dlg.lineLabelHint'), confirmLabel: t('dlg.setLabel') });
            if (label !== null) set({ label });
          })();
        }}
      >
        <span className="lt-ico"><LabelIcon size={15} /></span><span>{t('line.label')}</span>
      </button>
      <button title={t('line.dashed')} className={c.dashed ? 'on' : ''} onClick={() => set({ dashed: !c.dashed })}>
        <span className="lt-ico"><DashIcon size={15} /></span><span>{t('line.dashed')}</span>
      </button>
      <button title={t('line.weightHint')} onClick={cycleWeight}>
        <span className="lt-ico"><WeightIcon size={15} /></span><span>{t('line.weight')}</span>
      </button>
    </div>
  );
}
