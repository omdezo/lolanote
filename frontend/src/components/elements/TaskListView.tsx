// To-do list (§4.11): TASK children with checkbox completion, Tab-key
// indentation for subtasks, inline add, due dates, and reminders. Every
// toggle/edit is one transaction, so undo works per interaction. Reminder
// delivery is the backend sweep: reminderAt (RFC3339) → notification.
import { useEffect, useId, useMemo, useState } from 'react';
import type { QElement } from '../../api/types';
import { currentSub } from '../../auth/keycloak';
import { formatDate, useT } from '../../i18n';
import { dirAttr, elementDir } from '../../lib/direction';
import { createOp, deleteOp, updateOp, useBoard } from '../../store/boardStore';
import { useLocalization } from '../../store/settingsStore';
import { useUserNames } from '../../store/userNames';
import { CalendarIcon, CheckIcon, ClockIcon, CloseIcon, UserPlusIcon } from '../Icons';

export function TaskListView({ element }: { element: QElement }) {
  const elements = useBoard((s) => s.elements);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const t = useT();
  const [title, setTitle] = useState<string | null>(null);
  const [newTask, setNewTask] = useState('');

  const tasks = useMemo(
    () =>
      Object.values(elements)
        .filter((el) => el.type === 'TASK' && el.location.parentId === element.id && !el.deletedAt)
        .sort((a, b) => a.location.index - b.location.index),
    [elements, element.id],
  );

  const addTask = () => {
    const text = newTask.trim();
    if (!text) return;
    const op = createOp('TASK', element.id, {
      index: (tasks[tasks.length - 1]?.location.index ?? 0) + 1,
      content: { text, done: false, indent: 0 },
    });
    void commitTransaction([op]);
    setNewTask('');
  };

  // The list's direction override applies to every row; 'auto' lets each
  // field follow its own first strong letter (Arabic → RTL) as you type.
  const dir = dirAttr(elementDir(element));

  return (
    <div className="task-list" dir={dir === 'auto' ? undefined : dir}>
      <input
        className="tl-title"
        dir={dir}
        aria-label={t('task.list')}
        value={title ?? element.content?.title ?? ''}
        placeholder={t('task.list')}
        onChange={(e) => setTitle(e.target.value)}
        onBlur={() => {
          if (title !== null && title !== element.content?.title) {
            void commitTransaction([updateOp(element, { content: { title } })]);
          }
          setTitle(null);
        }}
        onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()}
      />
      {tasks.map((task) => (
        <TaskRow key={task.id} task={task} dir={dir} />
      ))}
      <input
        className="task-add"
        dir={dir}
        aria-label={t('task.add')}
        placeholder={`+ ${t('task.add')}`}
        value={newTask}
        onChange={(e) => setNewTask(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') addTask(); }}
        onBlur={addTask}
      />
    </div>
  );
}

function TaskRow({ task, dir }: { task: QElement; dir: 'auto' | 'ltr' | 'rtl' }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const t = useT();
  // The checkbox announces the task it belongs to by pointing at the text
  // field's id — otherwise a to-do list reads as "button, edit, button, button".
  const textId = useId();
  const [text, setText] = useState<string | null>(null);
  const [datePop, setDatePop] = useState<{ x: number; y: number } | null>(null);
  const [assignPop, setAssignPop] = useState<{ x: number; y: number } | null>(null);
  const localization = useLocalization();
  const users = useUserNames((s) => s.users);
  const done = !!task.content?.done;
  const indent = (task.content?.indent as number) ?? 0;
  const dueDate = (task.content?.dueDate as string) || '';
  const reminderAt = (task.content?.reminderAt as string) || '';
  const assigneeId = (task.content?.assigneeId as string) || '';

  useEffect(() => {
    if (assigneeId) void useUserNames.getState().resolve([assigneeId]);
  }, [assigneeId]);
  const assigneeName = assigneeId ? (users[assigneeId]?.name ?? assigneeId.slice(0, 8)) : '';

  const dueClass = (() => {
    if (!dueDate || done) return '';
    const due = new Date(`${dueDate}T23:59:59`);
    const days = (due.getTime() - Date.now()) / 86_400_000;
    if (days < 0) return ' overdue';
    if (days < 2) return ' due-soon';
    return '';
  })();

  return (
    <>
      {/* aria-level carries the depth. It used to be conveyed only by
          paddingLeft, which is invisible to assistive technology and physically
          left-handed on a bilingual product. */}
      <div
        className={`task-row${done ? ' done' : ''}`}
        style={{ paddingInlineStart: indent * 22 }}
        aria-level={indent + 1}
      >
        <button
          className={`task-check${done ? ' done' : ''}`}
          role="checkbox"
          aria-checked={done}
          aria-labelledby={textId}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => void commitTransaction([updateOp(task, { content: { done: !done } })])}
        >
          <CheckIcon size={11} />
        </button>
        <input
          id={textId}
          className="task-text"
          dir={dir}
          aria-label={t('task.item')}
          title={t('task.indentHint')}
          value={text ?? task.content?.text ?? ''}
          onChange={(e) => setText(e.target.value)}
          onBlur={() => {
            const trimmed = text?.trim();
            if (text !== null && trimmed !== task.content?.text) {
              if (trimmed === '') void commitTransaction([deleteOp(task)]);
              else void commitTransaction([updateOp(task, { content: { text: trimmed } })]);
            }
            setText(null);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
            // WCAG 2.1.2, Level A. Tab used to indent and Shift+Tab outdent,
            // with preventDefault() running BEFORE the 0..4 clamp — so at
            // indent 0 Shift+Tab was swallowed and at indent 4 Tab was, and
            // focus could not leave the field by keyboard at all. The exits
            // were a mouse click elsewhere or closing the tab, on the element
            // type a production board is mostly made of.
            //
            // Indent/outdent moved to Alt+arrow, which no browser reserves, and
            // Tab is Tab again. The arrows are logical, not physical: in an RTL
            // list ArrowLeft still means "one level deeper" because it points
            // the way reading runs.
            if (e.altKey && (e.key === 'ArrowRight' || e.key === 'ArrowLeft')) {
              const rtl = (e.currentTarget.ownerDocument.dir === 'rtl')
                || getComputedStyle(e.currentTarget).direction === 'rtl';
              const deeper = rtl ? e.key === 'ArrowLeft' : e.key === 'ArrowRight';
              const next = Math.max(0, Math.min(4, indent + (deeper ? 1 : -1)));
              if (next === indent) return;
              e.preventDefault();
              void commitTransaction([updateOp(task, { content: { indent: next } })]);
            }
          }}
        />
        <button
          title={t('task.due')}
          aria-label={t('task.due')}
          className="task-date-btn"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
            setDatePop(datePop ? null : { x: Math.min(r.left, window.innerWidth - 260), y: r.bottom + 6 });
          }}
        >
          <CalendarIcon size={13} />
        </button>
        <button
          title={t('task.assign')}
          aria-label={t('task.assign')}
          className="task-date-btn"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => {
            const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
            setAssignPop(assignPop ? null : { x: Math.min(r.left, window.innerWidth - 240), y: r.bottom + 6 });
          }}
        >
          <UserPlusIcon size={13} />
        </button>
        <button
          title={t('task.delete')}
          aria-label={t('task.delete')}
          className="task-del"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => void commitTransaction([deleteOp(task)])}
        >
          <CloseIcon size={12} />
        </button>
      </div>

      {(dueDate || reminderAt || assigneeId) && (
        <div className="task-meta" style={{ marginInlineStart: 27 + indent * 22 }}>
          {dueDate && (
            <button className={`task-chip${dueClass}`} onPointerDown={(e) => e.stopPropagation()}
              onClick={(e) => {
                const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                setDatePop(datePop ? null : { x: Math.min(r.left, window.innerWidth - 260), y: r.bottom + 6 });
              }}>
              <CalendarIcon size={11} /> {formatDate(`${dueDate}T00:00:00`, localization)}
            </button>
          )}
          {reminderAt && (
            <span className="task-chip" title={new Date(reminderAt).toLocaleString()}>
              <ClockIcon size={11} />
            </span>
          )}
          {assigneeId && (
            <button className="task-chip assignee" title={`Assigned to ${assigneeName}`}
              onPointerDown={(e) => e.stopPropagation()}
              onClick={(e) => {
                const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
                setAssignPop(assignPop ? null : { x: Math.min(r.left, window.innerWidth - 240), y: r.bottom + 6 });
              }}>
              <span className="task-assignee-dot">{assigneeName.slice(0, 2)}</span> {assigneeName}
            </button>
          )}
        </div>
      )}

      {datePop && (
        <TaskDatePopover
          task={task}
          x={datePop.x}
          y={datePop.y}
          onClose={() => setDatePop(null)}
        />
      )}
      {assignPop && (
        <AssigneePopover
          task={task}
          x={assignPop.x}
          y={assignPop.y}
          onClose={() => setAssignPop(null)}
        />
      )}
    </>
  );
}

