// Notifications bell (§6.2): polls unread notifications, shows a badge, and a
// dropdown that deep-links to the source board and marks read on open.
import { useEffect, useRef, useState } from 'react';
import { api } from '../../api/client';
import type { QNotification } from '../../api/types';
import { relativeTime } from '../../i18n';
import { useLocalization } from '../../store/settingsStore';
import { BellIcon, CloseIcon } from '../Icons';

const POLL_MS = 30_000;

export function NotificationsBell({ navigate }: { navigate: (boardId: string) => Promise<void> }) {
  const [items, setItems] = useState<QNotification[]>([]);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const localization = useLocalization();

  const load = () => api.notifications().then(setItems).catch(() => undefined);
  useEffect(() => {
    void load();
    const t = setInterval(load, POLL_MS);
    // Live: the server pushes notification.new over the socket — the badge
    // updates instantly instead of waiting for the next poll.
    const onPush = () => void load();
    window.addEventListener('qomra:notification', onPush);
    return () => {
      clearInterval(t);
      window.removeEventListener('qomra:notification', onPush);
    };
  }, []);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    window.addEventListener('pointerdown', onDown, true);
    return () => window.removeEventListener('pointerdown', onDown, true);
  }, [open]);

  const unread = items.filter((n) => !n.read).length;

  const openDropdown = async () => {
    setOpen(true);
    // Mark everything read on open.
    const unreadIds = items.filter((n) => !n.read).map((n) => n.id);
    if (unreadIds.length) {
      await api.markNotificationsRead(unreadIds).catch(() => undefined);
      setItems((cur) => cur.map((n) => ({ ...n, read: true })));
    }
  };

  const go = async (n: QNotification) => {
    setOpen(false);
    if (n.boardId) await navigate(n.boardId).catch(() => undefined);
  };

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button className="topbar-btn icon-only" title="Notifications" onClick={() => (open ? setOpen(false) : void openDropdown())}>
        <BellIcon size={17} />
        {unread > 0 && <span className="notif-badge">{unread > 9 ? '9+' : unread}</span>}
      </button>
      {open && (
        <div className="notif-dropdown">
          <div className="notif-head">
            <span>Notifications</span>
            <button className="panel-close" onClick={() => setOpen(false)}><CloseIcon size={14} /></button>
          </div>
          <div className="notif-body">
            {items.length === 0 && <div className="panel-empty" style={{ paddingTop: 24 }}>You're all caught up.</div>}
            {items.map((n) => (
              <button key={n.id} className="notif-item" onClick={() => void go(n)}>
                <div className="notif-msg">{n.message}</div>
                <div className="notif-time">{relativeTime(n.createdAt, localization)}</div>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
