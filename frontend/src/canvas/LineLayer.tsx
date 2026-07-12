// The SVG connection layer (§4.12): lines anchor to element edges, follow
// their cards when moved, curve via a center control point, and carry
// optional labels and arrowheads. Deleting a selected line = Delete key.
import { useEffect, useMemo } from 'react';
import type { QElement } from '../api/types';
import { deleteOp, updateOp, useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { prompt } from '../components/ui/Prompt';

const EXTENT = 100_000; // virtual canvas half-extent for the SVG surface

export function LineLayer() {
  const { boardId, elements, selection, select, commitTransaction } = useBoard();
  const sizes = useView((s) => s.sizes);
  const drag = useView((s) => s.drag);

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

  if (lines.length === 0) return null;

  const center = (el: QElement) => {
    const s = sizes[el.id] ?? { w: el.location.width || 260, h: 120 };
    const dragging = drag && drag.ids.includes(el.id);
    return {
      x: el.location.position.x + s.w / 2 + (dragging ? drag.dx : 0),
      y: el.location.position.y + s.h / 2 + (dragging ? drag.dy : 0),
      w: s.w, h: s.h,
    };
  };

  return (
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
      {lines.map((line) => {
        const from = elements[line.content?.fromId];
        const to = elements[line.content?.toId];
        if (!from || !to || from.deletedAt || to.deletedAt) return null;
        const a = center(from);
        const b = center(to);
        // Trim endpoints to the card edges along the connecting axis.
        const trim = (p: typeof a, q: typeof a) => {
          const dx = q.x - p.x, dy = q.y - p.y;
          const len = Math.hypot(dx, dy) || 1;
          const rx = p.w / 2, ry = p.h / 2;
          const t = Math.min(rx / Math.abs(dx / len) || Infinity, ry / Math.abs(dy / len) || Infinity);
          return { x: p.x + (dx / len) * Math.min(t, len / 2), y: p.y + (dy / len) * Math.min(t, len / 2) };
        };
        const p0 = trim(a, b);
        const p1 = trim(b, a);
        const mx = (p0.x + p1.x) / 2, my = (p0.y + p1.y) / 2;
        const curve = line.content?.curve ?? 0;
        // Curve offsets the control point perpendicular to the line.
        const nx = -(p1.y - p0.y), ny = p1.x - p0.x;
        const nl = Math.hypot(nx, ny) || 1;
        const cx = mx + (nx / nl) * curve, cy = my + (ny / nl) * curve;
        const selectedLine = selection.has(line.id);
        const color = selectedLine ? '#6c5ce7' : (line.content?.color ?? '#8a86a0');
        return (
          <g key={line.id} style={{ pointerEvents: 'auto', cursor: 'pointer' }}>
            <path
              d={`M ${p0.x} ${p0.y} Q ${cx} ${cy} ${p1.x} ${p1.y}`}
              fill="none"
              stroke="transparent"
              strokeWidth={14}
              onPointerDown={(e) => { e.stopPropagation(); select([line.id], e.shiftKey); }}
              onDoubleClick={async (e) => {
                e.stopPropagation();
                const label = await prompt({ title: 'Line label', defaultValue: line.content?.label ?? '', placeholder: 'Label this connection', confirmLabel: 'Set label' });
                if (label !== null) void commitTransaction([updateOp(line, { content: { label } })]);
              }}
            />
            <path
              d={`M ${p0.x} ${p0.y} Q ${cx} ${cy} ${p1.x} ${p1.y}`}
              fill="none"
              stroke={color}
              strokeWidth={line.content?.weight ?? 2}
              markerEnd={line.content?.endArrow ? 'url(#qn-arrow)' : undefined}
              markerStart={line.content?.startArrow ? 'url(#qn-arrow)' : undefined}
              style={{ pointerEvents: 'none' }}
            />
            {line.content?.label && (
              <text className="line-label" x={cx} y={cy - 6}>{line.content.label}</text>
            )}
          </g>
        );
      })}
    </svg>
  );
}
