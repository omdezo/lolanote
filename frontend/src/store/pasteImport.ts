// JN7 — the import path nobody looked at.
//
// PL3 says attached FILES are rejected by `visionTypes`. PL10 says structured
// import must land as a plan. Both address channels a person has to discover.
// The channel a person actually uses is Ctrl+V, and `pasteText` split on blank
// lines and committed ONE card with `textPreview: text.slice(0, 500)` — no
// markdown parsing (a `# Heading` stayed literal), no list detection, no split
// offer, no threshold of any kind.
//
// Step one of journey 2 for a real filmmaker is a script breakdown in a Doc and
// a shot list in a spreadsheet. Select-all-paste produced a single sticky note
// whose body was invisible to search, to export and to the agent past character
// five hundred.
//
// So: above a threshold, paste stops being a card and becomes a request. The
// text goes up as an attachment, the composer opens pre-filled, and the import
// arrives as a staged plan with ghosts and one revert — PL10's shape, reached
// through the gesture people already use.
import { uploadFile } from '../api/client';
import { useBoard } from './boardStore';

/** Long enough that one card is obviously the wrong container. */
export const IMPORT_MIN_CHARS = 1200;
/** Or structured enough — five blank-line-separated blocks is a document. */
export const IMPORT_MIN_BLOCKS = 5;

export function blockCount(text: string): number {
  return text.split(/\n{2,}/).map((p) => p.trim()).filter(Boolean).length;
}

/** Whether this paste is an import rather than a note. */
export function looksLikeImport(text: string): boolean {
  return text.length >= IMPORT_MIN_CHARS || blockCount(text) >= IMPORT_MIN_BLOCKS;
}

/** What the composer shows while a pasted document waits to be sent. */
export interface PendingImport {
  attachmentId: string;
  name: string;
  /** The first line or so, so the chip says which paste this is. */
  preview: string;
  chars: number;
}

function nameFor(text: string): string {
  const first = text.trim().split('\n', 1)[0].replace(/^#+\s*/, '').trim();
  const stem = (first.slice(0, 40) || 'pasted-text').replace(/[\\/:*?"<>|]/g, '-');
  return `${stem}.txt`;
}

/**
 * Offer a large paste to the agent, or decline and let the caller do what it
 * always did.
 *
 * THE HARD CLAUSE lives in this function's return value: every reason this
 * cannot work — the agent is off, the provider is down, the board is read-only,
 * a run already owns the bar, the upload failed — returns false, and `false`
 * means the ordinary paste happens exactly as before. The primary paste gesture
 * must not become provider-dependent; a person whose key rotated overnight
 * still gets their card.
 *
 * The upload happens HERE rather than at send time for the same reason: if the
 * text cannot be got onto the server, nothing has been consumed yet and the
 * caller's own path still has the string.
 */
export async function offerPasteToAgent(text: string): Promise<PendingImport | null> {
  if (!looksLikeImport(text)) return null;
  if (useBoard.getState().readOnly) return null;

  const { useAgent } = await import('../agent/agentStore');
  const agent = useAgent.getState();
  const caps = agent.capabilities;
  if (!caps?.enabled) return null;
  // Undefined means an older server that does not report health; only an
  // explicit false is a claim that the provider is down.
  if (caps.providerHealthy === false) return null;
  // A run in flight owns the bar, and hijacking it would lose the paste AND
  // interrupt the review.
  if (agent.run) return null;

  try {
    const name = nameFor(text);
    const file = new File([text], name, { type: 'text/plain' });
    const { attachmentId } = await uploadFile(file);
    const pending: PendingImport = {
      attachmentId,
      name,
      preview: text.trim().slice(0, 80),
      chars: text.length,
    };
    agent.setPendingImport(pending);
    agent.setOpen(true, 'board');
    return pending;
  } catch {
    // Network, quota, a rejected mime type — all of it means "paste normally".
    return null;
  }
}
