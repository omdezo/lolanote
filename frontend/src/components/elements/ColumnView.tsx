// Column (§4.9): a vertical container that adds structure to the freeform
// canvas. Children order by fractional location.index; dropping a card onto
// the column (handled in ElementShell) reparents it; the badge shows the
// live count; collapse hides the body.
import { useMemo, useState } from 'react';
import { dirAttr, elementDir } from '../../lib/direction';
import { createOp, updateOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { ElementShell } from '../../canvas/ElementShell';
import type { ElementViewProps } from './ElementView';
import { statLineFor } from './cards';
import { useT } from '../../i18n';
import { useProposedIds } from '../../agent/GhostLayer';
import { ChevronIcon, PlusIcon } from '../Icons';

export function ColumnView({ element, navigate, viewportRef }: ElementViewProps) {
  const elements = useBoard((s) => s.elements);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const t = useT();
  // Columns are where the agent does most of its filing, and they were the one
  // place the preview said nothing: root-canvas shells got `proposed`, children
  // inside a column got none. A plan moving six cards out of a column showed
  // nothing at all — ghosts are suppressed (the destination is another canvas),
  // the tile badge only counts things going IN, and the source cards sat
  // undimmed exactly where they were.
  const proposedIds = useProposedIds();
  const [title, setTitle] = useState<string | null>(null);

  const children = useMemo(
    () =>
      Object.values(elements)
        .filter((el) => el.location.parentId === element.id && !el.deletedAt && el.type !== 'LINE')
        .sort((a, b) => a.location.index - b.location.index),
    [elements, element.id],
  );

  const collapsed = !!element.content?.collapsed;

  // "3 boards, 1 file" subtitle under the title, like Milanote's columns.
  const stats: Record<string, number> = {};
  for (const child of children) {
    const t = child.type === 'ALIAS' ? 'BOARD' : child.type;
    stats[t] = (stats[t] ?? 0) + 1;
  }
  const statLine = statLineFor(stats);

  const commitTitle = () => {
    if (title !== null && title !== element.content?.title) {
      void commitTransaction([updateOp(element, { content: { title } })]);
    }
    setTitle(null);
  };

  const addNote = () => {
    const op = createOp('CARD', element.id, {
      index: (children[children.length - 1]?.location.index ?? 0) + 1,
      content: { doc: null, textPreview: '' },
    });
    void commitTransaction([op]);
    useView.getState().setEditing(op.elementId);
  };

  return (
    <div data-column-drop={element.id}>
      <div className="column-header">
        <button
          title={t(collapsed ? 'a11y.expand' : 'a11y.collapse')}
          aria-label={t(collapsed ? 'a11y.expand' : 'a11y.collapse')}
          aria-expanded={!collapsed}
          className={`column-collapse${collapsed ? ' closed' : ''}`}
          onClick={(e) => {
            e.stopPropagation();
            void commitTransaction([updateOp(element, { content: { collapsed: !collapsed } })]);
          }}
          style={{ transform: collapsed ? 'rotate(0deg)' : 'rotate(90deg)' }}
        >
          <ChevronIcon size={13} />
        </button>
        <div className="column-title-wrap">
          <input
            className="column-title"
            dir={dirAttr(elementDir(element))}
            aria-label={t('tool.column')}
            value={title ?? element.content?.title ?? ''}
            placeholder={t('column.titlePlaceholder')}
            onChange={(e) => setTitle(e.target.value)}
            onBlur={commitTitle}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
          />
          {statLine && <div className="column-stats">{statLine}</div>}
        </div>
        <span className="column-count">{children.length}</span>
      </div>
      {!collapsed && (
        <>
          <div className="column-body">
            {children.map((child) => (
              <ElementShell
                key={child.id}
                element={child}
                navigate={navigate}
                viewportRef={viewportRef}
                inColumn
                proposedKind={proposedIds.get(child.id)}
              />
            ))}
          </div>
          <button className="column-add" onClick={(e) => { e.stopPropagation(); addNote(); }}>
            <PlusIcon size={13} /> {t('a11y.addNote')}
          </button>
        </>
      )}
    </div>
  );
}
