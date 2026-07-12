// Boards launcher (§5, §6.2): "My boards" and "Shared with me" — the entry
// point an invited collaborator needs to actually find a board shared to them.
import { useEffect, useRef, useState } from 'react';
import gsap from 'gsap';
import { api } from '../../api/client';
import { currentSub } from '../../auth/keycloak';
import type { QElement } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { BoardGlyph, CloseIcon, HomeIcon } from '../Icons';

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
  const [boards, setBoards] = useState<QElement[]>([]);
  const user = useBoard((s) => s.user);
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api.myBoards().then(setBoards).catch(() => setBoards([]));
    if (panelRef.current) gsap.fromTo(panelRef.current, { x: -340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
  }, []);

  const sub = currentSub();
  const mine = boards.filter((b) => b.acl?.ownerId === sub);
  const shared = boards.filter((b) => b.acl?.ownerId !== sub);

  const tile = (b: QElement) => (
    <button
      key={b.id}
      className="drawer-board"
      onClick={() => { onClose(); void navigate(b.id); }}
    >
      <span className="drawer-tile" style={{ background: tileFor(b.id) }}><BoardGlyph size={20} /></span>
      <span className="drawer-board-title">{b.content?.title || 'Untitled'}</span>
    </button>
  );

  return (
    <div ref={panelRef} className="side-panel" style={{ left: 0, right: 'auto', borderRight: '1px solid var(--hairline)', borderLeft: 'none', boxShadow: '12px 0 40px rgba(0,0,0,0.08)' }}>
      <div className="panel-head">
        <h3>Boards</h3>
        <button className="panel-close" onClick={onClose}><CloseIcon size={15} /></button>
      </div>
      <div className="panel-body">
        {user && (
          <button className="drawer-home" onClick={() => { onClose(); void navigate(user.homeBoardId); }}>
            <HomeIcon size={17} /> Home
          </button>
        )}
        <div className="panel-section-label">MY BOARDS</div>
        {mine.length === 0 && <div style={{ fontSize: 12.5, color: 'var(--ink-3)' }}>No boards yet.</div>}
        <div className="drawer-grid">{mine.map(tile)}</div>
        {shared.length > 0 && (
          <>
            <div className="panel-section-label">SHARED WITH ME</div>
            <div className="drawer-grid">{shared.map(tile)}</div>
          </>
        )}
      </div>
    </div>
  );
}
