// JN14 · JN13 — the one screen where a person decides who has access to their
// film.
//
// JN14's failure was not subtle and had shipped: `f47ac10b-58cc-4372-…` in
// Consolas next to a red Remove button. With two collaborators, revoking the
// wrong person's access to a live production was a coin flip. The resolver was
// already imported by comment bubbles and task assignees two files away.
//
// JN13's failure is the step before: you cannot invite anyone who is not
// already a user, and the product had nothing to say about it.
import './../../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { ShareDialog } from './ShareDialog';
import { api } from '../../api/client';
import { useUserNames } from '../../store/userNames';
import type { ShareState } from '../../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SUB = 'f47ac10b-58cc-4372-a567-0e02b2c3d479';

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useUserNames.setState({ users: {} });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

const state = (over: Partial<ShareState> = {}): ShareState => ({
  ownerId: 'owner', editors: [SUB], ...over,
});

async function render(shareState: ShareState) {
  vi.spyOn(api, 'shareState').mockResolvedValue(shareState);
  await act(async () => { root.render(<ShareDialog boardId="b1" onClose={() => {}} />); });
}

describe('the editors list names people (JN14)', () => {
  it('renders a resolved name, never the raw subject', async () => {
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([
      { sub: SUB, name: 'Sara Al-Balushi', email: 's@example.com' },
      { sub: 'owner', name: 'Ali', email: 'a@example.com' },
    ]);
    await render(state());
    await act(async () => { await Promise.resolve(); });

    const row = container.querySelector('.editor-row')!;
    expect(row.textContent).toContain('Sara Al-Balushi');
    expect(row.textContent).not.toContain(SUB);
  });

  it('names the Remove button after the person it removes', async () => {
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([{ sub: SUB, name: 'Sara Al-Balushi' }]);
    await render(state());
    await act(async () => { await Promise.resolve(); });

    const remove = container.querySelector('.editor-row .pi-btn.danger')!;
    // "Remove" alone, sixteen rows deep, is the same failure as an unnamed
    // switch: you can hear that a destructive control exists and not what it
    // destroys.
    expect(remove.getAttribute('aria-label')).toContain('Sara Al-Balushi');
  });
});

describe('a lookup miss is a next step, not a dead end (JN13)', () => {
  it('says what has to happen and offers something sendable', async () => {
    vi.spyOn(api, 'resolveUsers').mockResolvedValue([]);
    vi.spyOn(api, 'inviteEditor').mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }));
    await render(state({ editors: [], publicEditLink: 'tok' }));

    const input = container.querySelector('.share-row input') as HTMLInputElement;
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!;
    await act(async () => {
      setter.call(input, 'producer@example.com');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => { (container.querySelector('.share-row .pi-btn') as HTMLButtonElement).click(); });

    const block = container.querySelector('.share-noaccount');
    expect(block, 'the 404 renders a way forward').toBeTruthy();
    // The specific address, so the owner knows which attempt this is about.
    expect(block!.textContent).toContain('producer@example.com');
    // And the bearer-token caveat is stated, not implied: an editor link is
    // not an invitation and must never be presented as one.
    expect(container.querySelector('.share-caveat')!.textContent).toMatch(/not tied to one person/i);
  });
});
