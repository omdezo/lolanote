// Boards launcher (§5, §6.2): "My boards" and "Shared with me" — the entry
// point an invited collaborator needs to actually find a board shared to them.
import { useEffect, useRef, useState } from 'react';
import { api } from '../../api/client';
import { currentSub } from '../../auth/keycloak';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { isRTL } from '../../store/settingsStore';
import { tweenFromTo } from '../../lib/motion';
import { useT } from '../../i18n';
import { BoardGlyph, CloseIcon, HomeIcon } from '../Icons';
import { isArchived, setArchived } from '../../store/archive';
import { toast } from '../ui/Toaster';

const tileGradients = [
  'linear-gradient(135deg,#6e6cf0,#4a48c4)', 'linear-gradient(135deg,#ff8a65,#e8590c)',
  'linear-gradient(135deg,#4dd0a6,#0ca678)', 'linear-gradient(135deg,#5fb0f5,#1c7ed6)',
  'linear-gradient(135deg,#f78fb3,#e64980)', 'linear-gradient(135deg,#9775fa,#7048e8)',
];
function tileFor(id: string) {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return tileGradients[h % tileGradients.length];
}

export function BoardsDrawer({ onClose, navigate }: { onClose: () => void; navigate: (id: string) => Promise<void> }) {
  const t = useT();
  const [boards, setBoards] = useState<QElement[]>([]);
  const user = useBoard((s) => s.user);
  const panelRef = useRef<HTMLDivElement>(null);
  const [showArchived, setShowArchived] = useState(false);
  const [busy, setBusy] = useState('');

  useEffect(() => {
    api.myBoards().then(setBoards).catch(() => setBoards([]));
    // Slide in from whichever edge the panel is actually docked to. A tween
    // takes a number, so this is the one place direction cannot be expressed
    // as a logical property.
    if (panelRef.current) tweenFromTo(panelRef.current, { x: isRTL() ? 340 : -340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
  }, []);

  const sub = currentSub();
  const owned = boards.filter((b) => b.acl?.ownerId === sub);
  // JN17. Archived work is out of the way, not out of reach: it leaves the live
  // list, keeps its own collapsed section, and comes back with one press. The
  // alternative the product used to offer for "we finished" was Delete, which
  // starts a 90-day destruction clock over the entire subtree.
  const mine = owned.filter((b) => !isArchived(b));
  const archived = owned.filter(isArchived);
  const shared = boards.filter((b) => b.acl?.ownerId !== sub && !isArchived(b));

  const toggleArchive = async (b: QElement) => {
    setBusy(b.id);
    try {
      await setArchived(b, !isArchived(b));
      setBoards(await api.myBoards());
    } catch {
      toast.error(t('board.archiveFailed'));
    } finally {
      setBusy('');
    }
  };

  // Owner-only, and a real sibling <button> rather than something that appears
  // on hover: an action reachable only by putting a pointer on a tile is an
  // action a keyboard cannot reach at all (AX12).
  const tile = (b: QElement, ownedByMe: boolean) => (
    <div key={b.id} className="drawer-board-wrap">
      <button className="drawer-board" onClick={() => { onClose(); void navigate(b.id); }}>
        <span className="drawer-tile" style={{ background: tileFor(b.id) }}><BoardGlyph size={20} /></span>
        <span className="drawer-board-title">{b.content?.title || t('common.untitled')}</span>
      </button>
      {ownedByMe && (
        <button
          className="drawer-archive"
          disabled={busy === b.id}
          aria-label={`${isArchived(b) ? t('board.unarchive') : t('board.archive')} — ${b.content?.title || t('common.untitled')}`}
          title={isArchived(b) ? t('board.unarchive') : t('board.archive')}
          onClick={() => void toggleArchive(b)}
        >
          {isArchived(b) ? t('board.unarchive') : t('board.archive')}
        </button>
      )}
    </div>
  );

  return (
    <div ref={panelRef} className="side-panel start">
      <div className="panel-head">
        <h3>{t('panel.boards')}</h3>
        <button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={15} /></button>
      </div>
      <div className="panel-body">
        {user && (
          <button className="drawer-home" onClick={() => { onClose(); void navigate(user.homeBoardId); }}>
            <HomeIcon size={17} /> {t('app.home')}
          </button>
        )}
        <div className="panel-section-label">{t('panel.myBoards')}</div>
        {mine.length === 0 && <div className="drawer-none">{t('panel.noBoards')}</div>}
        <div className="drawer-grid">{mine.map((b) => tile(b, true))}</div>
        {shared.length > 0 && (
          <>
            <div className="panel-section-label">{t('panel.sharedWithMe')}</div>
            <div className="drawer-grid">{shared.map((b) => tile(b, false))}</div>
          </>
        )}
        {archived.length > 0 && (
          <>
            {/* Collapsed by default: the point of archiving is that finished
                work stops competing with live work for attention. */}
            <button
              className="panel-section-label drawer-archived-toggle"
              aria-expanded={showArchived}
              onClick={() => setShowArchived(!showArchived)}
            >
              {t('panel.archived')} ({archived.length})
            </button>
            {showArchived && <div className="drawer-grid">{archived.map((b) => tile(b, true))}</div>}
          </>
        )}
      </div>
    </div>
  );
}
