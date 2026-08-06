// ElementShell wraps every card with the shared canvas behaviors: selection,
// dragging (multi-select drags commit ONE transaction, §9.5), resizing,
// size measurement for the line layer, the floating action bar, the line
// anchor, and column drop targets.
import { memo, useCallback, useEffect, useRef } from 'react';
import { tweenFromTo } from '../lib/motion';
import type { QElement } from '../api/types';
import { api } from '../api/client';
import { t } from '../i18n';
import { activateElement, isRenameable } from './activate';
import { elementDir, hasTextDirection, type TextDirection } from '../lib/direction';
import { createOp, deleteOp, moveOp, updateOp, useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import { isDestructive, kindLabel } from '../agent/agentStore';
import { useView } from '../store/viewStore';
import { highlightConnectTarget } from './LineLayer';
import { ElementView } from '../components/elements/ElementView';
import {
  AliasArrow, BoardIcon, ColumnIcon, DirAutoIcon, DirLtrIcon, DirRtlIcon, DuplicateIcon,
  LabelIcon, LockIcon, PaletteIcon, RenameIcon, ShieldIcon, SparkleIcon, SyncIcon, TemplateIcon, TrashIcon,
} from '../components/Icons';
import { useBoardStyle } from '../components/ui/BoardStylePopover';
import { useContextMenu } from '../components/ui/ContextMenu';
import { useAgent } from '../agent/agentStore';
import { LabelChips, useLabelPopover } from '../components/ui/LabelPopover';
import { prompt } from '../components/ui/Prompt';
import { toast } from '../components/ui/Toaster';
import { newObjectId } from '../lib/objectId';

interface Props {
  element: QElement;
  navigate: (boardId: string) => Promise<void>;
  viewportRef: React.RefObject<HTMLDivElement | null>;
  inColumn?: boolean;
  /**
   * The action kind a pending proposal would apply to this card, if any.
   *
   * A bare boolean until AX17, which is precisely the problem: the only signal
   * was a 38% dim, so "this card will be MOVED" and "this card will be DELETED"
   * were the same appearance, and neither was available to a screen reader or
   * to anyone whose vision the dim defeats.
   */
  proposedKind?: string;
}

/** Types that render at their own intrinsic size rather than filling the width
 *  their record carries. The shell must not be wider than what is drawn, or
 *  anything positioned against its edge ends up detached from it. */
const TILE_TYPES = new Set(['BOARD', 'ALIAS']);

/**
 * How many times any shell has rendered, and the instrument that keeps the
 * memo() above honest.
 *
 * This component was wrapped in memo() and then subscribed to the whole board
 * store, which made the memo unreachable — the wrapper looked like a
 * performance guarantee and was inoperative, and nothing could have told you.
 * An integer increment per render costs nothing and makes "a collaborator's
 * cursor re-renders zero cards" a fact a test can hold to rather than a claim
 * in a comment.
 */
let shellRenders = 0;
export function shellRenderCount(): number { return shellRenders; }

export const ElementShell = memo(function ElementShell({ element, navigate, viewportRef, inColumn, proposedKind }: Props) {
  const proposed = !!proposedKind;
  shellRenders += 1;
  const ref = useRef<HTMLDivElement>(null);
  // Derived booleans, not the containers they come from.
  //
  // This component is wrapped in memo() and then subscribed to the WHOLE board
  // store, which made the memo unreachable: a collaborator's cursor arrives at
  // 20 Hz per peer and replaces the root state object, so on a 1,000-element
  // board with four people pointing at things the canvas reconciled ~80,000
  // shells a second with nothing on the board changing. Selecting
  // `selection.has(id)` instead of `selection` makes the subscription
  // false→false for every card that is not this one, and zustand's Object.is
  // check stops the render before React sees it.
  const selected = useBoard((s) => s.selection.has(element.id));
  const select = useBoard((s) => s.select);
  const remoteEditor = useBoard((s) => s.remoteEditing[element.id]);
  // Same shape for the drag: `useView(s => s.drag)` handed every shell a fresh
  // object on every pointermove, so dragging ONE card re-rendered all 1,000.
  const isDragging = useView((s) => !!s.drag && s.drag.ids.includes(element.id));
  const dragDx = useView((s) => (s.drag?.ids.includes(element.id) ? s.drag.dx : 0));
  const dragDy = useView((s) => (s.drag?.ids.includes(element.id) ? s.drag.dy : 0));
  const labelFilter = useView((s) => s.labelFilter);
  // The roving index: exactly one card on the board is in the tab order.
  const isTabStop = useView((s) => s.focusedId === element.id);
  // Marked private: the agent's scope walk must skip it and its whole subtree,
  // and the card says so, because a rule you cannot see is a rule you cannot
  // rely on.
  const excluded = !!element.content?.agentExclude;
  // Label filter: cards without any selected label fade back.
  const labelDimmed = labelFilter.size > 0
    && element.type !== 'LINE'
    && !(element.labelIds ?? []).some((id) => labelFilter.has(id));

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
      tweenFromTo(ref.current, { scale: 0.92, opacity: 0 }, { scale: 1, opacity: 1, duration: 0.18, ease: 'power3.out' });
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

    // Second tap on something already selected opens it.
    //
    // On a coarse pointer there is no double-click that survives the drag
    // arming at 3px of finger drift, so OPEN did not exist on the device this
    // product installs to. First tap selects, second tap activates — and the
    // branch is on pointerType, not a global mode, so desktop double-click is
    // untouched.
    const coarse = e.pointerType === 'touch' || e.pointerType === 'pen';
    const wasSelected = board.selection.has(element.id);

    if (element.content?.locked) return; // locked cards cannot be dragged (§5)

    const startX = e.clientX, startY = e.clientY;
    const scale = view.scale;
    const ids = board.selection.has(element.id) && board.selection.size > 1
      ? Array.from(board.selection)
      : [element.id];
    let moved = false;
    // MO1's last clause: 3px is a mouse's slop, not a finger's. A tap on a
    // touchscreen drifts several pixels before it lifts, so at this threshold
    // every tap read as a drag — which is also what made the double-tap OPEN
    // gesture unreachable.
    const slop = coarse ? 10 : 3;

    const trashTarget = () => document.querySelector('.rail [data-trash-drop]');
    const onMove = (ev: PointerEvent) => {
      const dx = (ev.clientX - startX) / scale;
      const dy = (ev.clientY - startY) / scale;
      if (!moved && Math.hypot(dx, dy) > slop) moved = true;
      if (moved) {
        useView.getState().setDrag({ ids, dx, dy });
        // Light up the rail's trash target when hovering it mid-drag.
        const over = document.elementFromPoint(ev.clientX, ev.clientY)?.closest('[data-trash-drop]');
        trashTarget()?.classList.toggle('drop-hot', !!over);
      }
    };
    // MO4. `pointercancel` fires whenever the browser takes the gesture over —
    // a system edge swipe, an incoming call, the Android back gesture — and
    // when it does, `pointerup` never arrives. Without this the drag stayed in
    // `useView.drag` with these two window listeners still bound, and the card
    // sat frozen under an invisible finger until the page was reloaded.
    // Cancellation ends the gesture and writes nothing: a phantom move in the
    // undo history is worse than a lost drag.
    const detach = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onCancel);
    };
    const onCancel = () => {
      detach();
      trashTarget()?.classList.remove('drop-hot');
      useView.getState().setDrag(null);
    };
    const onUp = (ev: PointerEvent) => {
      detach();
      trashTarget()?.classList.remove('drop-hot');
      const d = useView.getState().drag;
      useView.getState().setDrag(null);
      if (!moved || !d) {
        if (!moved && coarse && wasSelected) activateElement(element, navigate);
        return;
      }

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

      // Dropping on a breadcrumb (or the Home crumb) files the selection
      // into that board's Unsorted tray — Milanote's move-up gesture (§5).
      const crumbBoard = dropNode?.closest('[data-crumb-board]')?.getAttribute('data-crumb-board');
      if (crumbBoard && crumbBoard !== state.boardId) {
        const movable = d.ids.filter((id) => {
          const el = state.elements[id];
          return el && el.type !== 'LINE' && !el.content?.isHome;
        });
        if (movable.length) {
          void api.moveElements(movable, crumbBoard).then(() => {
            void state.refreshBoard();
            toast.success(`${movable.length} · ${t('canvas.movedToUnsorted')}`);
          }).catch((err) => toast.error(err?.message || t('canvas.moveFailed')));
          state.clearSelection();
        }
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
    window.addEventListener('pointercancel', onCancel);
  }, [element, select, navigate]);

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
    const detach = () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onCancel);
    };
    // A cancelled resize restores the record's width rather than committing
    // whatever the element happened to be when the system stole the gesture.
    const onCancel = () => {
      detach();
      if (ref.current) ref.current.style.width = element.location.width ? `${element.location.width}px` : '';
    };
    const onUp = (ev: PointerEvent) => {
      detach();
      const w = Math.max(120, startW + (ev.clientX - startX) / scale);
      void useBoard.getState().commitTransaction([moveOp(element, { width: w })]);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onCancel);
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

  // Declared above openMenu because the menu's Rename row calls it: F2 and the
  // menu must reach the same dialog, or renaming has two behaviours.
  const renameBoard = useCallback(() => {
    void (async () => {
      const next = await prompt({
        title: t('menu.renameBoard'),
        placeholder: t('menu.boardName'),
        defaultValue: element.content?.title ?? '',
        confirmLabel: t('menu.rename'),
      });
      if (next?.trim()) void useBoard.getState().commitTransaction([updateOp(element, { content: { title: next.trim() } })]);
    })();
  }, [element]);

  // Takes coordinates rather than an event: the menu is also opened from the
  // keyboard (Shift+F10, and the Menu key), anchored to the shell's own rect,
  // because right-click is not an input every person has.
  const openMenu = useCallback((cx: number, cy: number) => {
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
      { label: t('menu.duplicate'), icon: <DuplicateIcon size={15} />, onClick: () => void onDuplicate() },
      ...(element.type === 'CARD' ? [{ label: t('menu.syncedCopy'), icon: <SyncIcon size={15} />, onClick: () => void onSyncCopy() }] : []),
      { label: locked ? t('menu.unlock') : t('menu.lock'), icon: <LockIcon size={15} />, onClick: () => void state.commitTransaction([updateOp(element, { content: { locked: !locked } })]) },
      { label: t('menu.addLabel'), icon: <LabelIcon size={15} />, onClick: () => useLabelPopover.getState().open(cx, cy, Array.from(useBoard.getState().selection)) },
      ...(hasTextDirection(element) ? [{
        label: t('dir.label'),
        icon: dir === 'rtl' ? <DirRtlIcon size={15} /> : dir === 'ltr' ? <DirLtrIcon size={15} /> : <DirAutoIcon size={15} />,
        sub: [
          { label: t('dir.auto'), icon: <DirAutoIcon size={15} />, checked: dir === 'auto', onClick: () => setDir('auto') },
          { label: t('dir.rtl'), icon: <DirRtlIcon size={15} />, checked: dir === 'rtl', onClick: () => setDir('rtl') },
          { label: t('dir.ltr'), icon: <DirLtrIcon size={15} />, checked: dir === 'ltr', onClick: () => setDir('ltr') },
        ],
      }] : []),
      ...(multi ? [{ label: t('menu.group'), icon: <ColumnIcon size={15} />, onClick: groupIntoColumn }] : []),
      ...(element.type === 'BOARD' || element.type === 'ALIAS' ? [{
        label: t('menu.colorIcon'),
        icon: <PaletteIcon size={15} />,
        onClick: () => {
          // Aliases inherit the target board's look — customize the target.
          const targetId = element.type === 'ALIAS' ? element.content?.targetBoardId : element.id;
          if (targetId) useBoardStyle.getState().open(cx, cy, targetId, 'color');
        },
      }] : []),
      ...(element.type === 'BOARD' ? [{
        label: t('menu.rename'),
        icon: <RenameIcon size={15} />,
        onClick: renameBoard,
      }] : []),
      ...(element.type === 'BOARD' ? [{ label: t('menu.shortcut'), icon: <AliasArrow size={15} />, onClick: () => void createShortcut(element) }] : []),
      ...(element.type === 'BOARD' ? [{
        label: element.content?.isTemplate ? t('menu.fromTemplate') : t('menu.toTemplate'),
        icon: <TemplateIcon size={15} />,
        onClick: () => void state.commitTransaction([updateOp(element, { content: { isTemplate: element.content?.isTemplate ? null : true } })]),
      }] : []),
      // Pointing at what you mean beats describing it. The agent already has a
      // "selection" scope; this is the entry point that made it reachable.
      ...(useAgent.getState().capabilities?.enabled ? [{
        label: multi ? `${t('menu.askAboutN')} ${state.selection.size}` : t('menu.askAbout'),
        icon: <SparkleIcon size={15} />,
        divider: true,
        onClick: () => useAgent.getState().setOpen(true, 'selection'),
      }] : []),
      // The other half of the same conversation, and the one that did not
      // exist: there was no way at any layer to tell the agent NOT to read
      // something. Cast medical notes, a distributor's numbers, an unsigned
      // contract and a private note about a crew member sit on the same board
      // as the shot list, and the only way to keep them out of a model context
      // was to keep them out of QomraNote.
      //
      // A property of the element, not a mode and not a per-request choice: it
      // survives moves, applies to every collaborator's run, and the board's
      // owner can make the decision rather than whoever happens to start the
      // next run.
      ...(useAgent.getState().capabilities?.enabled ? [{
        label: excluded ? t('agent.include') : t('agent.exclude'),
        icon: <ShieldIcon size={15} />,
        onClick: () => void state.commitTransaction(
          Array.from(multi ? state.selection : [element.id])
            .map((id) => state.elements[id])
            .filter((el): el is QElement => !!el)
            .map((el) => updateOp(el, { content: { agentExclude: excluded ? null : true } })),
        ),
      }] : []),
      { label: t('menu.delete'), icon: <TrashIcon size={15} />, danger: true, divider: true, onClick: onDelete },
    ];
    useContextMenu.getState().open(cx, cy, items);
  }, [element, select, onDelete, onDuplicate, onSyncCopy, excluded, renameBoard]);

  const onContextMenu = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    openMenu(e.clientX, e.clientY);
  }, [openMenu]);

  const style: React.CSSProperties = inColumn
    ? {
        width: '100%',
        // Follow the pointer while dragging out of / within a column.
        transform: isDragging ? `translate(${dragDx}px, ${dragDy}px)` : undefined,
        position: isDragging ? 'relative' : undefined,
        zIndex: isDragging ? 30 : undefined,
      }
    : {
        left: element.location.position.x + dragDx,
        top: element.location.position.y + dragDy,
        // A board renders as a fixed 148px tile, whatever width its record
        // carries — and everything anchored to the shell's edge (the
        // drag-to-connect dot, the action bar) was hanging off a 280px box
        // around a 148px picture, floating in empty canvas a hundred pixels
        // clear of the thing it belonged to.
        width: TILE_TYPES.has(element.type) ? undefined : element.location.width || undefined,
      };

  const cls = [
    'el',
    selected ? 'selected' : '',
    excluded ? 'agent-excluded' : '',
    isDragging ? 'dragging' : '',
    remoteEditor ? 'remote-edit' : '',
    labelDimmed ? 'label-dim' : '',
    element.type === 'COLUMN' ? 'column' : '',
    element.type === 'BOARD' || element.type === 'ALIAS' ? 'board-shell' : '',
    element.content?.variant === 'heading' ? 'heading-el' : '',
    proposed ? 'agent-proposed' : '',
  ].filter(Boolean).join(' ');

  // Milanote's board side-bar: Color / Icon / Rename appear beside a
  // selected board (aliases customize their target board's look).
  const isBoardish = element.type === 'BOARD' || element.type === 'ALIAS';
  const styleTargetId = element.type === 'ALIAS' ? element.content?.targetBoardId : element.id;

  /**
   * The keyboard's half of OPEN.
   *
   * Enter activates through the same handler the mouse's double-click uses, so
   * the two cannot drift. F2 renames a board or a column — the only rename path
   * used to be the context menu, which was itself pointer-only. Shift+F10 and
   * the Menu key open that menu anchored to this card, because right-click is
   * not an input every person has.
   */
  const onKeyDown = useCallback((e: React.KeyboardEvent) => {
    // Innards own their own keys: pressing Enter in a task field must not read
    // as "open the card the field is inside".
    if (e.target !== e.currentTarget) return;
    if (e.key === 'Enter') {
      if (activateElement(element, navigate)) { e.preventDefault(); e.stopPropagation(); }
      return;
    }
    if (e.key === 'F2' && isRenameable(element)) {
      e.preventDefault();
      e.stopPropagation();
      renameBoard();
      return;
    }
    if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
      e.preventDefault();
      e.stopPropagation();
      const rect = ref.current?.getBoundingClientRect();
      openMenu((rect?.left ?? 0) + 12, (rect?.top ?? 0) + 12);
    }
  }, [element, navigate, openMenu, renameBoard]);

  const label = (element.content?.title as string)
    || (element.content?.textPreview as string)
    || (element.content?.filename as string)
    || element.type.toLowerCase().replace('_', ' ');

  return (
    <div
      ref={ref}
      className={cls}
      style={style}
      // One tab stop for the whole board, not one per card. Cards outside it
      // stay programmatically focusable (-1), which is what lets an arrow key
      // move to them.
      tabIndex={isTabStop ? 0 : -1}
      role="group"
      aria-label={[
        label,
        excluded ? t('agent.excluded') : '',
        // AX17: the proposal reaches the accessible name, not only the opacity.
        proposedKind ? `${t('agent.pending')}: ${kindLabel(proposedKind, t)}` : '',
      ].filter(Boolean).join(' — ')}
      // Focus arriving here by any route — Tab, an arrow, a click — makes this
      // the tab stop, so the next Tab out and back returns to where the person
      // was rather than to the top-left of the board.
      onFocus={(e) => { if (e.target === e.currentTarget) useView.getState().setFocused(element.id); }}
      onKeyDown={onKeyDown}
      onPointerDown={onPointerDown}
      onContextMenu={onContextMenu}
      data-element-id={element.id}
    >
      {/* AX17. The dim survives as ambient signal and stops being the ONLY
          signal: a persistent badge naming the action, in the same words
          `kindLabel()` gives the ghost head, so the live card and its ghost say
          the same thing. The dim itself moved from 0.38 to 0.62 and the rest of
          the difference is carried by a dashed border matching the ghost
          treatment — so "proposal" reads as one visual language across the
          ghost layer and the live layer, and the text a person is being asked
          to approve stays readable while they approve it. */}
      {proposedKind && (
        <div className={`el-pending${isDestructive(proposedKind) ? ' danger' : ''}`}>
          {kindLabel(proposedKind, t)}
        </div>
      )}
      {remoteEditor && <div className="remote-edit-badge">{remoteEditor} {t('canvas.isEditing')}</div>}
      {excluded && (
        <div className="el-private" title={t('agent.excluded')}>
          <ShieldIcon size={11} aria-hidden="true" /> {t('agent.excluded')}
        </div>
      )}
      {selected && (
        <div className="el-actions" onPointerDown={(e) => e.stopPropagation()}>
          {element.type === 'CARD' && <button title={t('a11y.syncedCopy')} aria-label={t('a11y.syncedCopy')} onClick={onSyncCopy}><SyncIcon size={15} /></button>}
          <button title={t('a11y.duplicate')} aria-label={t('a11y.duplicate')} onClick={onDuplicate}><DuplicateIcon size={15} /></button>
          <button title={t('a11y.delete')} aria-label={t('a11y.delete')} className="danger" onClick={onDelete}><TrashIcon size={15} /></button>
        </div>
      )}
      {selected && !isDragging && isBoardish && (
        <div className="board-actions" onPointerDown={(e) => e.stopPropagation()}>
          <button
            onClick={(e) => styleTargetId && useBoardStyle.getState().open(e.clientX + 14, e.clientY - 10, styleTargetId, 'color')}
          >
            <span className="ba-ico"><PaletteIcon size={16} /></span>
            <span>{t('menu.color')}</span>
          </button>
          <button
            onClick={(e) => styleTargetId && useBoardStyle.getState().open(e.clientX + 14, e.clientY - 10, styleTargetId, 'icon')}
          >
            <span className="ba-ico"><BoardIcon size={16} /></span>
            <span>{t('menu.icon')}</span>
          </button>
          {element.type === 'BOARD' && (
            <button onClick={renameBoard}>
              <span className="ba-ico"><RenameIcon size={16} /></span>
              <span>{t('menu.rename')}</span>
            </button>
          )}
        </div>
      )}
      <LabelChips labelIds={element.labelIds} />
      <ElementView element={element} navigate={navigate} viewportRef={viewportRef} inColumn={inColumn} />
      {!inColumn && element.type !== 'BOARD' && element.type !== 'ALIAS' && (
        <div className="resize-handle" aria-hidden="true" onPointerDown={onResizeStart} />
      )}
      {!inColumn && (
        <div
          className="connect-anchor"
          aria-hidden="true"
          title={t('a11y.connect')}
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
            const detach = () => {
              window.removeEventListener('pointermove', onMove);
              window.removeEventListener('pointerup', onUp);
              window.removeEventListener('pointercancel', onCancel);
            };
            // A cancelled connect leaves the draft line on screen forever
            // otherwise — and must not create a LINE nobody drew.
            const onCancel = () => {
              detach();
              highlightConnectTarget(null);
              useView.getState().setLineDraft(null);
            };
            const onUp = (ev: PointerEvent) => {
              detach();
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
            window.addEventListener('pointercancel', onCancel);
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
