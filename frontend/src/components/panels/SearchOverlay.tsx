// Global search (§3.5): Ctrl/⌘+F overlay spanning the current board and
// every board you own, sorted by last modified. Clicking a result jumps to
// its board.
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../../api/client';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { useT, type TKey } from '../../i18n';
import { Modal } from '../ui/Modal';
import {
  BoardIcon, CloseIcon, ColorIcon, ColumnIcon, CommentIcon, DocumentIcon,
  FileIcon, ImageIcon, LinkIcon, NoteIcon, TableIcon, TodoIcon,
} from '../Icons';

interface Props { onClose: () => void; navigate: (boardId: string) => Promise<void> }

/** The element type, said in the reader's language. It used to be
 *  `el.type.toLowerCase().replace('_',' ')` — "comment_thread" as English
 *  shouted at an Arabic reader, in the one list they navigate by. */
const TYPE_KEYS: Record<string, TKey> = {
  BOARD: 'search.type.board', CARD: 'search.type.card', DOCUMENT: 'search.type.document',
  LINK: 'search.type.link', IMAGE: 'search.type.image', FILE: 'search.type.file',
  TASK_LIST: 'search.type.taskList', TASK: 'search.type.task', COLUMN: 'search.type.column',
  COLOR_SWATCH: 'search.type.swatch', COMMENT_THREAD: 'search.type.comment', TABLE: 'search.type.table',
};

const typeIcons: Record<string, JSX.Element> = {
  BOARD: <BoardIcon size={16} />, CARD: <NoteIcon size={16} />, DOCUMENT: <DocumentIcon size={16} />,
  LINK: <LinkIcon size={16} />, IMAGE: <ImageIcon size={16} />, FILE: <FileIcon size={16} />,
  TASK_LIST: <TodoIcon size={16} />, TASK: <TodoIcon size={16} />, COLUMN: <ColumnIcon size={16} />,
  COLOR_SWATCH: <ColorIcon size={16} />, COMMENT_THREAD: <CommentIcon size={16} />, TABLE: <TableIcon size={16} />,
};

export function SearchOverlay({ onClose, navigate }: Props) {
  const t = useT();
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

  // What the polite region says. Recomputed rather than pushed, so it changes
  // exactly when the list does and never announces a stale count.
  const status = searching
    ? t('search.searching')
    : !query.trim()
      ? ''
      : visible.length === 0
        ? t('search.none')
        : `${visible.length} ${visible.length === 1 ? t('search.result') : t('search.results')}`;

  return (
    <Modal
      title={t('search.title')}
      overlayId="search-overlay"
      onClose={onClose}
      headExtra={<button className="panel-close" aria-label={t('common.close')} title={t('common.close')} onClick={onClose}><CloseIcon size={15} /></button>}
    >
      <div className="modal-body">
          <input
            ref={inputRef}
            className="search-input"
            dir="auto"
            aria-label={t('topbar.search')}
            placeholder={t('search.placeholder')}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && visible[0]) void open(visible[0]); }}
          />
          {/* Two scopes with the current one carried by a CSS class alone —
              so both announced identically and neither said which was on. */}
          <div className="segmented" role="radiogroup" aria-label={t('search.scope')} style={{ marginTop: 10 }}>
            <button role="radio" aria-checked={scope === 'board'} className={scope === 'board' ? 'on' : ''} onClick={() => setScope('board')}>{t('search.thisBoard')}</button>
            <button role="radio" aria-checked={scope === 'everywhere'} className={scope === 'everywhere' ? 'on' : ''} onClick={() => setScope('everywhere')}>{t('search.everywhere')}</button>
          </div>
          {/* The result count, spoken on debounce settle. Search is the one
              navigation surface that does not need a pointer on the canvas,
              which makes it the natural primary interface for a keyboard user —
              and until now it never said how many things it had found. */}
          <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">{status}</div>
          <div style={{ marginTop: 10 }}>
            {searching && <div className="search-note">{t('search.searching')}</div>}
            {!searching && query.trim() && visible.length === 0 && (
              <div className="search-note">{t('search.none')}</div>
            )}
            {visible.map((el) => (
              // AX23. These were `<div onClick>`: not buttons, no tabIndex, no
              // key handler. The ONLY keyboard path through the product's one
              // cross-board discovery surface was Enter, which opened
              // `visible[0]` — so a keyboard user could reach the first hit and
              // no other. It is also the fallback AX1 leans on ("if you cannot
              // navigate the canvas, find things by name"), which means the
              // fallback did not exist either.
              <button key={el.id} type="button" className="search-result" onClick={() => void open(el)}>
                <span className="sr-icon" aria-hidden="true">{typeIcons[el.type] ?? <FileIcon size={16} />}</span>
                <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {el.content?.title || el.content?.textPreview || el.content?.filename || el.content?.url || t('search.untitled')}
                </span>
                <span className="sr-type">{t(TYPE_KEYS[el.type] ?? 'search.type.other')}</span>
              </button>
            ))}
          </div>
      </div>
    </Modal>
  );
}
