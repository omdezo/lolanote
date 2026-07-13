// The SVG connection layer (§4.12). Lines anchor to element edges OR free
// canvas points, follow their cards when moved, curve via a draggable center
// handle, and carry optional labels and arrowheads. Creating one is a drag
// from a card's edge anchor (ghost rendered here); a selected line exposes
// endpoint handles (drag to reconnect or drop free), the curve handle
// (double-click straightens), and a floating toolbar: Color · Start · End ·
// Label · Dashed · Weight — Milanote's exact line controls.
import { useEffect, useMemo, useState } from 'react';
import type { QElement } from '../api/types';
import { deleteOp, updateOp, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { prompt } from '../components/ui/Prompt';
import { ColorIcon, DashIcon, LabelIcon, LineEndIcon, LineStartIcon, WeightIcon } from '../components/Icons';

const EXTENT = 100_000; // virtual canvas half-extent for the SVG surface

const LINE_COLORS = ['#8a86a0', '#1d1d1f', '#f5f5f7', '#5e5ce6', '#1c7ed6', '#0ca678', '#f2a20d', '#e8590c', '#e64980'];
const WEIGHTS = [1.5, 2.5, 4, 6];

interface Pt { x: number; y: number }
interface EndInfo extends Pt { w: number; h: number; free: boolean }

type HandleDrag =
  | { lineId: string; kind: 'from' | 'to'; x: number; y: number }
  | { lineId: string; kind: 'curve'; value: number }
  | { lineId: string; kind: 'body'; dx: number; dy: number };

export function LineLayer() {
  const { boardId, elements, selection, select, commitTransaction } = useBoard();
  const sizes = useView((s) => s.sizes);
  const drag = useView((s) => s.drag);
  const lineDraft = useView((s) => s.lineDraft);
  const [handleDrag, setHandleDrag] = useState<HandleDrag | null>(null);

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
  // commits content.curve. All through updateOp → undoable.
  const startHandleDrag = (e: React.PointerEvent, line: QElement, kind: 'from' | 'to' | 'curve', geo: { p0: Pt; p1: Pt }) => {
    e.stopPropagation();
    e.preventDefault();
    const viewport = document.querySelector('.canvas-viewport') as HTMLElement | null;
    if (!viewport) return;
    const toCanvas = (ev: PointerEvent) => useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);

    const onMove = (ev: PointerEvent) => {
      const pt = toCanvas(ev);
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

  const rendered = lines.map((line) => {
    const from = resolveEnd(line, 'from');
    const to = resolveEnd(line, 'to');
    if (!from || !to) return null;
    const p0 = trim(from, to);
    const p1 = trim(to, from);
    const mx = (p0.x + p1.x) / 2, my = (p0.y + p1.y) / 2;
    const curve = handleDrag?.lineId === line.id && handleDrag.kind === 'curve'
      ? handleDrag.value
      : line.content?.curve ?? 0;
    const nx = -(p1.y - p0.y), ny = p1.x - p0.x;
    const nl = Math.hypot(nx, ny) || 1;
    const cx = mx + (nx / nl) * curve, cy = my + (ny / nl) * curve;
    // The bezier's actual midpoint (t = 0.5) — where the handle sits.
    const hx = 0.25 * p0.x + 0.5 * cx + 0.25 * p1.x;
    const hy = 0.25 * p0.y + 0.5 * cy + 0.25 * p1.y;
    const selectedLine = selection.has(line.id);
    const color = line.content?.color ?? '#8a86a0';
    const weight = line.content?.weight ?? 2;
    const dashed = !!line.content?.dashed;

    if (soloLine?.id === line.id) toolbar = { line, x: hx, y: hy };

    return (
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
            const label = await prompt({ title: 'Line label', defaultValue: line.content?.label ?? '', placeholder: 'Label this connection', confirmLabel: 'Set label' });
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
        {selectedLine && (
          <>
            {/* endpoint handles: drag to reconnect or drop on open canvas */}
            <circle
              className="line-handle"
              cx={p0.x} cy={p0.y} r={7}
              onPointerDown={(e) => startHandleDrag(e, line, 'from', { p0, p1 })}
            />
            <circle
              className="line-handle"
              cx={p1.x} cy={p1.y} r={7}
              onPointerDown={(e) => startHandleDrag(e, line, 'to', { p0, p1 })}
            />
            {/* curve handle: drag to bend, double-click to straighten */}
            <circle
              className="line-handle curve"
              cx={hx} cy={hy} r={6}
              onPointerDown={(e) => startHandleDrag(e, line, 'curve', { p0, p1 })}
              onDoubleClick={(e) => {
                e.stopPropagation();
                void commitTransaction([updateOp(line, { content: { curve: 0 } })]);
              }}
            />
          </>
        )}
      </g>
    );
  });

  return (
    <>
      <svg
        style={{ position: 'absolute', left: -EXTENT, top: -EXTENT, overflow: 'visible', pointerEvents: 'none' }}
        width={EXTENT * 2}
        height={EXTENT * 2}
        viewBox={`${-EXTENT} ${-EXTENT} ${EXTENT * 2} ${EXTENT * 2}`}
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
      {(() => {
        const tb = toolbar as { line: QElement; x: number; y: number } | null;
        return tb ? <LineToolbar line={tb.line} x={tb.x} y={tb.y} /> : null;
      })()}
    </>
  );
}

// ---- floating line toolbar: Color · Start · End · Label · Dashed · Weight ----

function LineToolbar({ line, x, y }: { line: QElement; x: number; y: number }) {
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
      <button title="Line color" onClick={() => setColorsOpen(!colorsOpen)}>
        <span className="lt-ico"><ColorIcon size={15} /></span><span>Color</span>
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
      <button title="Arrow at start" className={c.startArrow ? 'on' : ''} onClick={() => set({ startArrow: !c.startArrow })}>
        <span className="lt-ico"><LineStartIcon size={15} /></span><span>Start</span>
      </button>
      <button title="Arrow at end" className={c.endArrow ? 'on' : ''} onClick={() => set({ endArrow: !c.endArrow })}>
        <span className="lt-ico"><LineEndIcon size={15} /></span><span>End</span>
      </button>
      <button
        title="Label"
        className={c.label ? 'on' : ''}
        onClick={() => {
          void (async () => {
            const label = await prompt({ title: 'Line label', defaultValue: c.label ?? '', placeholder: 'Label this connection', confirmLabel: 'Set label' });
            if (label !== null) set({ label });
          })();
        }}
      >
        <span className="lt-ico"><LabelIcon size={15} /></span><span>Label</span>
      </button>
      <button title="Dashed" className={c.dashed ? 'on' : ''} onClick={() => set({ dashed: !c.dashed })}>
        <span className="lt-ico"><DashIcon size={15} /></span><span>Dashed</span>
      </button>
      <button title="Line weight" onClick={cycleWeight}>
        <span className="lt-ico"><WeightIcon size={15} /></span><span>Weight</span>
      </button>
    </div>
  );
}
