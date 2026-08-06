// Notifications bell (§6.2): polls unread notifications, shows a badge, and a
// dropdown that deep-links to the source board.
//
// JN11. Every mechanism around this bell was designed to lose information.
// Opening the dropdown marked EVERY unread notification read, before the person
// had read one — so the boundary between seen and unseen was destroyed by the
// act of looking, which is the one gesture that must not destroy it. And `go()`
// navigated by `boardId` and silently dropped `elementId`, which the reminder
// sweeper had carefully populated: you arrived on a board of 200 elements with
// no indication which task the reminder was about.
//
// The month-later journey opens here. Marking read now follows the ROW — you
// read a thing, that thing is read — and the destructive version is a button
// somebody presses on purpose.
import { useEffect, useRef, useState } from 'react';
import { api } from '../../api/client';
import type { QNotification } from '../../api/types';
import { relativeTime, useT } from '../../i18n';
import { useLocalization } from '../../store/settingsStore';
import { navigateToElement } from '../../store/navigation';
import { BellIcon, CloseIcon } from '../Icons';

const POLL_MS = 30_000;

export function NotificationsBell({ navigate }: { navigate: (boardId: string) => Promise<void> }) {
  const t = useT();
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

  const markRead = async (ids: string[]) => {
    if (!ids.length) return;
    const set = new Set(ids);
    // Optimistic: the badge is the thing being answered, and a round trip
    // between the click and the count changing reads as the click not landing.
    setItems((cur) => cur.map((n) => (set.has(n.id) ? { ...n, read: true } : n)));
    await api.markNotificationsRead(ids).catch(() => undefined);
  };

  const markAll = () => void markRead(items.filter((n) => !n.read).map((n) => n.id));

  const go = async (n: QNotification) => {
    setOpen(false);
    void markRead([n.id]);
    if (!n.boardId) return;
    // The elementId travels the rest of the way now. `navigate` stays the
    // caller's board-only path for anything that has no element.
    if (n.elementId) await navigateToElement(n.boardId, n.elementId).catch(() => undefined);
    else await navigate(n.boardId).catch(() => undefined);
  };

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      {/* The count is in the name, not only in a badge: a `<span>` inside a
          button named by a static string announces as "Notifications", and the
          number — the entire reason to press it — was visual only. */}
      <button
        className="topbar-btn icon-only"
        title={t('notif.bell')}
        aria-label={unread > 0 ? `${t('notif.bell')} — ${unread} ${t('notif.unread')}` : t('notif.bell')}
        onClick={() => setOpen(!open)}
      >
        <BellIcon size={17} />
        {unread > 0 && <span className="notif-badge" aria-hidden="true">{unread > 9 ? '9+' : unread}</span>}
      </button>
      {open && (
        <div className="notif-dropdown">
          <div className="notif-head">
            <span>{t('notif.bell')}</span>
            {unread > 0 && (
              <button className="pi-btn" onClick={markAll}>{t('notif.markAllRead')}</button>
            )}
            <button className="panel-close" onClick={() => setOpen(false)} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={14} /></button>
          </div>
          <div className="notif-body">
            {items.length === 0 && <div className="panel-empty" style={{ paddingTop: 24 }}>{t('notif.caughtUp')}</div>}
            {items.map((n) => (
              <button key={n.id} className={`notif-item${n.read ? '' : ' unread'}`} onClick={() => void go(n)}>
                <div className="notif-msg">{n.message}</div>
                <div className="notif-time">
                  {relativeTime(n.createdAt, localization)}
                  {!n.read && <span className="sr-only"> — {t('notif.unread')}</span>}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