// AssigneePopover lists the board's collaborators (owner + editors) — the
// people who can actually see the task. Assigning writes content.assigneeId;
// the server notifies the assignee (§4.11).
function AssigneePopover({ task, x, y, onClose }: { task: QElement; x: number; y: number; onClose: () => void }) {
  const t = useT();
  const acl = useBoard((s) => s.elements[s.boardId]?.acl);
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const users = useUserNames((s) => s.users);
  const subs = useMemo(
    () => Array.from(new Set([acl?.ownerId, ...(acl?.editors ?? []), currentSub()].filter(Boolean))) as string[],
    [acl],
  );
  useEffect(() => { if (subs.length) void useUserNames.getState().resolve(subs); }, [subs]);

  const assign = (sub: string | null) => {
    void commitTransaction([updateOp(task, { content: { assigneeId: sub } })]);
    onClose();
  };

  return (
    <>
      <div style={{ position: 'fixed', inset: 0, zIndex: 219 }} onPointerDown={(e) => { e.stopPropagation(); onClose(); }} />
      <div className="task-date-pop" style={{ left: x, top: y }} onPointerDown={(e) => e.stopPropagation()}>
        <label>{t('task.assignTo')}</label>
        {subs.map((sub) => (
          <button
            key={sub}
            className={`assignee-row${task.content?.assigneeId === sub ? ' on' : ''}`}
            onClick={() => assign(sub)}
          >
            <span className="task-assignee-dot">{(users[sub]?.name ?? sub).slice(0, 2)}</span>
            {users[sub]?.name ?? sub.slice(0, 8)}{sub === currentSub() ? ' (me)' : ''}
          </button>
        ))}
        {task.content?.assigneeId && (
          <button className="tdp-clear" onClick={() => assign(null)}>{t('task.unassign')}</button>
        )}
      </div>
    </>
  );
}

