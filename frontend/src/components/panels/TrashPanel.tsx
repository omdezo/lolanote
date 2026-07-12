// Trash (§3.4): per-account, split "deleted by me / by others", restore per
// item, permanent single delete, and irreversible Empty Trash. Items expire
// after 3 months server-side.
import { useEffect, useRef, useState } from 'react';
import gsap from 'gsap';
import { api } from '../../api/client';
import type { TrashItem } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { CloseIcon, RestoreIcon, TrashIcon } from '../Icons';
import { confirm } from '../ui/Prompt';

interface Props { onClose: () => void; navigate: (boardId: string) => Promise<void> }

export function TrashPanel({ onClose }: Props) {
  const [items, setItems] = useState<TrashItem[]>([]);
  const [busy, setBusy] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  const refreshBoard = useBoard((s) => s.refreshBoard);

  const load = () => api.trash().then(setItems).catch(() => setItems([]));
  useEffect(() => {
    void load();
    if (panelRef.current) gsap.fromTo(panelRef.current, { x: 340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
  }, []);

  const restore = async (id: string) => {
    await api.restoreTrash(id);
    await Promise.all([load(), refreshBoard()]);
  };

  const purgeOne = async (id: string) => {
    await api.deleteTrashItem(id);
    await load();
  };

  const empty = async () => {
    if (!(await confirm('Empty trash? This permanently deletes everything in it and cannot be undone.', 'Empty trash'))) return;
    setBusy(true);
    try {
      await api.emptyTrash();
      await load();
    } finally {
      setBusy(false);
    }
  };

  const mine = items.filter((i) => i.deletedByMe);
  const others = items.filter((i) => !i.deletedByMe);

  const row = (item: TrashItem) => (
    <div key={item.element.id} className="panel-item">
      <div>
        {item.element.content?.title || item.element.content?.textPreview || item.element.content?.filename || `(${item.element.type.toLowerCase()})`}
      </div>
      <div className="pi-meta">
        {item.element.type} · deleted {item.element.deletedAt ? new Date(item.element.deletedAt).toLocaleDateString() : ''}
      </div>
      <div className="pi-actions">
        <button className="pi-btn" onClick={() => void restore(item.element.id)}><RestoreIcon size={13} /> Restore</button>
        <button className="pi-btn danger" onClick={() => void purgeOne(item.element.id)}>Delete forever</button>
      </div>
    </div>
  );

  return (
    <div ref={panelRef} className="side-panel">
      <div className="panel-head">
        <h3><TrashIcon size={17} /> Trash</h3>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <button className="pi-btn danger" onClick={() => void empty()} disabled={busy || items.length === 0}>
            Empty trash
          </button>
          <button className="panel-close" onClick={onClose} title="Close"><CloseIcon size={15} /></button>
        </div>
      </div>
      <div className="panel-body">
        {items.length === 0 && (
          <div className="panel-empty">
            <TrashIcon size={40} style={{ opacity: 0.35 }} />
            Trash is empty.<br />Deleted items are kept for 3 months.
          </div>
        )}
        {mine.length > 0 && <div className="panel-section-label">DELETED BY ME</div>}
        {mine.map(row)}
        {others.length > 0 && <div className="panel-section-label">DELETED BY OTHERS</div>}
        {others.map(row)}
      </div>
    </div>
  );
}
