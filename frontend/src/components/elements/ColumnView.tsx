// Column (§4.9): a vertical container that adds structure to the freeform
// canvas. Children order by fractional location.index; dropping a card onto
// the column (handled in ElementShell) reparents it; the badge shows the
// live count; collapse hides the body.
import { useMemo, useState } from 'react';
import type { QElement } from '../../api/types';
import { createOp, updateOp, useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import { ElementShell } from '../../canvas/ElementShell';
import type { ElementViewProps } from './ElementView';
import { ChevronIcon, PlusIcon } from '../Icons';

export function ColumnView({ element, navigate, viewportRef }: ElementViewProps) {
  const { elements, commitTransaction } = useBoard();
  const [title, setTitle] = useState<string | null>(null);

  const children = useMemo(
    () =>
      Object.values(elements)
        .filter((el) => el.location.parentId === element.id && !el.deletedAt && el.type !== 'LINE')
        .sort((a, b) => a.location.index - b.location.index),
    [elements, element.id],
  );

  const collapsed = !!element.content?.collapsed;

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
          title={collapsed ? 'Expand' : 'Collapse'}
          className={`column-collapse${collapsed ? ' closed' : ''}`}
          onClick={(e) => {
            e.stopPropagation();
            void commitTransaction([updateOp(element, { content: { collapsed: !collapsed } })]);
          }}
          style={{ transform: collapsed ? 'rotate(0deg)' : 'rotate(90deg)' }}
        >
          <ChevronIcon size={13} />
        </button>
        <input
          className="column-title"
          value={title ?? element.content?.title ?? ''}
          placeholder="Column title"
          onChange={(e) => setTitle(e.target.value)}
          onBlur={commitTitle}
          onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
        />
        <span className="column-count">{children.length}</span>
      </div>
      {!collapsed && (
        <>
          <div className="column-body">
            {children.map((child) => (
              <ElementShell key={child.id} element={child} navigate={navigate} viewportRef={viewportRef} inColumn />
            ))}
          </div>
          <button className="column-add" onClick={(e) => { e.stopPropagation(); addNote(); }}>
            <PlusIcon size={13} /> Add a note
          </button>
        </>
      )}
    </div>
  );
}
