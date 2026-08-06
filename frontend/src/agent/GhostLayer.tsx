// The canvas half of the preview.
//
// Anything the plan places directly on this board appears where it will
// actually land — the geometry comes from the server, so what a person approves
// is positioned identically to what commits. Elements destined for a column or
// a nested board are represented by a count on their container instead: a
// column orders its children and has no coordinate to show, and a nested
// board's coordinates belong to a canvas that is not this one. Where that
// container ALREADY exists — a real board tile this layer never draws — the
// count is a badge laid over the tile, because the alternative was a review
// that said nothing at all about where most of the changes were going.
//
// Elements the plan will move or delete dim in place rather than jumping, so
// the board still reads as the user's own until they accept.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { tweenFromTo } from '../lib/motion';
import { useBoard } from '../store/boardStore';
import { useView } from '../store/viewStore';
import { useT } from '../i18n';
import { isCreate, isDestructive, kindLabel, useAgent, useEffectivePlan, WORKING } from './agentStore';
import type { AgentAction, QElement } from '../api/types';

export function GhostLayer() {
  const run = useAgent((s) => s.run);
  const events = useAgent((s) => s.events);
  const boardId = useBoard((s) => s.boardId);
  // A duplicate names its contents by the SOURCE elements it copies, so the
  // ghost has to read the live board to show what is in the copy.
  const elements = useBoard((s) => s.elements);
  const effective = useEffectivePlan();
  const layerRef = useRef<HTMLDivElement>(null);

  // A coordinate only means something on the canvas it was computed for.
  //
  // The server now lays out EVERY canvas a plan writes to, not just this one, so
  // a column filed into a nested board arrives carrying a position in that
  // board's coordinate space. Drawn here it would be a phantom card at a
  // meaningless place on the root canvas — a preview of something that will not
  // happen. What those actions do land in is the count on their container's
  // tile, which is what `inside` below is for.
  const placed = useMemo(
    () => (effective?.actions ?? []).filter(
      (a) => a.position && (!a.parentId || a.parentId === boardId),
    ),
    [effective, boardId],
  );

  // What is going INSIDE each placed element — by name, not as a count.
  //
  // A count was the whole review surface for the canonical request: ask to file
  // thirty cards into four columns and you got four empty rectangles labelled
  // "12 inside". Which card went where — the only thing a person is actually
  // deciding — appeared nowhere on the canvas at all.
  const inside = useMemo(() => {
    const byParent: Record<string, { seq: number; label: string }[]> = {};
    for (const a of effective?.actions ?? []) {
      // A duplicate is one action that writes a whole subtree, so nothing is
      // parented to it and the ordinary count comes out empty. Its contents
      // live on the action itself.
      if (a.kind === 'duplicate' && a.copies && a.copies.length > 1) {
        byParent[a.elementId] = a.copies.slice(1).map((c, i) => ({
          seq: a.seq, label: elements[c.sourceId]?.content?.textPreview
            ?? elements[c.sourceId]?.content?.title ?? `item ${i + 1}`,
        }));
        continue;
      }
      if (!a.parentId || a.parentId === boardId) continue;
      (byParent[a.parentId] ??= []).push({
        seq: a.seq,
        label: a.title || a.text || a.summary || '',
      });
    }
    return byParent;
  }, [effective, boardId, elements]);

  // Nested board tiles this plan is filing things INTO.
  //
  // The review was blind in exactly the place the agent now does most of its
  // work. Ghosts are suppressed for anything destined for another canvas (their
  // coordinates mean nothing here), and a nested board's tile is an ordinary
  // board element that this layer never draws — so a plan filing thirty
  // elements into sub-boards previewed as five untouched rectangles and a text
  // list, and the person was approving something they could not see.
  //
  // The count is rolled up through the whole chain, not read off the action's
  // own parent: the realistic plan files cards into a COLUMN inside the nested
  // board, and a badge that only counted direct children would say 0 on the one
  // tile everything was going into.
  const tiles = useMemo<NestedTile[]>(
    () => rollUpDestinations(effective?.actions ?? [], elements, boardId)
      .filter((t) => !!elements[t.id])
      .map((t) => ({
        ...t,
        at: {
          x: elements[t.id].location.position.x,
          y: elements[t.id].location.position.y,
        },
      })),
    [effective, boardId, elements],
  );

  useEffect(() => {
    if (!layerRef.current || placed.length === 0) return;
    tweenFromTo(
      layerRef.current.querySelectorAll('.ghost-el'),
      { opacity: 0, y: 14, scale: 0.97 },
      { opacity: 1, y: 0, scale: 1, duration: 0.32, ease: 'power3.out', stagger: 0.04 },
    );
  }, [run?.id, placed.length]);

  // A plan can also be nothing but arrows — "connect these six cards" writes no
  // boxes and fills no sub-board, so counting only those two would render an
  // empty layer for the request the connector grammar exists to serve.
  const hasConnectors = (effective?.actions ?? []).some((a) => a.kind === 'connect' && a.fromId && a.toId);

  // The same roll-up, fed by the journal instead of by the finished plan. Every
  // field it needs — seq, kind, elementId, parentId — is already on the
  // `action.staged` event the server has emitted since staging shipped; nothing
  // on the canvas ever read one.
  const staged = useMemo<NestedTile[]>(() => {
    if (!run || !WORKING.includes(run.state)) return EMPTY_TILES;
    const asActions: AgentAction[] = [];
    for (const ev of events) {
      if (ev.type !== 'action.staged') continue;
      const d = ev.data ?? {};
      if (typeof d.seq !== 'number' || !d.parentId) continue;
      asActions.push({
        seq: d.seq, kind: String(d.kind ?? '') as AgentAction['kind'],
        elementId: String(d.elementId ?? ''), parentId: String(d.parentId),
        summary: ev.message ?? '',
      });
    }
    return rollUpDestinations(asActions, elements, boardId)
      .filter((t) => !!elements[t.id])
      .map((t) => ({
        ...t,
        at: { x: elements[t.id].location.position.x, y: elements[t.id].location.position.y },
      }));
  }, [run, events, elements, boardId]);

  // While the run is still working the plan is not a plan yet — there are no
  // coordinates, because the layout pass has not run. What there IS, on the
  // wire and from the first staged action, is where each change is destined for.
  // A run was otherwise an opaque wait followed by a wall of ghosts; a person
  // who can see the first three cards heading into Archive cancels at second
  // five instead of reviewing forty rows at minute two.
  const streaming = !!run && WORKING.includes(run.state);
  if (streaming) {
    return staged.length > 0 ? (
      <div className="ghost-layer">
        {staged.map((tile) => <TileBadge key={tile.id} tile={tile} streaming />)}
      </div>
    ) : null;
  }

  // Badges alone are a preview: a plan that touches nothing on this canvas and
  // fills three sub-boards used to render an empty layer, which is the exact
  // case the badge exists for.
  if (!run || run.state !== 'PROPOSED'
    || (placed.length === 0 && tiles.length === 0 && !hasConnectors)) return null;

  return (
    <div ref={layerRef} className="ghost-layer">
      <GhostConnectors actions={effective?.actions ?? []} placed={placed} />
      {placed.map((a) => (
        <GhostElement key={a.seq} action={a} nested={inside[a.elementId] ?? EMPTY_CHILDREN} />
      ))}
      {tiles.map((tile) => (
        <TileBadge key={tile.id} tile={tile} />
      ))}
    </div>
  );
}

