// JN7 — the import path is Ctrl+V, and nobody had looked at it.
//
// The hard clause is the interesting assertion here: the paste-to-agent hook
// MUST degrade to today's behaviour when the agent is disabled or offline, or
// the product's primary paste gesture becomes provider-dependent — a rotated
// key overnight and a filmmaker's Ctrl+V stops working.
import './../test/domStubs';

import { describe, expect, it, beforeEach, vi, afterEach } from 'vitest';

import { blockCount, looksLikeImport, offerPasteToAgent, IMPORT_MIN_CHARS, IMPORT_MIN_BLOCKS } from './pasteImport';
import { useBoard } from './boardStore';
import { useAgent } from '../agent/agentStore';
import * as client from '../api/client';

const caps = (over: Record<string, unknown> = {}) => ({
  enabled: true, can: [], cannot: [], limits: { maxActions: 20, maxSteps: 8 }, ...over,
} as never);

beforeEach(() => {
  useBoard.setState({ readOnly: false, boardId: 'b1' });
  useAgent.setState({ capabilities: caps(), run: null, pendingImport: null, open: false });
});

afterEach(() => { vi.restoreAllMocks(); });

describe('what counts as an import rather than a note', () => {
  it('a long paste does', () => {
    expect(looksLikeImport('x'.repeat(IMPORT_MIN_CHARS))).toBe(true);
    expect(looksLikeImport('a short thought')).toBe(false);
  });

  it('and a structured one does even when it is short', () => {
    const doc = Array.from({ length: IMPORT_MIN_BLOCKS }, (_, i) => `Block ${i}`).join('\n\n');
    expect(blockCount(doc)).toBe(IMPORT_MIN_BLOCKS);
    expect(looksLikeImport(doc)).toBe(true);
  });
});

describe('the hard clause: paste never becomes provider-dependent', () => {
  const big = 'y'.repeat(IMPORT_MIN_CHARS + 1);

  it('declines when the agent is switched off', async () => {
    useAgent.setState({ capabilities: caps({ enabled: false }) });
    const upload = vi.spyOn(client, 'uploadFile');
    expect(await offerPasteToAgent(big)).toBe(null);
    expect(upload, 'nothing was even uploaded').not.toHaveBeenCalled();
  });

  it('declines when the provider is down', async () => {
    useAgent.setState({ capabilities: caps({ providerHealthy: false }) });
    expect(await offerPasteToAgent(big)).toBe(null);
  });

  it('declines on a read-only board', async () => {
    useBoard.setState({ readOnly: true });
    expect(await offerPasteToAgent(big)).toBe(null);
  });

  it('declines while a run already owns the bar', async () => {
    useAgent.setState({ run: { id: 'r1', state: 'PROPOSED' } as never });
    expect(await offerPasteToAgent(big)).toBe(null);
  });

  it('declines when the upload fails, so the ordinary card still happens', async () => {
    vi.spyOn(client, 'uploadFile').mockRejectedValue(new Error('offline'));
    expect(await offerPasteToAgent(big)).toBe(null);
    expect(useAgent.getState().pendingImport).toBe(null);
  });
});

describe('when it can work, the paste becomes a request', () => {
  it('uploads the text and opens the composer holding it', async () => {
    vi.spyOn(client, 'uploadFile').mockResolvedValue({ url: '/u', attachmentId: 'att1' });
    const text = `# Scene 12\n\n${'z'.repeat(IMPORT_MIN_CHARS)}`;

    const pending = await offerPasteToAgent(text);

    expect(pending?.attachmentId).toBe('att1');
    // Named after the thing pasted, so the chip in the composer says which
    // paste this is rather than "pasted-text.txt" four times.
    expect(pending?.name).toContain('Scene 12');
    expect(useAgent.getState().pendingImport?.attachmentId).toBe('att1');
    expect(useAgent.getState().open).toBe(true);
  });

  it('leaves a small paste entirely alone', async () => {
    const upload = vi.spyOn(client, 'uploadFile');
    expect(await offerPasteToAgent('one line of thought')).toBe(null);
    expect(upload).not.toHaveBeenCalled();
  });
});
