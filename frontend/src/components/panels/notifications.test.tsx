// JN11 — "what did I miss" is the first minute of the month-later journey, and
// every mechanism around the bell was built to lose the answer.
//
// Two facts are asserted because both were silently destructive:
//   1. Opening the panel used to mark EVERY notification read, before one had
//      been read. The boundary between seen and unseen was destroyed by the act
//      of looking.
//   2. `go()` navigated by boardId and dropped `elementId` — which the reminder
//      sweeper populates — so following a reminder landed you on a board of 200
//      elements with no indication which one it meant.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { NotificationsBell } from './NotificationsBell';
import { api } from '../../api/client';
import { useBoard } from '../../store/boardStore';
import { useView } from '../../store/viewStore';
import type { QNotification } from '../../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const notif = (over: Partial<QNotification>): QNotification => ({
  id: 'n1', kind: 'reminder', actorId: 'u2', message: 'A reminder fired',
  read: false, createdAt: new Date().toISOString(), ...over,
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

async function openBell(items: QNotification[]) {
  vi.spyOn(api, 'notifications').mockResolvedValue(items);
  await act(async () => { root.render(<NotificationsBell navigate={async () => {}} />); });
  await act(async () => { container.querySelector('button')!.click(); });
}

describe('looking does not destroy the signal', () => {
  it('marks nothing read merely by opening the panel', async () => {
    const mark = vi.spyOn(api, 'markNotificationsRead').mockResolvedValue(undefined as never);
    await openBell([notif({ id: 'a' }), notif({ id: 'b' })]);
    expect(mark).not.toHaveBeenCalled();
    // And the rows still say which ones are new.
    expect(container.querySelectorAll('.notif-item.unread').length).toBe(2);
  });

  it('marks exactly the row a person opened', async () => {
    const mark = vi.spyOn(api, 'markNotificationsRead').mockResolvedValue(undefined as never);
    await openBell([notif({ id: 'a' }), notif({ id: 'b' })]);
    await act(async () => { (container.querySelectorAll('.notif-item')[1] as HTMLButtonElement).click(); });
    expect(mark).toHaveBeenCalledWith(['b']);
  });

  it('still offers the sweeping version, as something you press on purpose', async () => {
    const mark = vi.spyOn(api, 'markNotificationsRead').mockResolvedValue(undefined as never);
    await openBell([notif({ id: 'a' }), notif({ id: 'b' })]);
    const all = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Mark all read');
    expect(all, 'an explicit mark-all button exists').toBeTruthy();
    await act(async () => { all!.click(); });
    expect(mark).toHaveBeenCalledWith(['a', 'b']);
  });

  it('carries the unread count in the button name, not only in a badge', async () => {
    await openBell([notif({ id: 'a' }), notif({ id: 'b' })]);
    const bell = container.querySelector('button')!;
    expect(bell.getAttribute('aria-label')).toContain('2');
  });
});

describe('the deep link keeps the element it was given', () => {
  it('selects the element the reminder was about', async () => {
    // The sweeper populated elementId; the bell threw it away.
    useBoard.setState({
      boardId: 'b9',
      elements: { t1: { id: 't1', type: 'TASK', content: {}, location: { parentId: 'b9', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 } } } as never,
      selection: new Set(),
    });
    vi.spyOn(api, 'markNotificationsRead').mockResolvedValue(undefined as never);

    await openBell([notif({ id: 'a', boardId: 'b9', elementId: 't1' })]);
    await act(async () => { (container.querySelector('.notif-item') as HTMLButtonElement).click(); });

    expect([...useBoard.getState().selection]).toEqual(['t1']);
    expect(useView.getState().focusedId).toBe('t1');
  });
});
