// Global search (§3.5): Ctrl/⌘+F overlay spanning the current board and
// every board you own, sorted by last modified. Clicking a result jumps to
// its board.
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../../api/client';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import {
  BoardIcon, CloseIcon, ColorIcon, ColumnIcon, CommentIcon, DocumentIcon,
  FileIcon, ImageIcon, LinkIcon, NoteIcon, TableIcon, TodoIcon,
} from '../Icons';

interface Props { onClose: () => void; navigate: (boardId: string) => Promise<void> }

const typeIcons: Record<string, JSX.Element> = {
  BOARD: <BoardIcon size={16} />, CARD: <NoteIcon size={16} />, DOCUMENT: <DocumentIcon size={16} />,
  LINK: <LinkIcon size={16} />, IMAGE: <ImageIcon size={16} />, FILE: <FileIcon size={16} />,
  TASK_LIST: <TodoIcon size={16} />, TASK: <TodoIcon size={16} />, COLUMN: <ColumnIcon size={16} />,
  COLOR_SWATCH: <ColorIcon size={16} />, COMMENT_THREAD: <CommentIcon size={16} />, TABLE: <TableIcon size={16} />,
};

export function SearchOverlay({ onClose, navigate }: Props) {
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<QElement[]>([]);
  const [searching, setSearching] = useState(false);
  const [scope, setScope] = useState<'board' | 'everywhere'>('everywhere');
  const { boardId, elements } = useBoard();
  const inputRef = useRef<HTMLInputElement>(null);
  const debounce = useRef<number>(0);

  // "This board" keeps only hits living on the open board (the board itself,
  // direct children, or children of its columns/lists — all in the store).
  const visible = useMemo(() => {
    if (scope === 'everywhere') return results;
    return results.filter((el) => el.id === boardId
      || el.location.parentId === boardId
      || !!elements[el.location.parentId]);
  }, [results, scope, boardId, elements]);

  useEffect(() => inputRef.current?.focus(), []);

  useEffect(() => {
    window.clearTimeout(debounce.current);
    if (!query.trim()) { setResults([]); return; }
    debounce.current = window.setTimeout(async () => {
      setSearching(true);
      try {
        setResults(await api.search(query.trim()));
      } finally {
        setSearching(false);
      }
    }, 250);
    return () => window.clearTimeout(debounce.current);
  }, [query]);

  const open = async (el: QElement) => {
    // Boards open directly; anything else opens the board that contains it.
    const target = el.type === 'BOARD' ? el.id : el.location.parentId;
    onClose();
    if (target) await navigate(target).catch(() => undefined);
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head"><h3>Search everything</h3><button className="panel-close" onClick={onClose}><CloseIcon size={15} /></button></div>
        <div className="modal-body">
          <input
            ref={inputRef}
            className="search-input"
            dir="auto"
            placeholder="Search notes, boards, links, files…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && visible[0]) void open(visible[0]); }}
          />
          <div className="segmented" style={{ marginTop: 10 }}>
            <button className={scope === 'board' ? 'on' : ''} onClick={() => setScope('board')}>This board</button>
            <button className={scope === 'everywhere' ? 'on' : ''} onClick={() => setScope('everywhere')}>Everywhere</button>
          </div>
          <div style={{ marginTop: 10 }}>
            {searching && <div style={{ color: '#9a97a5', fontSize: 13, padding: 8 }}>Searching…</div>}
            {!searching && query.trim() && visible.length === 0 && (
              <div style={{ color: '#9a97a5', fontSize: 13, padding: 8 }}>No matches.</div>
            )}
            {visible.map((el) => (
              <div key={el.id} className="search-result" onClick={() => void open(el)}>
                <span className="sr-icon">{typeIcons[el.type] ?? <FileIcon size={16} />}</span>
                <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {el.content?.title || el.content?.textPreview || el.content?.filename || el.content?.url || '(untitled)'}
                </span>
                <span className="sr-type">{el.type.toLowerCase().replace('_', ' ')}</span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
