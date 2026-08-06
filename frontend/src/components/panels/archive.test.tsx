// JN17 — there was no archive. `grep -rn 'archive'` over the whole repository
// returned four hits and not one of them was a feature: the word appears inside
// a PROMPT EXAMPLE ("Archive the stale stuff"), in an eval fixture, and twice
// as a Lucide glyph name. So the agent's own system prompt offered as a worked
// example of a good request a thing no primitive in the product could satisfy.
//
// The assertion that matters is the one about competition for attention: an
// archived board must LEAVE the live list. A section that merely adds a label
// while the board still sits in MY BOARDS is the ad-hoc "Archive" board the
// product was supposed to stop people from inventing.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { BoardsDrawer } from './BoardsDrawer';
import { isArchived, setArchived } from '../../store/archive';
import { api } from '../../api/client';
import * as keycloak from '../../auth/keycloak';
import type { QElement } from '../../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SUB = 'me';

const board = (id: string, title: string, archived = false): QElement => ({
  id, type: 'BOARD',
  content: { title, ...(archived ? { archived: true } : {}) },
  location: { parentId: '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 },
  acl: { ownerId: SUB, editors: [] },
  createdBy: SUB, createdAt: '', updatedAt: '',
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  vi.spyOn(keycloak, 'currentSub').mockReturnValue(SUB);
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

describe('archived work stops competing with live work', () => {
  it('keeps archived boards out of MY BOARDS and behind a collapsed section', async () => {
    vi.spyOn(api, 'myBoards').mockResolvedValue([
      board('live', 'Ep 2 — Production'),
      board('done', 'Ep 1 — 2025', true),
    ]);

    await act(async () => { root.render(<BoardsDrawer onClose={() => {}} navigate={async () => {}} />); });

    const titles = [...container.querySelectorAll('.drawer-board-title')].map((n) => n.textContent);
    expect(titles).toEqual(['Ep 2 — Production']);

    const toggle = container.querySelector('.drawer-archived-toggle')!;
    expect(toggle.textContent).toContain('1');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');

    await act(async () => { (toggle as HTMLButtonElement).click(); });
    expect([...container.querySelectorAll('.drawer-board-title')].map((n) => n.textContent))
      .toContain('Ep 1 — 2025');
  });

  it('offers archiving as a focusable button named after its board', async () => {
    vi.spyOn(api, 'myBoards').mockResolvedValue([board('live', 'Ep 2 — Production')]);
    await act(async () => { root.render(<BoardsDrawer onClose={() => {}} navigate={async () => {}} />); });

    const btn = container.querySelector('.drawer-archive')!;
    expect(btn.tagName).toBe('BUTTON');
    // "Archive" alone in a grid of six tiles names nothing.
    expect(btn.getAttribute('aria-label')).toContain('Ep 2 — Production');
  });
});

describe('archiving is a status written like any other edit', () => {
  it('goes through the transaction path with an inverse, on its own board', async () => {
    const apply = vi.spyOn(api, 'applyTransaction').mockResolvedValue({} as never);
    await setArchived(board('b7', 'Ep 1'), true);

    // Stamped with the board being archived, not whatever happens to be open:
    // filing the row against the open board is JN9's bug through another door.
    expect(apply.mock.calls[0][0]).toBe('b7');
    const op = apply.mock.calls[0][2][0];
    expect(op).toMatchObject({
      elementId: 'b7',
      action: 'update',
      changes: { content: { archived: true } },
      undoChanges: { content: { archived: false } },
    });
  });

  it('reads back as a status, so unarchive is the same mechanism', () => {
    expect(isArchived(board('b', 't', true))).toBe(true);
    expect(isArchived(board('b', 't'))).toBe(false);
  });
});
