// ElementShell wraps every card with the shared canvas behaviors: selection,
// dragging (multi-select drags commit ONE transaction, §9.5), resizing,
// size measurement for the line layer, the floating action bar, the line
// anchor, and column drop targets.
import { memo, useCallback, useEffect, useRef } from 'react';
import gsap from 'gsap';
import type { QElement } from '../api/types';
import { api } from '../api/client';
import { t } from '../i18n';
import { elementDir, hasTextDirection, type TextDirection } from '../lib/direction';
import { createOp, deleteOp, moveOp, updateOp, useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { useView } from '../store/viewStore';
import { highlightConnectTarget } from './LineLayer';
import { ElementView } from '../components/elements/ElementView';
import {
  AliasArrow, BoardIcon, ColumnIcon, DirAutoIcon, DirLtrIcon, DirRtlIcon, DuplicateIcon,
  LabelIcon, LockIcon, PaletteIcon, RenameIcon, SyncIcon, TemplateIcon, TrashIcon,
} from '../components/Icons';
import { useBoardStyle } from '../components/ui/BoardStylePopover';
import { useContextMenu } from '../components/ui/ContextMenu';
import { LabelChips, useLabelPopover } from '../components/ui/LabelPopover';
import { prompt } from '../components/ui/Prompt';
import { newObjectId } from '../lib/objectId';

interface Props {
  element: QElement;
  navigate: (boardId: string) => Promise<void>;
  viewportRef: React.RefObject<HTMLDivElement | null>;
  inColumn?: boolean;
}

export const ElementShell = memo(function ElementShell({ element, navigate, viewportRef, inColumn }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const { selection, select, commitTransaction, remoteEditing } = useBoard();
  const drag = useView((s) => s.drag);
  const selected = selection.has(element.id);
  const isDragging = !!drag && drag.ids.includes(element.id);
  const remoteEditor = remoteEditing[element.id];

  // Measure rendered size — the line layer and marquee hit-tests need it.
  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    const report = () => useView.getState().reportSize(element.id, node.offsetWidth, node.offsetHeight);
    report();
    const obs = new ResizeObserver(report);
    obs.observe(node);
    return () => obs.disconnect();
  }, [element.id]);

  // Drop-in animation on mount.
  useEffect(() => {
    if (ref.current) {
      gsap.fromTo(ref.current, { scale: 0.92, opacity: 0 }, { scale: 1, opacity: 1, duration: 0.18, ease: 'power3.out' });
    }
  }, []);

  // ---- drag to move (canvas elements) / line-mode click-to-connect ----
  const onPointerDown = useCallback((e: React.PointerEvent) => {
    if (e.button !== 0) return;
    const view = useView.getState();
    const board = useBoard.getState();

    // Interactive innards (inputs, editors, links) keep their own pointer flow.
    if ((e.target as HTMLElement).closest('input, textarea, [contenteditable="true"], a, button, select')) return;
    e.stopPropagation();

    const additive = e.shiftKey;
    if (!board.selection.has(element.id)) select([element.id], additive);
    else if (additive) select([element.id], true);

    if (element.content?.locked) return; // locked cards cannot be dragged (§5)

    const startX = e.clientX, startY = e.clientY;
    const scale = view.scale;
    const ids = board.selection.has(element.id) && board.selection.size > 1
      ? Array.from(board.selection)
      : [element.id];
    let moved = false;

    const trashTarget = () => document.querySelector('.rail [data-trash-drop]');
    const onMove = (ev: PointerEvent) => {
      const dx = (ev.clientX - startX) / scale;
      const dy = (ev.clientY - startY) / scale;
      if (!moved && Math.hypot(dx, dy) > 3) moved = true;
      if (moved) {
        useView.getState().setDrag({ ids, dx, dy });
        // Light up the rail's trash target when hovering it mid-drag.
        const over = document.elementFromPoint(ev.clientX, ev.clientY)?.closest('[data-trash-drop]');
        trashTarget()?.classList.toggle('drop-hot', !!over);
      }
    };
    const onUp = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      trashTarget()?.classList.remove('drop-hot');
      const d = useView.getState().drag;
      useView.getState().setDrag(null);
      if (!moved || !d) return;

      const state = useBoard.getState();
      const dropNode = document.elementFromPoint(ev.clientX, ev.clientY);

      // Dropping on the rail's trash deletes the whole dragged set.
      if (dropNode?.closest('[data-trash-drop]')) {
        const ops = d.ids
          .map((id) => state.elements[id])
          .filter((el): el is QElement => !!el)
          .map((el) => deleteOp(el));
        void state.commitTransaction(ops);
        state.clearSelection();
        return;
      }

      // Dropping over a column reparents into it AND positions by the
      // pointer, using fractional indexing so a reorder is one write (§4.9).
      const columnId = dropNode?.closest('[data-column-drop]')?.getAttribute('data-column-drop');
      const dragged = d.ids.map((id) => state.elements[id]).filter((el): el is QElement => !!el);

      if (columnId) {
        let baseIndex = columnInsertIndex(columnId, ev.clientY, new Set(d.ids));
        const ops = dragged
          .filter((el) => el.id !== columnId && !el.type.match(/^(COLUMN|LINE)$/))
          .map((el) => {
            const op = moveOp(el, { parentId: columnId, section: 'CANVAS', index: baseIndex });
            baseIndex += 0.0001; // keep multi-drops in order
            return op;
          });
        if (ops.length) void state.commitTransaction(ops);
        return;
      }

      // Dropping onto open canvas: reparent out of any column to the board,
      // translated by the drag delta. Preference: snap to a 20px grid.
      const { snapToGrid } = useSettings.getState().settings.preferences;
      const snap = (v: number) => (snapToGrid ? Math.round(v / 20) * 20 : v);
      const ops = dragged.map((el) => {
        const leavingColumn = el.location.parentId !== state.boardId && el.type !== 'LINE';
        if (leavingColumn) {
          const pt = useView.getState().toCanvas(ev.clientX, ev.clientY, document.querySelector('.canvas-viewport') as HTMLElement);
          return moveOp(el, { parentId: state.boardId, section: 'CANVAS', position: { x: snap(pt.x - 130), y: snap(pt.y - 30) } });
        }
        return moveOp(el, { position: { x: snap(el.location.position.x + d.dx), y: snap(el.location.position.y + d.dy) } });
      });
      void state.commitTransaction(ops);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }, [element, select]);

  // ---- resize handle (width; images/notes/columns) ----
  const onResizeStart = useCallback((e: React.PointerEvent) => {
    e.stopPropagation();
    const startX = e.clientX;
    const startW = ref.current?.offsetWidth ?? element.location.width ?? 260;
    const scale = useView.getState().scale;
    const onMove = (ev: PointerEvent) => {
      const w = Math.max(120, startW + (ev.clientX - startX) / scale);
      if (ref.current) ref.current.style.width = `${w}px`;
    };
    const onUp = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      const w = Math.max(120, startW + (ev.clientX - startX) / scale);
      void useBoard.getState().commitTransaction([moveOp(element, { width: w })]);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  }, [element]);

  // ---- floating actions ----
  const onDelete = useCallback(() => {
    const state = useBoard.getState();
    const ids = state.selection.size > 1 && state.selection.has(element.id)
      ? Array.from(state.selection) : [element.id];
    const ops = ids.map((id) => state.elements[id]).filter(Boolean).map((el) => deleteOp(el!));
    void state.commitTransaction(ops);
    state.clearSelection();
  }, [element.id]);

  const onDuplicate = useCallback(async () => {
    const created = await api.duplicate(element.id);
    const state = useBoard.getState();
    state.upsertElements(created);
    if (created[0]) state.select([created[0].id]);
  }, [element.id]);

  const onSyncCopy = useCallback(async () => {
    // Synced note (§4.15): a CLONE instance sharing this card's content.
    const clone = await api.convertToClone(element.id, element.location.parentId, {
      x: element.location.position.x + 40, y: element.location.position.y + 40,
    });
    useBoard.getState().upsertElements([clone]);
  }, [element]);

  const onContextMenu = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    const state = useBoard.getState();
    if (state.readOnly) return;
    if (!state.selection.has(element.id)) select([element.id]);
    const multi = state.selection.size > 1;
    const locked = !!element.content?.locked;

    // Text direction targets the content carrier: clones share their source
    // card's content, so the override lands on the source and syncs everywhere.
    const dirTarget = element.type === 'CLONE'
      ? state.elements[element.content?.cloneSourceId] ?? element
      : element;
    const dir = elementDir(dirTarget);
    const setDir = (next: TextDirection) =>
      void state.commitTransaction([updateOp(dirTarget, {
        content: { textDirection: next === 'auto' ? null : next },
      })]);

    const items = [
      { label: 'Duplicate', icon: <DuplicateIcon size={15} />, onClick: () => void onDuplicate() },
      ...(element.type === 'CARD' ? [{ label: 'Make synced copy', icon: <SyncIcon size={15} />, onClick: () => void onSyncCopy() }] : []),
      { label: locked ? 'Unlock' : 'Lock', icon: <LockIcon size={15} />, onClick: () => void state.commitTransaction([updateOp(element, { content: { locked: !locked } })]) },
      { label: 'Add label', icon: <LabelIcon size={15} />, onClick: () => useLabelPopover.getState().open(e.clientX, e.clientY, Array.from(useBoard.getState().selection)) },
      ...(hasTextDirection(element) ? [{
        label: t('dir.label'),
        icon: dir === 'rtl' ? <DirRtlIcon size={15} /> : dir === 'ltr' ? <DirLtrIcon size={15} /> : <DirAutoIcon size={15} />,
        sub: [
          { label: t('dir.auto'), icon: <DirAutoIcon size={15} />, checked: dir === 'auto', onClick: () => setDir('auto') },
          { label: t('dir.rtl'), icon: <DirRtlIcon size={15} />, checked: dir === 'rtl', onClick: () => setDir('rtl') },
          { label: t('dir.ltr'), icon: <DirLtrIcon size={15} />, checked: dir === 'ltr', onClick: () => setDir('ltr') },
        ],
      }] : []),
      ...(multi ? [{ label: 'Group into column', icon: <ColumnIcon size={15} />, onClick: groupIntoColumn }] : []),
      ...(element.type === 'BOARD' || element.type === 'ALIAS' ? [{
        label: 'Color & icon',
        icon: <PaletteIcon size={15} />,
        onClick: () => {
          // Aliases inherit the target board's look — customize the target.
          const targetId = element.type === 'ALIAS' ? element.content?.targetBoardId : element.id;
          if (targetId) useBoardStyle.getState().open(e.clientX, e.clientY, targetId, 'color');
        },
      }] : []),
      ...(element.type === 'BOARD' ? [{
        label: 'Rename',
        icon: <RenameIcon size={15} />,
        onClick: () => {
          void (async () => {
            const next = await prompt({
              title: 'Rename board',
              placeholder: 'Board name',
              defaultValue: element.content?.title ?? '',
              confirmLabel: 'Rename',
            });
            if (next?.trim()) void useBoard.getState().commitTransaction([updateOp(element, { content: { title: next.trim() } })]);
          })();
        },
      }] : []),
      ...(element.type === 'BOARD' ? [{ label: 'Create shortcut', icon: <AliasArrow size={15} />, onClick: () => void createShortcut(element) }] : []),
      ...(element.type === 'BOARD' ? [{
        label: element.content?.isTemplate ? 'Remove from templates' : 'Convert to template',
        icon: <TemplateIcon size={15} />,
        onClick: () => void state.commitTransaction([updateOp(element, { content: { isTemplate: element.content?.isTemplate ? null : true } })]),
      }] : []),
      { label: 'Delete', icon: <TrashIcon size={15} />, danger: true, divider: true, onClick: onDelete },
    ];
    useContextMenu.getState().open(e.clientX, e.clientY, items);
  }, [element, select, onDelete, onDuplicate, onSyncCopy]);

  const style: React.CSSProperties = inColumn
    ? {
        width: '100%',
        // Follow the pointer while dragging out of / within a column.
        transform: isDragging ? `translate(${drag!.dx}px, ${drag!.dy}px)` : undefined,
        position: isDragging ? 'relative' : undefined,
        zIndex: isDragging ? 30 : undefined,
      }
    : {
        left: element.location.position.x + (isDragging ? drag!.dx : 0),
        top: element.location.position.y + (isDragging ? drag!.dy : 0),
        width: element.location.width || undefined,
      };

  const cls = [
    'el',
    selected ? 'selected' : '',
    isDragging ? 'dragging' : '',
    remoteEditor ? 'remote-edit' : '',
    element.type === 'COLUMN' ? 'column' : '',
    element.type === 'BOARD' || element.type === 'ALIAS' ? 'board-shell' : '',
    element.content?.variant === 'heading' ? 'heading-el' : '',
  ].filter(Boolean).join(' ');

  // Milanote's board side-bar: Color / Icon / Rename appear beside a
  // selected board (aliases customize their target board's look).
  const isBoardish = element.type === 'BOARD' || element.type === 'ALIAS';
  const styleTargetId = element.type === 'ALIAS' ? element.content?.targetBoardId : element.id;
  const renameBoard = useCallback(() => {
    void (async () => {
      const next = await prompt({
        title: 'Rename board',
        placeholder: 'Board name',
        defaultValue: element.content?.title ?? '',
        confirmLabel: 'Rename',
      });
      if (next?.trim()) void useBoard.getState().commitTransaction([updateOp(element, { content: { title: next.trim() } })]);
    })();
  }, [element]);

  return (
    <div ref={ref} className={cls} style={style} onPointerDown={onPointerDown} onContextMenu={onContextMenu} data-element-id={element.id}>
      {remoteEditor && <div className="remote-edit-badge">{remoteEditor} is editing…</div>}
      {selected && (
        <div className="el-actions" onPointerDown={(e) => e.stopPropagation()}>
          {element.type === 'CARD' && <button title="Synced copy" onClick={onSyncCopy}><SyncIcon size={15} /></button>}
          <button title="Duplicate (Ctrl+D)" onClick={onDuplicate}><DuplicateIcon size={15} /></button>
          <button title="Delete" className="danger" onClick={onDelete}><TrashIcon size={15} /></button>
        </div>
      )}
      {selected && !isDragging && isBoardish && (
        <div className="board-actions" onPointerDown={(e) => e.stopPropagation()}>
          <button
            onClick={(e) => styleTargetId && useBoardStyle.getState().open(e.clientX + 14, e.clientY - 10, styleTargetId, 'color')}
          >
            <span className="ba-ico"><PaletteIcon size={16} /></span>
            <span>Color</span>
          </button>
          <button
            onClick={(e) => styleTargetId && useBoardStyle.getState().open(e.clientX + 14, e.clientY - 10, styleTargetId, 'icon')}
          >
            <span className="ba-ico"><BoardIcon size={16} /></span>
            <span>Icon</span>
          </button>
          {element.type === 'BOARD' && (
            <button onClick={renameBoard}>
              <span className="ba-ico"><RenameIcon size={16} /></span>
              <span>Rename</span>
            </button>
          )}
        </div>
      )}
      <LabelChips labelIds={element.labelIds} />
      <ElementView element={element} navigate={navigate} viewportRef={viewportRef} inColumn={inColumn} />
      {!inColumn && element.type !== 'BOARD' && element.type !== 'ALIAS' && (
        <div className="resize-handle" onPointerDown={onResizeStart} />
      )}
      {!inColumn && (
        <div
          className="connect-anchor"
          title="Drag to connect"
          onPointerDown={(e) => {
            // Drag-to-connect (§4.12): a ghost line follows the pointer;
            // releasing over a card connects to it, releasing on open canvas
            // leaves a free endpoint you can grab later.
            e.stopPropagation();
            e.preventDefault();
            const viewport = viewportRef.current ?? (document.querySelector('.canvas-viewport') as HTMLElement | null);
            if (!viewport) return;
            const view = useView.getState();
            const start = view.toCanvas(e.clientX, e.clientY, viewport);
            view.setLineDraft({ sourceId: element.id, x: start.x, y: start.y });

            const onMove = (ev: PointerEvent) => {
              const pt = useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);
              useView.getState().setLineDraft({ sourceId: element.id, x: pt.x, y: pt.y });
              highlightConnectTarget(ev, element.id);
            };
            const onUp = (ev: PointerEvent) => {
              window.removeEventListener('pointermove', onMove);
              window.removeEventListener('pointerup', onUp);
              highlightConnectTarget(null);
              useView.getState().setLineDraft(null);
              const state = useBoard.getState();
              const pt = useView.getState().toCanvas(ev.clientX, ev.clientY, viewport);
              const targetShell = document.elementFromPoint(ev.clientX, ev.clientY)?.closest('[data-element-id]');
              const targetId = targetShell?.getAttribute('data-element-id');
              if (Math.hypot(pt.x - start.x, pt.y - start.y) < 8 && !targetId) return; // accidental click
              const content: Record<string, any> = {
                fromId: element.id, color: '#8a86a0', weight: 2, endArrow: true, curve: 0, label: '',
              };
              if (targetId && targetId !== element.id && state.elements[targetId]?.type !== 'LINE') {
                content.toId = targetId;
              } else {
                content.toPoint = { x: pt.x, y: pt.y }; // free endpoint
              }
              const op = createOp('LINE', state.boardId, { content });
              void state.commitTransaction([op]);
              state.select([op.elementId]);
            };
            window.addEventListener('pointermove', onMove);
            window.addEventListener('pointerup', onUp);
          }}
        />
      )}
    </div>
  );
});

