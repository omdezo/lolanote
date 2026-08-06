// JN8 — the run ledger, and its honesty clause.
//
// The failure this protects against is not "a list was short". It is that the
// product's central trust promise silently stopped existing after six runs,
// and nothing anywhere said so: the list simply got shorter from the far end,
// so a person's model of the guarantee stayed wrong until Thursday, when they
// wanted Monday's reorganisation back.
//
// So the assertions are: a run older than the sixth is reachable and offers
// Revert; and a run whose revert has become impossible renders the REASON
// rather than a button that will 500.
import './../test/domStubs';

import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest';
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';

import { RunLedger, revertStateOf } from './RunLedger';
import { api } from '../api/client';
import { useBoard } from '../store/boardStore';
import type { AgentRun } from '../api/types';

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const run = (over: Partial<AgentRun> = {}): AgentRun => ({
  id: 'r', state: 'COMPLETED', rev: 1,
  task: { intent: 'Organize the board', rootBoardId: 'b1', scope: 'board', autonomy: 'preview' },
  usage: { inputTokens: 0, outputTokens: 0, cachedTokens: 0, costUsd: 0.02, calls: 1 },
  createdAt: new Date(Date.now() - 3 * 24 * 3600_000).toISOString(),
  updatedAt: '',
  plan: { summary: '', actions: [{ seq: 0, kind: 'create_column', elementId: 'c1', summary: '' }] },
  ...over,
} as unknown as AgentRun);

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  useBoard.setState({ boardId: 'b1' });
});

afterEach(() => {
  act(() => { root.unmount(); });
  container.remove();
  vi.restoreAllMocks();
});

describe('revert is offered on age, not on recency', () => {
  it('offers it on a completed run whatever its position in the list', () => {
    expect(revertStateOf(run({ id: 'old' })).kind).toBe('available');
  });

  it('does not offer it on a run that is already fully undone', () => {
    // Per-action revert can reach the same end state as a whole-run revert, and
    // `allReverted` short-circuits server-side — so the button would do nothing
    // and teach the person that Undo is unreliable.
    const r = run({ revertedElementIds: ['c1'] });
    expect(revertStateOf(r).kind).toBe('done');
    expect(revertStateOf(run({ state: 'REVERTED' })).kind).toBe('done');
  });

  it('does not offer it on a run that never wrote', () => {
    expect(revertStateOf(run({ state: 'DISCARDED' })).kind).toBe('none');
  });

  it('names the reason when revert has become impossible', () => {
    // JN20's reachable trigger: tidy the trash in month two, then try to undo
    // month one. The inverse contains a restore of an element that is gone.
    const r = run({ revertBlockedReason: '3 items were permanently deleted' });
    const state = revertStateOf(r);
    expect(state.kind).toBe('blocked');
    expect(state.kind === 'blocked' && state.reason).toContain('permanently deleted');
  });
});

describe('the ledger surface', () => {
  it('renders every run the server returns, not the first six', async () => {
    const many = Array.from({ length: 11 }, (_, i) => run({ id: `r${i}`, task: { intent: `Request ${i}`, rootBoardId: 'b1', scope: 'board', autonomy: 'preview' } } as never));
    vi.spyOn(api, 'agentRuns').mockResolvedValue(many);

    await act(async () => { root.render(<RunLedger onClose={() => {}} />); });

    expect(container.querySelectorAll('.ac-ledger-row').length).toBe(11);
    // The seventh — the first one the old composer could never show.
    expect(container.textContent).toContain('Request 6');
  });

  it('draws a reason, not a button, on a run that can no longer be undone', async () => {
    vi.spyOn(api, 'agentRuns').mockResolvedValue([
      run({ id: 'gone', revertBlockedReason: '3 items were permanently deleted' }),
    ]);

    await act(async () => { root.render(<RunLedger onClose={() => {}} />); });

    const row = container.querySelector('.ac-ledger-row')!;
    expect(row.querySelector('button')).toBe(null);
    expect(row.textContent).toContain('permanently deleted');
  });

  it('asks for more only when the page it got was full', async () => {
    vi.spyOn(api, 'agentRuns').mockResolvedValue([run({ id: 'only' })]);
    await act(async () => { root.render(<RunLedger onClose={() => {}} />); });
    const buttons = [...container.querySelectorAll('button')].map((b) => b.textContent);
    expect(buttons).not.toContain('Show older runs');
  });
});
