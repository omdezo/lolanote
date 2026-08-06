// Trash (§3.4): per-account, split "deleted by me / by others", restore per
// item, permanent single delete, and irreversible Empty Trash.
//
// JN18. Trashing a container cascades: `transaction_service.go` soft-deletes
// the whole live subtree plus its dangling connectors under ONE `trashBatchId`,
// and every member's Restore restores the entire batch anyway. This panel
// rendered one row per MEMBER, so ending a 400-element production produced 400
// rows — and the server caps the list at 500, which meant one ordinary deletion
// pushed everything else out of the only surface in the product that can
// restore anything. The card someone deleted by accident last Tuesday was still
// in the database, still restorable by id, and no longer visible anywhere. The
// batch machinery was the well-built part; the panel threw it away.
//
// JN21/DL17. Every row showed the date the item was deleted — a date nobody
// needs — and the retention window was stated in exactly one place: inside the
// empty-state block, i.e. rendered only when there was nothing left to lose.
// Now every row states what it has left, the header states the window, and
// under a week the row turns amber. See lib/deadline.ts for the law.
import { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../../api/client';
import type { TrashItem } from '../../api/types';
import { useBoard } from '../../store/boardStore';
import { isRTL } from '../../store/settingsStore';
import { tweenFromTo } from '../../lib/motion';
import { deadlineLabel, purgeDeadline, remaining, FALLBACK_TRASH_RETENTION_DAYS } from '../../lib/deadline';
import { useT } from '../../i18n';
import { useUserNames, nameOf } from '../../store/userNames';
import { CloseIcon, RestoreIcon, TrashIcon } from '../Icons';
import { confirm } from '../ui/Prompt';

interface Props { onClose: () => void; navigate: (boardId: string) => Promise<void> }

/** One deletion, as a person performed it: a root and everything it took. */
interface Batch {
  /** Batch id, or the element id when the deletion was a single element. */
  key: string;
  /** The element a person would name if asked what they deleted. */
  root: TrashItem;
  members: TrashItem[];
  deletedByMe: boolean;
}

/** The title a person would use for this thing. */
export function trashTitle(item: TrashItem, untitled: string): string {
  const c = item.element.content ?? {};
  return c.title || c.textPreview || c.filename || c.name || untitled;
}

/**
 * Group the flat list the server returns back into the deletions it came from.
 *
 * The root is chosen as the member whose own parent is NOT also in the batch —
 * that is exactly the element the person clicked delete on, and it is the only
 * one whose title describes the deletion rather than one leaf of it. A batch
 * whose root cannot be identified (a partially-purged batch, say) falls back to
 * the oldest member so the rows still add up.
 */
export function groupBatches(items: TrashItem[]): Batch[] {
  const byBatch = new Map<string, TrashItem[]>();
  for (const item of items) {
    const key = item.element.trashBatchId || item.element.id;
    const list = byBatch.get(key);
    if (list) list.push(item); else byBatch.set(key, [item]);
  }
  const out: Batch[] = [];
  for (const [key, members] of byBatch) {
    const ids = new Set(members.map((m) => m.element.id));
    const root = members.find((m) => !ids.has(m.element.location?.parentId ?? '')) ?? members[0];
    out.push({ key, root, members, deletedByMe: root.deletedByMe });
  }
  // Most recent deletion first — the same order the flat list had, which is the
  // order regret arrives in.
  out.sort((a, b) => (b.root.element.deletedAt ?? '').localeCompare(a.root.element.deletedAt ?? ''));
  return out;
}

export function TrashPanel({ onClose }: Props) {
  const t = useT();
  const [items, setItems] = useState<TrashItem[]>([]);
  const [busy, setBusy] = useState(false);
  const [filter, setFilter] = useState('');
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const panelRef = useRef<HTMLDivElement>(null);
  const refreshBoard = useBoard((s) => s.refreshBoard);
  // Re-rendered when a name arrives, so "deleted by 4f7ac10b" becomes a person.
  useUserNames((s) => s.users);

  const load = () => api.trash().then(setItems).catch(() => setItems([]));
  useEffect(() => {
    void load();
    // Docked to the inline END, so under RTL it arrives from the left.
    if (panelRef.current) tweenFromTo(panelRef.current, { x: isRTL() ? -340 : 340 }, { x: 0, duration: 0.28, ease: 'power3.out' });
  }, []);

  // JN14's argument applied here: a raw Keycloak sub beside a Restore button is
  // not a person. The resolver is one import away and always was.
  useEffect(() => {
    const subs = [...new Set(items.map((i) => i.element.deletedBy).filter(Boolean) as string[])];
    if (subs.length) void useUserNames.getState().resolve(subs);
  }, [items]);

  const restore = async (id: string) => {
    await api.restoreTrash(id);
    await Promise.all([load(), refreshBoard()]);
  };

  /**
   * JN20. "Delete forever" had no confirmation at all — one click on the most
   * irreversible control in the product — and neither irreversible control said
   * what it actually costs.
   *
   * The cost is not only the item. `plan.go InvertOps` inverts a delete to a
   * RESTORE, and both revert paths begin with a fetch that fails on a
   * hard-deleted element: tidy your trash in month two and month one's
   * reorganisation stops being undoable. The apply loop does not roll back, so
   * a 40-op inverse can get 37 ops in and abort, leaving the board half
   * reverted with no journal row describing it.
   *
   * Refusing the impossible up front needs the server (it holds the index from
   * elements to still-revertible runs). Saying it out loud does not, and a
   * present informed choice beats a silent future failure.
   */
  const purgeOne = async (id: string) => {
    if (!(await confirm(t('trash.purgeConfirm'), t('trash.deleteForever')))) return;
    await api.deleteTrashItem(id);
    await load();
  };

  const empty = async () => {
    if (!(await confirm(`${t('trash.emptyConfirm')} ${t('trash.revertWarning')}`, t('trash.empty')))) return;
    setBusy(true);
    try {
      await api.emptyTrash();
      await load();
    } finally {
      setBusy(false);
    }
  };

  const untitled = t('app.untitled');
  const batches = useMemo(() => groupBatches(items), [items]);
  const needle = filter.trim().toLowerCase();
  // The last line of defence was the least searchable list in the app.
  const visible = needle
    ? batches.filter((b) => b.members.some((m) => trashTitle(m, untitled).toLowerCase().includes(needle)))
    : batches;

  const mine = visible.filter((b) => b.deletedByMe);
  const others = visible.filter((b) => !b.deletedByMe);

  const deadlineOf = (item: TrashItem) => remaining(purgeDeadline(item));

  const memberRow = (item: TrashItem) => (
    <div key={item.element.id} className="panel-item trash-member">
      <div className="pi-title">{trashTitle(item, untitled)}</div>
      <div className="pi-meta">{item.element.type}</div>
    </div>
  );

  const batchRow = (batch: Batch) => {
    const count = batch.members.length;
    const open = expanded.has(batch.key);
    const left = deadlineOf(batch.root);
    const by = batch.root.element.deletedBy;
    return (
      <div key={batch.key} className={`panel-item trash-batch${left ? ` d-${left.urgency}` : ''}`}>
        <div className="pi-title">{trashTitle(batch.root, untitled)}</div>
        <div className="pi-meta">
          {count > 1
            ? <>{batch.root.element.type} · {t('trash.items').replace('{n}', String(count))}</>
            : batch.root.element.type}
          {' · '}
          {t('trash.deletedOn')} {batch.root.element.deletedAt ? new Date(batch.root.element.deletedAt).toLocaleDateString() : ''}
          {by && <> · {t('trash.deletedBy')} {nameOf(by)}</>}
        </div>
        {/* The number that matters, said in the units a person thinks in.
            It is text as well as colour — the amber is the ambient half. */}
        {left && <div className={`pi-deadline d-${left.urgency}`}>{deadlineLabel(left, t)}</div>}
        {count > 1 && <div className="pi-note">{t('trash.batchNote')}</div>}
        <div className="pi-actions">
          <button className="pi-btn" onClick={() => void restore(batch.root.element.id)}>
            <RestoreIcon size={13} /> {t('trash.restore')}
          </button>
          <button className="pi-btn danger" onClick={() => void purgeOne(batch.root.element.id)}>{t('trash.deleteForever')}</button>
          {count > 1 && (
            <button
              className="pi-btn"
              aria-expanded={open}
              onClick={() => setExpanded((prev) => {
                const next = new Set(prev);
                if (next.has(batch.key)) next.delete(batch.key); else next.add(batch.key);
                return next;
              })}
            >
              {open ? t('trash.collapse') : t('trash.expand')}
            </button>
          )}
        </div>
        {open && <div className="trash-members">{batch.members.filter((m) => m !== batch.root).map(memberRow)}</div>}
      </div>
    );
  };

  return (
    <div ref={panelRef} className="side-panel">
      <div className="panel-head">
        <h3><TrashIcon size={17} /> {t('topbar.trash')}</h3>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <button className="pi-btn danger" onClick={() => void empty()} disabled={busy || items.length === 0}>
            {t('trash.empty')}
          </button>
          <button className="panel-close" onClick={onClose} aria-label={t('common.close')} title={t('common.close')}><CloseIcon size={15} /></button>
        </div>
      </div>
      {/* Out of the empty state, onto the header: the person who needs to know
          the window is the one looking at something they might want back. */}
      <div className="panel-subhead">
        {t('trash.keptDays').replace('{n}', String(FALLBACK_TRASH_RETENTION_DAYS))}
      </div>
      {items.length > 0 && (
        <div className="panel-filter">
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            aria-label={t('trash.filter')}
            placeholder={t('trash.filter')}
          />
        </div>
      )}
      <div className="panel-body">
        {items.length === 0 && (
          <div className="panel-empty">
            <TrashIcon size={40} style={{ opacity: 0.35 }} />
            {t('trash.none')}
          </div>
        )}
        {items.length > 0 && visible.length === 0 && <div className="panel-empty">{t('trash.noMatch')}</div>}
        {mine.length > 0 && <div className="panel-section-label">{t('trash.byMe')}</div>}
        {mine.map(batchRow)}
        {others.length > 0 && <div className="panel-section-label">{t('trash.byOthers')}</div>}
        {others.map(batchRow)}
      </div>
    </div>
  );
}
