// To-do list (§4.11): TASK children with checkbox completion, Tab-key
// indentation for subtasks, inline add, due dates, and reminders. Every
// toggle/edit is one transaction, so undo works per interaction. Reminder
// delivery is the backend sweep: reminderAt (RFC3339) → notification.
import { useMemo, useState } from 'react';
import type { QElement } from '../../api/types';
import { formatDate } from '../../i18n';
import { dirAttr, elementDir } from '../../lib/direction';
import { createOp, deleteOp, updateOp, useBoard } from '../../store/boardStore';
import { useLocalization } from '../../store/settingsStore';
import { CalendarIcon, CheckIcon, ClockIcon, CloseIcon } from '../Icons';

export function TaskListView({ element }: { element: QElement }) {
  const { elements, commitTransaction } = useBoard();
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
        value={title ?? element.content?.title ?? ''}
        placeholder="To-do list"
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
        placeholder="+ Add a task"
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
  const [text, setText] = useState<string | null>(null);
  const [datePop, setDatePop] = useState<{ x: number; y: number } | null>(null);
  const localization = useLocalization();
  const done = !!task.content?.done;
  const indent = (task.content?.indent as number) ?? 0;
  const dueDate = (task.content?.dueDate as string) || '';
  const reminderAt = (task.content?.reminderAt as string) || '';

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
      <div className={`task-row${done ? ' done' : ''}`} style={{ paddingLeft: indent * 22 }}>
        <button
          className={`task-check${done ? ' done' : ''}`}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => void commitTransaction([updateOp(task, { content: { done: !done } })])}
        >
          <CheckIcon size={11} />
        </button>
        <input
          className="task-text"
          dir={dir}
          value={text ?? task.content?.text ?? ''}
          onChange={(e) => setText(e.target.value)}
          onBlur={() => {
            const t = text?.trim();
            if (text !== null && t !== task.content?.text) {
              if (t === '') void commitTransaction([deleteOp(task)]);
              else void commitTransaction([updateOp(task, { content: { text: t } })]);
            }
            setText(null);
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
            if (e.key === 'Tab') {
              // Tab indents, Shift+Tab outdents (§4.11).
              e.preventDefault();
              const next = Math.max(0, Math.min(4, indent + (e.shiftKey ? -1 : 1)));
              if (next !== indent) void commitTransaction([updateOp(task, { content: { indent: next } })]);
            }
          }}
        />
        <button
          title="Due date & reminder"
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
          title="Delete task"
          className="task-del"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => void commitTransaction([deleteOp(task)])}
        >
          <CloseIcon size={12} />
        </button>
      </div>

      {(dueDate || reminderAt) && (
        <div className="task-meta" style={{ marginLeft: 27 + indent * 22 }}>
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
        <label>DUE DATE</label>
        <input type="date" value={due} onChange={(e) => setDue(e.target.value)} />
        <label>REMINDER</label>
        <input type="datetime-local" value={remind} onChange={(e) => setRemind(e.target.value)} />
        {(due || remind) && (
          <button className="tdp-clear" onClick={() => { setDue(''); setRemind(''); }}>Clear</button>
        )}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button className="btn-quiet" onClick={onClose}>Cancel</button>
          <button className="btn-primary" onClick={save}>Save</button>
        </div>
      </div>
    </>
  );
}