// toLocalInput converts an RFC3339 timestamp to datetime-local input format.
function toLocalInput(iso: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function TaskDatePopover({ task, x, y, onClose }: { task: QElement; x: number; y: number; onClose: () => void }) {
  const t = useT();
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [due, setDue] = useState((task.content?.dueDate as string) || '');
  const [remind, setRemind] = useState(toLocalInput((task.content?.reminderAt as string) || ''));

  const save = () => {
    // A (re)set reminder clears reminderSent so the sweep fires it again.
    const reminderAt = remind ? new Date(remind).toISOString() : null;
    void commitTransaction([updateOp(task, {
      content: { dueDate: due || null, reminderAt, reminderSent: null },
    })]);
    onClose();
  };

  return (
    <>
      <div style={{ position: 'fixed', inset: 0, zIndex: 219 }} onPointerDown={(e) => { e.stopPropagation(); onClose(); }} />
      <div className="task-date-pop" style={{ left: x, top: y }} onPointerDown={(e) => e.stopPropagation()}>
        <label>{t('task.dueDate')}</label>
        <input type="date" value={due} onChange={(e) => setDue(e.target.value)} />
        <label>{t('task.reminder')}</label>
        <input type="datetime-local" value={remind} onChange={(e) => setRemind(e.target.value)} />
        {(due || remind) && (
          <button className="tdp-clear" onClick={() => { setDue(''); setRemind(''); }}>{t('task.clear')}</button>
        )}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button className="btn-quiet" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn-primary" onClick={save}>{t('task.save')}</button>
        </div>
      </div>
    </>
  );
}
