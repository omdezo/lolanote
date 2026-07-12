// Dispatcher: renders the right component for each of the 19 element types.
// Unrecognized types render a graceful UNKNOWN fallback — the same
// forward-compatibility mechanism Milanote ships (§9.4).
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { NoteCard } from './NoteCard';
import { ColumnView } from './ColumnView';
import { TaskListView } from './TaskListView';
import { TableCard } from './TableCard';
import { DocumentCard } from './DocumentCard';
import { BoardCard, CommentCard, FileCard, ImageCard, LinkCard, SketchCard, SwatchCard } from './cards';

export interface ElementViewProps {
  element: QElement;
  navigate: (boardId: string) => Promise<void>;
  viewportRef: React.RefObject<HTMLDivElement | null>;
}

export function ElementView(props: ElementViewProps) {
  const { element } = props;
  const elements = useBoard((s) => s.elements);

  switch (element.type) {
    case 'CARD':
      return <NoteCard element={element} />;
    case 'DOCUMENT':
      return <DocumentCard element={element} />;
    case 'TABLE':
      return <TableCard element={element} />;
    case 'CLONE': {
      // Synced note (§4.15): render (and edit) the shared source content.
      const source = elements[element.content?.cloneSourceId];
      return source
        ? <NoteCard element={source} cloneShellId={element.id} />
        : <div className="note-card" style={{ color: '#9a97a5', fontSize: 12 }}>Synced note unavailable</div>;
    }
    case 'BOARD':
    case 'ALIAS':
      return <BoardCard {...props} />;
    case 'COLUMN':
      return <ColumnView {...props} />;
    case 'TASK_LIST':
      return <TaskListView element={element} />;
    case 'LINK':
      return <LinkCard element={element} />;
    case 'IMAGE':
      return <ImageCard element={element} />;
    case 'FILE':
      return <FileCard element={element} />;
    case 'COLOR_SWATCH':
      return <SwatchCard element={element} />;
    case 'SKETCH':
      return <SketchCard element={element} />;
    case 'COMMENT_THREAD':
      return <CommentCard element={element} />;
    case 'SKELETON':
      return <div className="note-card" style={{ opacity: 0.4 }}>Loading…</div>;
    default:
      return (
        <div className="note-card" style={{ color: '#9a97a5', fontSize: 12 }}>
          Unsupported element ({element.type}) — kept safe for newer clients.
        </div>
      );
  }
}
