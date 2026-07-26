// The canvas half of the preview.
//
// Anything the plan places directly on this board appears where it will
// actually land — the geometry comes from the server, so what a person approves
// is positioned identically to what commits. Elements destined for a column or
// a nested board have no coordinate of their own and are represented by a count
// on their container instead of a phantom card in the wrong place.
//
// Elements the plan will move or delete dim in place rather than jumping, so
// the board still reads as the user's own until they accept.
import { useEffect, useMemo, useRef } from 'react';
import gsap from 'gsap';
import { useBoard } from '../store/boardStore';
import { isDestructive, kindLabel, useAgent, useEffectivePlan } from './agentStore';
import type { AgentAction } from '../api/types';

export function GhostLayer() {
  const run = useAgent((s) => s.run);
  const boardId = useBoard((s) => s.boardId);
  const effective = useEffectivePlan();
  const layerRef = useRef<HTMLDivElement>(null);

  const placed = useMemo(
    () => (effective?.actions ?? []).filter((a) => a.position),
    [effective],
  );

  // How many further changes each placed element will receive, so a new board
  // can honestly say "3 columns inside" instead of silently hiding them.
  const inside = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const a of effective?.actions ?? []) {
      if (!a.parentId || a.parentId === boardId) continue;
      counts[a.parentId] = (counts[a.parentId] ?? 0) + 1;
    }
    return counts;
  }, [effective, boardId]);

  useEffect(() => {
    if (!layerRef.current || placed.length === 0) return;
    gsap.fromTo(
      layerRef.current.querySelectorAll('.ghost-el'),
      { opacity: 0, y: 14, scale: 0.97 },
      { opacity: 1, y: 0, scale: 1, duration: 0.32, ease: 'power3.out', stagger: 0.04 },
    );
  }, [run?.id, placed.length]);

  if (!run || run.state !== 'PROPOSED' || placed.length === 0) return null;

  return (
    <div ref={layerRef} className="ghost-layer">
      {placed.map((a) => (
        <GhostElement key={a.seq} action={a} nested={inside[a.elementId] ?? 0} />
      ))}
    </div>
  );
}

function GhostElement({ action, nested }: { action: AgentAction; nested: number }) {
  const box = action.position!;
  // The link runs both ways: hovering the ghost lights its row in the dock, and
  // hovering the row lights the ghost. A line of text and a rectangle in space
  // should be visibly the same thing.
  const hot = useAgent((s) => s.hoverSeq === action.seq);
  return (
    <div
      className={`ghost-el k-${action.kind}${hot ? ' hot' : ''}`}
      style={{ left: box.x, top: box.y, width: box.width }}
      onPointerDown={(e) => e.stopPropagation()}
      onMouseEnter={() => useAgent.getState().setHover(action.seq)}
      onMouseLeave={() => useAgent.getState().setHover(null)}
    >
      <div className="ghost-el-head">
        <span className="ghost-kind">{kindLabel(action.kind)}</span>
        {nested > 0 && <span className="ghost-count">{nested} inside</span>}
      </div>
      <div className="ghost-el-body" dir="auto">
        {action.title || action.text || action.summary}
      </div>
      {(action.tasks ?? []).slice(0, 4).map((t) => (
        <div key={t} className="ghost-task" dir="auto">☐ {t}</div>
      ))}
    </div>
  );
}

/**
 * Ids a pending plan will move or remove. The canvas dims these so a person can
 * see what is in play without anything actually changing.
 */
export function useProposedIds(): Set<string> {
  const state = useAgent((s) => s.run?.state);
  const effective = useEffectivePlan();
  return useMemo(() => {
    if (state !== 'PROPOSED' || !effective) return EMPTY;
    const ids = new Set<string>();
    for (const a of effective.actions) {
      if (a.kind === 'move_element' || isDestructive(a.kind)) ids.add(a.elementId);
    }
    return ids;
  }, [state, effective]);
}

const EMPTY = new Set<string>();
