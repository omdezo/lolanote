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

// presenceColor gives each collaborator a stable hue from their identity.
export function presenceColor(seed: string): string {
  let h = 0;
  for (const ch of seed) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return `hsl(${h % 360}, 62%, 48%)`;
}

export function Topbar({ navigate, panel, setPanel }: Props) {
  const { user, boardId, boardTitle, breadcrumb, presence, undoStack, redoStack, undo, redo, role } = useBoard();
  // Subscribing to the language keeps every t() label live on change.
  useSettings((s) => s.settings.localization.language);
  const [exporting, setExporting] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [exportOpen, setExportOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const exportRef = useRef<HTMLDivElement>(null);
  const isHome = user?.homeBoardId === boardId;

  useEffect(() => {
    if (!menuOpen && !exportOpen) return;
    const onDown = (e: PointerEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false);
      if (!exportRef.current?.contains(e.target as Node)) setExportOpen(false);
    };
    window.addEventListener('pointerdown', onDown);
    return () => window.removeEventListener('pointerdown', onDown);
  }, [menuOpen, exportOpen]);

  const doExport = async (format: 'markdown' | 'text' | 'json') => {
    setExportOpen(false);
    setExporting(true);
    try {
      const blob = await exportBoardBlob(boardId, format);
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      const ext = format === 'markdown' ? 'md' : format === 'json' ? 'json' : 'txt';
      a.download = `${boardTitle || 'board'}.${ext}`;
      a.click();
      URL.revokeObjectURL(a.href);
    } finally {
      setExporting(false);
    }
  };

  // Who else is here, as a sentence. Names rather than initials: "SA" in a
  // coloured circle is not an identification.
  const peers = Object.values(presence);
  const presenceSummary = peers.length === 0
    ? t('presence.alone')
    : `${t('presence.here')}: ${peers.map((p) => p.name || t('presence.guest')).join(', ')}`;

  // Only CHANGES are announced, and only after the first render — otherwise
  // opening a board with four people on it interrupts with a roll call.
  const [presenceNews, setPresenceNews] = useState('');
  const knownPeers = useRef<string | null>(null);
  useEffect(() => {
    const key = peers.map((p) => p.clientId).sort().join('|');
    if (knownPeers.current === null) { knownPeers.current = key; return; }
    if (knownPeers.current === key) return;
    knownPeers.current = key;
    setPresenceNews(presenceSummary);
  }, [peers, presenceSummary]);

  const toggle = (p: PanelKind) => setPanel(panel === p ? 'none' : p);
  const initials = (user?.displayName || '?').split(/\s+/).map((w) => w[0]).slice(0, 2).join('');

  return (
    // AX18. The chrome was <div>s all the way down: no <header>, no <nav>, and
    // the breadcrumb — the product's only statement of WHERE YOU ARE in a tree
    // people nest four deep — was a div of buttons with the current board as a
    // bare <span>. A screen-reader user could not jump to it, could not tell it
    // from the toolbar next to it, and was never told which crumb was current.
    <header className="topbar">
      <button className="topbar-btn icon-only" title={t('topbar.boards')} onClick={() => toggle('boards')} style={{ marginRight: 2 }}>
        <BoardIcon size={18} />
      </button>
      <div className="brand">
        <div className="brand-mark">Q</div>
        <div className="brand-name">Qomra<em>Note</em></div>
      </div>

      <nav className="breadcrumbs" aria-label={t('a11y.breadcrumb')}>
        <ol className="crumb-list">
          {user && !isHome && (
            <li className="crumb-item">
              <button className="crumb" data-crumb-board={user.homeBoardId} onClick={() => void navigate(user.homeBoardId)}>{t('app.home')}</button>
              {/* The chevron is punctuation between two names, and a screen
                  reader reading it aloud ("greater-than sign") is noise. */}
              <span className="crumb-sep" aria-hidden="true"><ChevronIcon size={13} /></span>
            </li>
          )}
          {breadcrumb.filter((b) => b.id !== user?.homeBoardId).map((b) => (
            <li className="crumb-item" key={b.id}>
              <button className="crumb" data-crumb-board={b.id} onClick={() => void navigate(b.id)}>{b.title || t('app.untitled')}</button>
              <span className="crumb-sep" aria-hidden="true"><ChevronIcon size={13} /></span>
            </li>
          ))}
          {/* aria-current="page" is the whole point: the last crumb is not a
              link and never was, and until now nothing said why. */}
          <li className="crumb-item">
            <span className="crumb current" aria-current="page">{isHome ? t('app.home') : boardTitle}</span>
          </li>
        </ol>
      </nav>

      {/* AX29. Presence was simultaneously unannounced where it matters and
          over-present where it does not: the canvas carried a moving name per
          peer (now aria-hidden), while this stack — the only place presence is
          a stable fact — was five unnamed coloured circles with a `title`.
          A summary with a count, plus a polite announcement when the set
          changes, so "somebody just joined the board you are working on" is
          something a person can hear once instead of never. */}
      <div className="presence-stack" aria-label={presenceSummary} role="img">
        {Object.values(presence).slice(0, 5).map((p) => (
          <div key={p.clientId} className="avatar" aria-hidden="true" title={p.name} style={{ background: presenceColor(p.sub || p.clientId) }}>
            {(p.name || '?').slice(0, 2)}
          </div>
        ))}
      </div>
      <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">{presenceNews}</div>

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
      <div className="avatar-menu-wrap" ref={exportRef}>
        <button className="topbar-btn icon-only" onClick={() => setExportOpen(!exportOpen)} disabled={exporting || isHome}
          title={isHome ? t('topbar.homeNoExport') : t('topbar.export')}>
          <ExportIcon size={17} />
        </button>
        {exportOpen && !isHome && (
          <div className="avatar-dropdown" style={{ width: 180 }}>
            <button className="ad-item" onClick={() => void doExport('markdown')}>Markdown (.md)</button>
            <button className="ad-item" onClick={() => void doExport('text')}>Plain text (.txt)</button>
            <button className="ad-item" onClick={() => void doExport('json')}>JSON (.json)</button>
          </div>
        )}
      </div>
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
          {user?.avatarUrl ? <img className="avatar-img" src={user.avatarUrl} alt="" /> : initials}
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
            <button className="ad-item" onClick={() => void logout()}>
              <span className="ad-icon"><LogoutIcon size={16} /></span> {t('topbar.logout')}
            </button>
          </div>
        )}
      </div>
    </header>
  );
}