/** A board tile on this canvas and the plan rows destined for it, anywhere
 *  inside. */
export interface Destination { id: string; label: string; seqs: number[]; elementIds: string[] }

/**
 * Which nested board on this canvas each change ultimately lands in.
 *
 * Rolled up through the whole chain rather than read off the action's own
 * parent: the realistic plan files cards into a COLUMN inside the nested board,
 * and a count of direct children reads 0 on the one tile everything is going
 * into. Ids the plan is about to create are resolved against the plan itself,
 * which is what lets a card destined for a column that does not exist yet still
 * find the tile that column will sit in.
 *
 * Shared between the review preview and the outcome card because the two have
 * to agree: the badge saying "+7 in Pre-Production" before you press Apply and
 * the row offering to take you there afterwards are the same claim, and a
 * second implementation is a second answer.
 */
export function rollUpDestinations(
  actions: AgentAction[],
  elements: Record<string, QElement>,
  boardId: string,
): Destination[] {
  const staged = new Map<string, AgentAction>();
  for (const a of actions) {
    if (isCreate(a.kind)) staged.set(a.elementId, a);
  }
  // Which tile ON THIS CANVAS a destination ultimately sits in, or null when it
  // sits on this canvas directly, inside a ghost, or nowhere we can draw.
  const tileOf = (from: string): string | null => {
    let cur = from;
    for (let guard = 0; guard < 8 && cur && cur !== boardId; guard++) {
      const here: QElement | undefined = elements[cur];
      if (here) {
        if (here.location.parentId === boardId) return here.type === 'BOARD' ? cur : null;
        cur = here.location.parentId;
        continue;
      }
      cur = staged.get(cur)?.parentId ?? '';
    }
    return null;
  };

  const byTile = new Map<string, Destination>();
  for (const a of actions) {
    if (!a.parentId || a.parentId === boardId) continue;
    const tile = tileOf(a.parentId);
    if (!tile) continue;
    const entry = byTile.get(tile)
      ?? { id: tile, label: (elements[tile]?.content?.title as string) || '', seqs: [], elementIds: [] };
    entry.seqs.push(a.seq);
    if (a.elementId) entry.elementIds.push(a.elementId);
    byTile.set(tile, entry);
  }
  return [...byTile.values()];
}

