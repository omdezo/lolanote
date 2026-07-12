// Topbar — glass chrome: brand mark, breadcrumb path, presence, undo/redo,
// search / unsorted / trash / export / share, all on the SVG icon set.
import { useState } from 'react';
import { exportBoardBlob } from '../api/client';
import { logout } from '../auth/keycloak';
import { useBoard } from '../store/boardStore';
import type { PanelKind } from '../App';
import { NotificationsBell } from './panels/NotificationsBell';
import {
  BoardIcon, ChevronIcon, ExportIcon, InboxIcon, LogoutIcon, RedoIcon, SearchIcon,
  ShareIcon, TemplateIcon, TrashIcon, UndoIcon,
} from './Icons';

interface Props {
  navigate: (boardId: string) => Promise<void>;
  panel: PanelKind;
  setPanel: (p: PanelKind) => void;
}

export function Topbar({ navigate, panel, setPanel }: Props) {
  const { user, boardId, boardTitle, breadcrumb, presence, undoStack, redoStack, undo, redo, role } = useBoard();
  const [exporting, setExporting] = useState(false);
  const isHome = user?.homeBoardId === boardId;

  const doExport = async (format: 'markdown' | 'text') => {
    setExporting(true);
    try {
      const blob = await exportBoardBlob(boardId, format);
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `${boardTitle || 'board'}.${format === 'markdown' ? 'md' : 'txt'}`;
      a.click();
      URL.revokeObjectURL(a.href);
    } finally {
      setExporting(false);
    }
  };

  const toggle = (p: PanelKind) => setPanel(panel === p ? 'none' : p);

  return (
    <div className="topbar">
      <button className="topbar-btn icon-only" title="Boards" onClick={() => toggle('boards')} style={{ marginRight: 2 }}>
        <BoardIcon size={18} />
      </button>
      <div className="brand">
        <div className="brand-mark">Q</div>
        <div className="brand-name">Qomra<em>Note</em></div>
      </div>

      <div className="breadcrumbs">
        {user && !isHome && (
          <>
            <button className="crumb" onClick={() => void navigate(user.homeBoardId)}>Home</button>
            <span className="crumb-sep"><ChevronIcon size={13} /></span>
          </>
        )}
        {breadcrumb.filter((b) => b.id !== user?.homeBoardId).map((b) => (
          <span key={b.id} style={{ display: 'inline-flex', alignItems: 'center' }}>
            <button className="crumb" onClick={() => void navigate(b.id)}>{b.title || 'Untitled'}</button>
            <span className="crumb-sep"><ChevronIcon size={13} /></span>
          </span>
        ))}
        <span className="crumb current">{isHome ? 'Home' : boardTitle}</span>
      </div>

      <div className="presence-stack">
        {Object.values(presence).slice(0, 5).map((p) => (
          <div key={p.clientId} className="avatar" title={p.name}>{(p.name || '?').slice(0, 2)}</div>
        ))}
      </div>

      <button className="topbar-btn icon-only" onClick={undo} disabled={undoStack.length === 0} title="Undo (Ctrl+Z)"><UndoIcon size={17} /></button>
      <button className="topbar-btn icon-only" onClick={redo} disabled={redoStack.length === 0} title="Redo (Ctrl+Shift+Z)"><RedoIcon size={17} /></button>
      <div className="topbar-divider" />
      <button className={`topbar-btn icon-only${panel === 'search' ? ' primary' : ''}`} onClick={() => toggle('search')} title="Search (Ctrl+F)"><SearchIcon size={17} /></button>
      <button className={`topbar-btn${panel === 'unsorted' ? ' primary' : ''}`} onClick={() => toggle('unsorted')} title="Unsorted tray">
        <InboxIcon size={17} /> Unsorted
      </button>
      <button className={`topbar-btn icon-only${panel === 'trash' ? ' primary' : ''}`} onClick={() => toggle('trash')} title="Trash"><TrashIcon size={17} /></button>
      <button className="topbar-btn icon-only" onClick={() => toggle('templates')} title="Templates">
        <TemplateIcon size={17} />
      </button>
      <NotificationsBell navigate={navigate} />
      <button className="topbar-btn icon-only" onClick={() => void doExport('markdown')} disabled={exporting || isHome}
        title={isHome ? 'The Home board cannot be exported' : 'Export as Markdown'}>
        <ExportIcon size={17} />
      </button>
      <button
        className="topbar-btn primary"
        onClick={() => toggle('share')}
        disabled={isHome || role !== 'owner'}
        title={isHome ? 'The Home board can never be shared' : 'Share this board'}
      >
        <ShareIcon size={16} /> Share
      </button>
      <div className="topbar-divider" />
      <button className="topbar-btn icon-only" onClick={logout} title={`Signed in as ${user?.displayName ?? ''} — log out`}><LogoutIcon size={17} /></button>
    </div>
  );
}
