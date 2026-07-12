// To-do list (§4.11): TASK children with checkbox completion, Tab-key
// indentation for subtasks, and inline add. Every toggle/edit is one
// transaction, so undo works per interaction.
import { useMemo, useState } from 'react';
import type { QElement } from '../../api/types';
import { createOp, deleteOp, updateOp, useBoard } from '../../store/boardStore';
import { CheckIcon, CloseIcon } from '../Icons';

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

  return (
    <div className="task-list">
      <input
        className="tl-title"
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
        <TaskRow key={task.id} task={task} />
      ))}
      <input
        className="task-add"
        placeholder="+ Add a task"
        value={newTask}
        onChange={(e) => setNewTask(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') addTask(); }}
        onBlur={addTask}
      />
    </div>
  );
}

function TaskRow({ task }: { task: QElement }) {
  const commitTransaction = useBoard((s) => s.commitTransaction);
  const [text, setText] = useState<string | null>(null);
  const done = !!task.content?.done;
  const indent = (task.content?.indent as number) ?? 0;

  return (
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
        title="Delete task"
        className="task-del"
        onPointerDown={(e) => e.stopPropagation()}
        onClick={() => void commitTransaction([deleteOp(task)])}
      >
        <CloseIcon size={12} />
      </button>
    </div>
  );
}
