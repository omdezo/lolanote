// JN18 · JN21 · DL17 — the trash panel, which is the last line of defence and
// was the least legible list in the app.
//
// These assert RENDERED reality, not shapes: a 400-element deletion has to draw
// ONE row (the 500-row server cap is why — one project deletion used to evict
// every other recoverable thing from the only UI that can restore anything),
// and every row has to state what it has left rather than when it was deleted.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { TrashPanel, groupBatches } from './TrashPanel';
import { purgeDeadline, remaining, FALLBACK_TRASH_RETENTION_DAYS } from '../../lib/deadline';
import type { TrashItem } from '../../api/types';
import { api } from '../../api/client';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const DAY = 24 * 60 * 60 * 1000;

function item(over: Partial<TrashItem['element']> & { id: string }, deletedByMe = true): TrashItem {
  return {
    deletedByMe,
    element: {
      type: 'CARD',
      content: { title: over.id },
      location: { parentId: '', section: 'CANVAS', position: { x: 0, y: 0 }, index: 0, width: 0, height: 0 },
      createdBy: 'u1', createdAt: '', updatedAt: '',
      deletedAt: new Date(Date.now() - 10 * DAY).toISOString(),
      deletedBy: 'u1',
      ...over,
    } as TrashItem['element'],
  };
}

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

describe('the trash renders the deletion, not its 400 members (JN18)', () => {
  it('collapses one cascade into one batch with the container as its root', () => {
    // Deleting "Pre-Production" cascades to its whole live subtree under one
    // trashBatchId. The root is the member nothing else in the batch parents.
    const members: TrashItem[] = [
      item({ id: 'preprod', type: 'BOARD', trashBatchId: 'b1', content: { title: 'Ep 1 — Pre-Production' } }),
      ...Array.from({ length: 399 }, (_, i) =>
        item({
          id: `card${i}`,
          trashBatchId: 'b1',
          location: { parentId: 'preprod', section: 'CANVAS', position: { x: 0, y: 0 }, index: i, width: 0, height: 0 },
        } as never)),
    ];
    const batches = groupBatches(members);
    expect(batches.length).toBe(1);
    expect(batches[0].members.length).toBe(400);
    expect(batches[0].root.element.id).toBe('preprod');
  });

  it('keeps unbatched single deletions as their own rows', () => {
    const batches = groupBatches([item({ id: 'a' }), item({ id: 'b' })]);
    expect(batches.length).toBe(2);
  });

  it('draws one row per deletion in the panel, not one per element', async () => {
    const cascade: TrashItem[] = [
      item({ id: 'preprod', type: 'BOARD', trashBatchId: 'b1', content: { title: 'Ep 1 — Pre-Production' } }),
      ...Array.from({ length: 40 }, (_, i) =>
        item({
          id: `c${i}`,
          trashBatchId: 'b1',
          location: { parentId: 'preprod', section: 'CANVAS', position: { x: 0, y: 0 }, index: i, width: 0, height: 0 },
        } as never)),
      item({ id: 'lonely' }),
    ];
    vi.spyOn(api, 'trash').mockResolvedValue(cascade);
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([]);

    await act(async () => {
      root.render(<TrashPanel onClose={() => {}} navigate={async () => {}} />);
    });

    // 41 elements, two deletions.
    expect(container.querySelectorAll('.trash-batch').length).toBe(2);
    expect(container.textContent).toContain('Ep 1 — Pre-Production');
    // The count is stated, because "one row" must not mean "39 things hidden
    // with no sign they exist" — that is the elision the digest refuses to do.
    expect(container.textContent).toContain('41 items');
  });
});

describe('every row says what it has left (JN21/DL17)', () => {
  it('prefers the server purgeAt over any client arithmetic', () => {
    // DL17's hard clause. If the server has computed the deadline, the client
    // must not second-guess it — a config change to domain.TrashRetention has
    // to move this number, not silently disagree with it.
    const serverSaid = new Date(Date.now() + 3 * DAY).toISOString();
    const d = purgeDeadline({ purgeAt: serverSaid, element: { deletedAt: new Date().toISOString() } });
    expect(d!.toISOString()).toBe(serverSaid);
  });

  it('falls back to one named constant, not a scattered 90', () => {
    const deletedAt = new Date(Date.now() - 10 * DAY).toISOString();
    const d = purgeDeadline({ element: { deletedAt } })!;
    const days = Math.round((d.getTime() - new Date(deletedAt).getTime()) / DAY);
    expect(days).toBe(FALLBACK_TRASH_RETENTION_DAYS);
  });

  it('turns amber inside a week and stays calm outside it', () => {
    expect(remaining(new Date(Date.now() + 40 * DAY))!.urgency).toBe('calm');
    expect(remaining(new Date(Date.now() + 6 * DAY))!.urgency).toBe('soon');
    expect(remaining(new Date(Date.now() - DAY))!.key).toBe('deadline.expired');
  });

  it('renders the countdown as text on the row, not only as a colour', async () => {
    vi.spyOn(api, 'trash').mockResolvedValue([
      item({ id: 'x', deletedAt: new Date(Date.now() - 86 * DAY).toISOString() }),
    ]);
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([]);

    await act(async () => {
      root.render(<TrashPanel onClose={() => {}} navigate={async () => {}} />);
    });

    const deadline = container.querySelector('.pi-deadline');
    expect(deadline, 'a trashed item states its own deadline').toBeTruthy();
    expect(deadline!.textContent).toMatch(/days left/);
    // Amber is carried in a class AND in the words above; the class alone would
    // be the 1.4.1 failure this document spends AX17 on.
    expect(deadline!.className).toContain('d-soon');
  });

  it('states the retention window on the header, where the items are', async () => {
    vi.spyOn(api, 'trash').mockResolvedValue([item({ id: 'x' })]);
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([]);
    await act(async () => {
      root.render(<TrashPanel onClose={() => {}} navigate={async () => {}} />);
    });
    // It used to live inside the `items.length === 0` branch — visible only
    // when there was nothing to warn about.
    const subhead = container.querySelector('.panel-subhead');
    expect(subhead?.textContent).toContain(String(FALLBACK_TRASH_RETENTION_DAYS));
  });
});