/** Nominal height for a ghost that has not been measured yet — one frame. */
const GHOST_FALLBACK_H = 86;

/**
 * The arrows.
 *
 * Connect actions were skipped by the server's layout pass (they have no
 * coordinate of their own) and then filtered out here, because this layer draws
 * only actions carrying a position. The consequence was that the whole
 * design_as flow/tree capability — where the tree geometry is computed FROM the
 * edges — previewed as a set of loose boxes, with the one fact explaining why
 * they sit where they do being the one fact the reviewer could not see. The
 * native product draws a ghost line for a single drag; the agent's preview of
 * eight showed none.
 *
 * Endpoints resolve against the same two sources LineLayer uses — a ghost this
 * plan is about to place, or a live element with its measured size — and a
 * connector whose ends do not BOTH resolve on this canvas is skipped, the same
 * honesty rule TileBadge already applies to nested boards.
 */
function GhostConnectors({ actions, placed }: { actions: AgentAction[]; placed: AgentAction[] }) {
  const elements = useBoard((s) => s.elements);
  const sizes = useView((s) => s.sizes);
  const hoverSeq = useAgent((s) => s.hoverSeq);
  // Ghost boxes carry a width from the server and no height — the card decides
  // that when it draws. Measured after paint so an arrow lands on the middle of
  // the rectangle a person is looking at rather than a guess about it.
  const [heights, setHeights] = useState<Record<number, number>>({});

  useLayoutEffect(() => {
    const next: Record<number, number> = {};
    for (const a of placed) {
      const node = document.querySelector<HTMLElement>(`.ghost-el[data-seq="${a.seq}"]`);
      if (node) next[a.seq] = node.offsetHeight;
    }
    setHeights((prev) => {
      const same = Object.keys(next).length === Object.keys(prev).length
        && Object.entries(next).every(([k, v]) => prev[Number(k)] === v);
      return same ? prev : next;
    });
  }, [placed]);

  const connectors = useMemo(() => {
    const ghostOf = new Map<string, AgentAction>();
    for (const a of placed) ghostOf.set(a.elementId, a);

    const anchor = (id: string): { x: number; y: number } | null => {
      const ghost = ghostOf.get(id);
      if (ghost?.position) {
        const h = heights[ghost.seq] ?? GHOST_FALLBACK_H;
        return { x: ghost.position.x + ghost.position.width / 2, y: ghost.position.y + h / 2 };
      }
      const live = elements[id];
      // Only elements on THIS canvas: a coordinate from a nested board's space
      // would draw a line to a meaningless place.
      if (!live || live.deletedAt || live.type === 'LINE') return null;
      const s = sizes[id] ?? { w: live.location.width || 260, h: 120 };
      return { x: live.location.position.x + s.w / 2, y: live.location.position.y + s.h / 2 };
    };

    const out: { seq: number; from: { x: number; y: number }; to: { x: number; y: number } }[] = [];
    for (const a of actions) {
      if (a.kind !== 'connect' || !a.fromId || !a.toId) continue;
      const from = anchor(a.fromId);
      const to = anchor(a.toId);
      if (!from || !to) continue;
      out.push({ seq: a.seq, from, to });
    }
    return out;
  }, [actions, placed, elements, sizes, heights]);

  if (connectors.length === 0) return null;

  return (
    <svg className="ghost-lines" aria-hidden="true">
      <defs>
        <marker id="ghost-arrow" viewBox="0 0 10 10" refX="9" refY="5"
          markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M0 0 L10 5 L0 10 z" fill="currentColor" />
        </marker>
      </defs>
      {connectors.map((c) => (
        <line
          key={c.seq}
          className={`ghost-line${hoverSeq === c.seq ? ' hot' : ''}`}
          x1={c.from.x} y1={c.from.y} x2={c.to.x} y2={c.to.y}
          markerEnd="url(#ghost-arrow)"
        />
      ))}
    </svg>
  );
}