// columnInsertIndex returns a fractional location.index for dropping at the
// pointer's vertical position among a column's existing children (excluding
// the ones being dragged). Fractional indexing keeps a reorder a single write.
function columnInsertIndex(columnId: string, clientY: number, dragging: Set<string>): number {
  const state = useBoard.getState();
  const siblings = Object.values(state.elements)
    .filter((el) => el.location.parentId === columnId && !el.deletedAt && !dragging.has(el.id))
    .sort((a, b) => a.location.index - b.location.index);
  if (siblings.length === 0) return 1;

  // Find the first sibling whose DOM midpoint is below the pointer.
  for (let i = 0; i < siblings.length; i++) {
    const node = document.querySelector(`[data-element-id="${siblings[i].id}"]`);
    const rect = node?.getBoundingClientRect();
    if (rect && clientY < rect.top + rect.height / 2) {
      const prev = i === 0 ? siblings[0].location.index - 1 : siblings[i - 1].location.index;
      return (prev + siblings[i].location.index) / 2;
    }
  }
  return siblings[siblings.length - 1].location.index + 1; // append
}

// groupIntoColumn wraps the current multi-selection into a new column (§4.9).
function groupIntoColumn() {
  const state = useBoard.getState();
  const ids = Array.from(state.selection);
  const items = ids.map((id) => state.elements[id]).filter((e): e is QElement => !!e && e.type !== 'COLUMN' && e.type !== 'LINE');
  if (items.length === 0) return;
  const minX = Math.min(...items.map((e) => e.location.position.x));
  const minY = Math.min(...items.map((e) => e.location.position.y));
  const colId = newObjectId();
  const colOp = createOp('COLUMN', state.boardId, { position: { x: minX, y: minY }, width: 320, content: { title: '', collapsed: false } });
  colOp.elementId = colId;
  const moveOps = items.map((el, i) => moveOp(el, { parentId: colId, section: 'CANVAS', index: i + 1 }));
  void state.commitTransaction([colOp, ...moveOps]);
  state.clearSelection();
}

// createShortcut drops an ALIAS pointing at a board next to it (§4.16).
async function createShortcut(board: QElement) {
  const state = useBoard.getState();
  await state.commitTransaction([
    createOp('ALIAS', state.boardId, {
      position: { x: board.location.position.x + 40, y: board.location.position.y + 40 },
      content: { targetBoardId: board.id, title: board.content?.title ?? 'Board' },
    }),
  ]);
}
