// Topbar — glass chrome: brand mark, breadcrumb path, presence, undo/redo,
// search / unsorted / trash / export / share, and the avatar menu (settings,
// log out). Labels flow through i18n so the language setting applies live.
import { useEffect, useRef, useState } from 'react';
import { exportBoardBlob } from '../api/client';
import { logout } from '../auth/keycloak';
import { t } from '../i18n';
import { useBoard } from '../store/boardStore';
import { useSettings } from '../store/settingsStore';
import type { PanelKind } from '../App';
import { NotificationsBell } from './panels/NotificationsBell';
import {
  BoardIcon, ChevronIcon, ExportIcon, InboxIcon, LogoutIcon, RedoIcon, SearchIcon,
  SettingsIcon, ShareIcon, TemplateIcon, TrashIcon, UndoIcon,
} from './Icons';

interface Props {
  navigate: (boardId: string) => Promise<void>;
  panel: PanelKind;
  setPanel: (p: PanelKind) => void;
}

export function Topbar({ navigate, panel, setPanel }: Props) {
  const { user, boardId, boardTitle, breadcrumb, presence, undoStack, redoStack, undo, redo, role } = useBoard();
  // Subscribing to the language keeps every t() label live on change.
  useSettings((s) => s.settings.localization.language);
  const [exporting, setExporting] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const isHome = user?.homeBoardId === boardId;

  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e: PointerEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false);
    };
    window.addEventListener('pointerdown', onDown);
    return () => window.removeEventListener('pointerdown', onDown);
  }, [menuOpen]);

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
  const initials = (user?.displayName || '?').split(/\s+/).map((w) => w[0]).slice(0, 2).join('');

  return (
    <div className="topbar">
      <button className="topbar-btn icon-only" title={t('topbar.boards')} onClick={() => toggle('boards')} style={{ marginRight: 2 }}>
        <BoardIcon size={18} />
      </button>
      <div className="brand">
        <div className="brand-mark">Q</div>
        <div className="brand-name">Qomra<em>Note</em></div>
      </div>

      <div className="breadcrumbs">
        {user && !isHome && (
          <>
            <button className="crumb" onClick={() => void navigate(user.homeBoardId)}>{t('app.home')}</button>
            <span className="crumb-sep"><ChevronIcon size={13} /></span>
          </>
        )}
        {breadcrumb.filter((b) => b.id !== user?.homeBoardId).map((b) => (
          <span key={b.id} style={{ display: 'inline-flex', alignItems: 'center' }}>
            <button className="crumb" onClick={() => void navigate(b.id)}>{b.title || t('app.untitled')}</button>
            <span className="crumb-sep"><ChevronIcon size={13} /></span>
          </span>
        ))}
        <span className="crumb current">{isHome ? t('app.home') : boardTitle}</span>
      </div>

      <div className="presence-stack">
        {Object.values(presence).slice(0, 5).map((p) => (
          <div key={p.clientId} className="avatar" title={p.name}>{(p.name || '?').slice(0, 2)}</div>
        ))}
      </div>

      <button className="topbar-btn icon-only" onClick={undo} disabled={undoStack.length === 0} title={`${t('topbar.undo')} (Ctrl+Z)`}><UndoIcon size={17} /></button>
      <button className="topbar-btn icon-only" onClick={redo} disabled={redoStack.length === 0} title={`${t('topbar.redo')} (Ctrl+Shift+Z)`}><RedoIcon size={17} /></button>
      <div className="topbar-divider" />
      <button className={`topbar-btn icon-only${panel === 'search' ? ' primary' : ''}`} onClick={() => toggle('search')} title={`${t('topbar.search')} (Ctrl+F)`}><SearchIcon size={17} /></button>
      <button className={`topbar-btn${panel === 'unsorted' ? ' primary' : ''}`} onClick={() => toggle('unsorted')} title={t('topbar.unsorted')}>
        <InboxIcon size={17} /> {t('topbar.unsorted')}
      </button>
      <button className={`topbar-btn icon-only${panel === 'trash' ? ' primary' : ''}`} onClick={() => toggle('trash')} title={t('topbar.trash')}><TrashIcon size={17} /></button>
      <button className="topbar-btn icon-only" onClick={() => toggle('templates')} title={t('topbar.templates')}>
        <TemplateIcon size={17} />
      </button>
      <NotificationsBell navigate={navigate} />
      <button className="topbar-btn icon-only" onClick={() => void doExport('markdown')} disabled={exporting || isHome}
        title={isHome ? t('topbar.homeNoExport') : t('topbar.export')}>
        <ExportIcon size={17} />
      </button>
      <button
        className="topbar-btn primary"
        onClick={() => toggle('share')}
        disabled={isHome || role !== 'owner'}
        title={isHome ? t('topbar.homeNoShare') : t('topbar.shareThis')}
      >
        <ShareIcon size={16} /> {t('topbar.share')}
      </button>
      <div className="topbar-divider" />

      <div className="avatar-menu-wrap" ref={menuRef}>
        <button className="avatar-btn" onClick={() => setMenuOpen(!menuOpen)} title={user?.displayName ?? ''}>
          {initials}
        </button>
        {menuOpen && (
          <div className="avatar-dropdown">
            <div className="ad-head">
              <div className="ad-name">{user?.displayName}</div>
              <div className="ad-email">{user?.email}</div>
            </div>
            <button className="ad-item" onClick={() => { setMenuOpen(false); setPanel('settings'); }}>
              <span className="ad-icon"><SettingsIcon size={16} /></span> {t('topbar.settings')}
            </button>
            <div className="ad-sep" />
            <button className="ad-item" onClick={logout}>
              <span className="ad-icon"><LogoutIcon size={16} /></span> {t('topbar.logout')}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