const EMPTY_CHILDREN: { seq: number; label: string }[] = [];
const EMPTY_TILES: NestedTile[] = [];

/** A destination, plus where its tile sits on this canvas. */
interface NestedTile extends Destination {
  at: { x: number; y: number };
}

/**
 * A board renders as a fixed tile whatever width its record carries — the same
 * fact ElementShell encodes by ignoring location.width for boards. Framing the
 * badge with the stored width would draw a 260px dashed box around a 148px
 * picture, floating clear of the thing it belongs to.
 */
const TILE = { width: 148, height: 118 };

/**
 * The count, drawn on the real tile.
 *
 * Honest depth-preview: it says how much is going in there and does not pretend
 * to render a canvas that is not this one. The wrapper is inert so the tile
 * underneath can still be opened mid-review; only the pill takes the pointer.
 */
function TileBadge({ tile, streaming }: { tile: NestedTile; streaming?: boolean }) {
  const focus = useAgent((s) => s.tileFocus);
  const t = useT();
  const lit = focus?.id === tile.id;
  const pin = () => {
    const current = useAgent.getState().tileFocus;
    const same = current?.pinned && current.id === tile.id;
    useAgent.getState().setTileFocus(
      same ? null : { id: tile.id, label: tile.label, seqs: tile.seqs, pinned: true },
    );
  };
  return (
    <div
      className={`ghost-badge${lit ? ' hot' : ''}${streaming ? ' streaming' : ''}`}
      style={{ left: tile.at.x, top: tile.at.y, width: TILE.width, height: TILE.height }}
    >
      {/* A <span onClick> for the single control that makes a thirty-card,
          three-sub-board plan reviewable: not focusable, no role, no key
          handler, so it excluded exactly the people who most need a linear
          review surface. It has always had a click handler and a title; being
          a button is the honest spelling of that. */}
      <button
        type="button"
        className="ghost-count"
        title={t('agent.insideHint')}
        aria-pressed={!!focus?.pinned && focus.id === tile.id}
        aria-label={`${tile.label || t('app.untitled')} — +${tile.seqs.length} ${t('agent.inside')}`}
        onPointerDown={(e) => e.stopPropagation()}
        onClick={pin}
        onMouseEnter={() => {
          if (useAgent.getState().tileFocus?.pinned) return;
          useAgent.getState().setTileFocus({
            id: tile.id, label: tile.label, seqs: tile.seqs, pinned: false,
          });
        }}
        onMouseLeave={() => {
          if (!useAgent.getState().tileFocus?.pinned) useAgent.getState().setTileFocus(null);
        }}
        onFocus={() => {
          if (useAgent.getState().tileFocus?.pinned) return;
          useAgent.getState().setTileFocus({
            id: tile.id, label: tile.label, seqs: tile.seqs, pinned: false,
          });
        }}
        onBlur={() => {
          if (!useAgent.getState().tileFocus?.pinned) useAgent.getState().setTileFocus(null);
        }}
      >
        +{tile.seqs.length} {t('agent.inside')}
      </button>
    </div>
  );
}

function GhostElement({ action, nested }: {
  action: AgentAction;
  nested: { seq: number; label: string }[];
}) {
  const box = action.position!;
  // The link runs both ways: hovering the ghost lights its row in the dock, and
  // hovering the row lights the ghost. A line of text and a rectangle in space
  // should be visibly the same thing.
  const hoverSeq = useAgent((s) => s.hoverSeq);
  const hot = hoverSeq === action.seq;
  const t = useT();
  return (
    <div
      className={`ghost-el k-${action.kind}${hot ? ' hot' : ''}`}
      data-seq={action.seq}
      style={{ left: box.x, top: box.y, width: box.width }}
      // The binding was mouse-only in both directions, which meant that on
      // touch hoverSeq had no producer at all and a reviewer facing thirty rows
      // and twelve dashed rectangles could not ask "which rectangle is this
      // row?" — the question review exists to answer. Focus is the third
      // producer, and it is what makes the ghost reachable at 400% zoom, where
      // the row and its rectangle are never on screen together.
      tabIndex={0}
      role="group"
      aria-label={`${kindLabel(action.kind, t)} — ${action.title || action.text || action.summary}`}
      onPointerDown={(e) => e.stopPropagation()}
      onMouseEnter={() => useAgent.getState().setHover(action.seq)}
      onMouseLeave={() => useAgent.getState().setHover(null)}
      onFocus={() => useAgent.getState().setHover(action.seq)}
      onBlur={() => useAgent.getState().setHover(null)}
    >
      <div className="ghost-el-head">
        <span className="ghost-kind">{kindLabel(action.kind, t)}</span>
        {nested.length > 0 && (
          <span className="ghost-count">{nested.length} {t('agent.inside')}</span>
        )}
      </div>
      <div className="ghost-el-body" dir="auto">
        {action.title || action.text || action.summary}
      </div>
      {/* The contents, named. Hovering one lights its row in the review list,
          the same way hovering a row lights this ghost — a line of text and a
          rectangle in space should read as the same object. */}
      {nested.length > 0 && (
        <div className="ghost-contents">
          {nested.slice(0, 6).map((child) => (
            <div
              key={child.seq}
              className={`ghost-child${hoverSeq === child.seq ? ' hot' : ''}`}
              dir="auto"
              tabIndex={0}
              onMouseEnter={() => useAgent.getState().setHover(child.seq)}
              onMouseLeave={() => useAgent.getState().setHover(null)}
              onFocus={() => useAgent.getState().setHover(child.seq)}
              onBlur={() => useAgent.getState().setHover(null)}
            >
              {child.label}
            </div>
          ))}
          {nested.length > 6 && (
            <div className="ghost-child more">+{nested.length - 6}</div>
          )}
        </div>
      )}
      {/* The box glyph is decoration: it announces as an unpronounceable
          symbol, so the word it stands for is written out for AT instead. */}
      {(action.tasks ?? []).slice(0, 4).map((task) => (
        <div key={task} className="ghost-task" dir="auto">
          <span aria-hidden="true">☐</span>
          <span className="sr-only">{t('agent.taskItem')}: </span>
          {task}
        </div>
      ))}
    </div>
  );
}

/**
 * What a pending plan will do to each element it touches, keyed by id.
 *
 * A Set of ids until AX17: the canvas dimmed these to 38% opacity and that was
 * the ENTIRE signal — no aria, no icon, no border, no text. WCAG 1.4.1 on the
 * highest-stakes review in the product, and the second-order effect is worse
 * than the first: 38% multiplies through to the card's own text, so `--ink` at
 * 16.8:1 becomes about 3.3:1. The person is asked to read and approve content
 * that the preview has made hard to read, and for a low-vision user the cards
 * about to go to the trash are simply gone.
 *
 * Carrying the KIND rather than mere membership is what lets the shell say
 * which thing is about to happen — and it comes from `kindLabel`, the same map
 * the ghost head uses, so the badge on a live card and the badge on its ghost
 * cannot drift apart.
 */
export function useProposedIds(): Map<string, string> {
  const state = useAgent((s) => s.run?.state);
  const effective = useEffectivePlan();
  return useMemo(() => {
    if (state !== 'PROPOSED' || !effective) return EMPTY;
    const ids = new Map<string, string>();
    for (const a of effective.actions) {
      // 'place' moves an element to a new coordinate, so the original must dim
      // too — otherwise the card appears twice, once where it is and once as a
      // ghost where it is going.
      if (a.kind === 'move_element' || a.kind === 'place' || isDestructive(a.kind)) ids.set(a.elementId, a.kind);
    }
    return ids;
  }, [state, effective]);
}

const EMPTY = new Map<string, string>();
