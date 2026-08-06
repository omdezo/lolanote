// Thin typed API client. Every call carries the Keycloak bearer token; the
// share token (for boards opened via share links) rides along when present.
import { forceRefreshToken, getToken, isAuthenticated } from '../auth/keycloak';
import type {
  AgentAdjustment, AgentAutonomy, AgentCapabilities, AgentEvent, AgentAuditEntry, AgentBoardState, AgentRun, AgentScope,
  BoardView, Label, LinkMetadata, Op, PresignResult, QComment, QElement,
  QNotification, ShareState, TrashItem, Txn, User, UserSettings,
} from './types';

// DeepPartial lets settings patches carry only the fields that changed.
export type DeepPartial<T> = { [K in keyof T]?: T[K] extends object ? DeepPartial<T[K]> : T[K] };

const BASE = '/api/v1';

let shareToken = '';
export function setShareToken(token: string) { shareToken = token; }
export function getShareToken(): string { return shareToken; }

let sharePassword = '';
export function setSharePassword(pw: string) { sharePassword = pw; }

async function request<T>(method: string, path: string, body?: unknown, extra?: Record<string, string>): Promise<T> {
  const doFetch = (token: string) => {
    const headers: Record<string, string> = { ...extra };
    if (token) headers.Authorization = `Bearer ${token}`;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (shareToken) headers['X-Share-Token'] = shareToken;
    if (sharePassword) headers['X-Share-Password'] = sharePassword;
    return fetch(BASE + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  };

  let res = await doFetch(await getToken());

  // A 401 on an authenticated session usually means the access token just
  // rolled over — force one refresh and retry once. A second 401 falls
  // through as a real error (the refresh path already handles dead sessions).
  if (res.status === 401 && isAuthenticated()) {
    const fresh = await forceRefreshToken();
    if (fresh) res = await doFetch(fresh);
  }

  if (!res.ok) {
    let message = `${res.status}`;
    try {
      const payload = await res.json();
      message = payload?.error?.message ?? message;
    } catch { /* non-JSON error body */ }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

// The server accepts 8-64 characters of [A-Za-z0-9_-]; a UUID's hex and hyphens
// are inside that set. Falls back to random characters where randomUUID is
// missing rather than sending nothing, because a request with no key silently
// loses the guarantee the header exists to provide.
function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return Array.from({ length: 32 }, () => Math.floor(Math.random() * 36).toString(36)).join('');
}

export const api = {
  me: () => request<User>('GET', '/me'),
  updateMe: (patch: { displayName?: string; email?: string; avatarUrl?: string }) =>
    request<User>('PATCH', '/me', patch),
  settings: () => request<UserSettings>('GET', '/me/settings'),
  updateSettings: (patch: DeepPartial<UserSettings>) =>
    request<UserSettings>('PATCH', '/me/settings', patch),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('POST', '/me/password', { currentPassword, newPassword }),
  deleteAccount: () => request<void>('DELETE', '/me', { confirm: 'DELETE' }),
  realtimeTicket: () => request<{ ticket: string }>('POST', '/realtime/ticket'),

  board: (id: string) => request<BoardView>('GET', `/boards/${id}`),
  boardChildren: (id: string) => request<QElement[]>('GET', `/boards/${id}/children`),
  boardUnsorted: (id: string) => request<QElement[]>('GET', `/boards/${id}/unsorted`),
  boardChildStats: (id: string) => request<Record<string, Record<string, number>>>('GET', `/boards/${id}/childstats`),
  myBoards: () => request<QElement[]>('GET', '/boards'),
  templates: () => request<QElement[]>('GET', '/templates'),
  useTemplate: (id: string, boardId: string, position: { x: number; y: number }) =>
    request<QElement>('POST', `/templates/${id}/use`, { boardId, position }),

  // The key is minted HERE, once per call, rather than at each call site.
  //
  // The server journals the row before applying, so a second POST carrying the
  // same key observes the first commit instead of repeating it. Without the
  // header a retried write — a network blip, a double-click that beats the
  // optimistic state — re-runs every op: creates fail mid-loop on duplicate
  // element ids, leaving a partial write with no journal row and no inverse.
  // The agent path was already safe because it pins its transaction id from the
  // run; this is the human half.
  //
  // Minting inside the client means the 401-refresh retry above reuses the same
  // key automatically, and no future caller can forget to supply one. A fresh
  // key per attempt would buy nothing at all.
  applyTransaction: (boardId: string, clientId: string, ops: Op[]) =>
    request<Txn>('POST', '/transactions', { boardId, clientId, ops },
      { 'Idempotency-Key': newIdempotencyKey() }),

  element: (id: string) => request<QElement>('GET', `/elements/${id}`),
  duplicate: (id: string) => request<QElement[]>('POST', `/elements/${id}/duplicate`),
  convertToClone: (id: string, targetParentId: string, position: { x: number; y: number }) =>
    request<QElement>('POST', `/elements/${id}/clone`, { targetParentId, position }),
  cloneInstances: (id: string) => request<Array<{ id: string; parentId: string; boardTitle?: string }>>('GET', `/elements/${id}/clones`),

  trash: () => request<TrashItem[]>('GET', '/trash'),
  restoreTrash: (id: string) => request<void>('POST', `/trash/${id}/restore`),
  deleteTrashItem: (id: string) => request<void>('DELETE', `/trash/${id}`),
  emptyTrash: () => request<{ deleted: number }>('DELETE', '/trash'),

  presign: (filename: string, contentType: string, fileSize: number) =>
    request<PresignResult>('POST', '/attachments/presign', { filename, contentType, fileSize }),
  completeUpload: (id: string) => request<unknown>('POST', `/attachments/${id}/complete`),

  resolveLink: (url: string) => request<LinkMetadata>('POST', '/links/resolve', { url }),

  shareState: (boardId: string) => request<ShareState>('GET', `/boards/${boardId}/share`),
  inviteEditor: (boardId: string, email: string) =>
    request<ShareState>('POST', `/boards/${boardId}/share/editors`, { email }),
  removeEditor: (boardId: string, sub: string) =>
    request<ShareState>('DELETE', `/boards/${boardId}/share/editors/${sub}`),
  createShareLink: (boardId: string, opts: { kind: 'edit' | 'view'; allowFeedback?: boolean; password?: string; welcomeMessage?: string }) =>
    request<ShareState>('POST', `/boards/${boardId}/share/link`, opts),
  revokeShareLink: (boardId: string, kind: 'edit' | 'view') =>
    request<ShareState>('DELETE', `/boards/${boardId}/share/link/${kind}`),
  resolveSharedLink: (token: string) =>
    request<{ boardId: string; title: string; kind: string; welcomeMessage: string }>('GET', `/shared/${token}`),

  search: (q: string) => request<QElement[]>('GET', `/search?q=${encodeURIComponent(q)}`),

  resolveUsers: (subs: string[]) =>
    request<Array<{ sub: string; name: string; avatarUrl?: string; email?: string }>>('POST', '/users/resolve', { subs }),
  moveElements: (ids: string[], targetBoardId: string) =>
    request<void>('POST', '/elements/move', { ids, targetBoardId }),

  comments: (threadId: string) => request<QComment[]>('GET', `/threads/${threadId}/comments`),
  addComment: (threadId: string, body: string, mentions: string[] = []) =>
    request<QComment>('POST', `/threads/${threadId}/comments`, { body, mentions }),
  editComment: (id: string, body: string) => request<QComment>('PATCH', `/comments/${id}`, { body }),
  react: (id: string, emoji: string) => request<QComment>('POST', `/comments/${id}/reactions`, { emoji }),

  labels: () => request<Label[]>('GET', '/labels'),
  createLabel: (name: string, color?: string) => request<Label>('POST', '/labels', { name, color }),
  attachLabel: (elementId: string, labelId: string) =>
    request<void>('POST', `/elements/${elementId}/labels`, { labelId }),
  detachLabel: (elementId: string, labelId: string) =>
    request<void>('DELETE', `/elements/${elementId}/labels/${labelId}`),

  // ---- AI agent ----
  // Note what is absent: no call sends ops. The client asks for a run and
  // sends typed adjustments; the server decides what lands on the board.
  agentCapabilities: () => request<AgentCapabilities>('GET', '/agent/capabilities'),
  agentCreateRun: (body: {
    boardId: string; intent: string; scope: AgentScope;
    selectionIds?: string[]; autonomy: AgentAutonomy; attachmentIds?: string[];
    /** Where the person is looking, in board coordinates, so new content
     *  lands there instead of at the far edge of a large board. */
    viewport?: { x: number; y: number; width: number; height: number };
    /** The label filter on screen right now. Travels for the same reason the
     *  viewport does: "tidy this up" means the twelve items they can see, not
     *  the two hundred behind the filter. */
    activeLabelIds?: string[];
    /**
     * Who composed the intent — the composer, or the ambient pill.
     *
     * The server labels Continue and Retry itself; only the client can tell a
     * paragraph somebody wrote from a canned sentence they accepted with one
     * click, and those two have nothing in common as far as apply rate,
     * adjustment rate or cost tolerance are concerned. Without it every rate in
     * every dashboard is computed over a mixed population.
     */
    source?: 'typed' | 'hint';
  }) =>
    request<AgentRun>('POST', '/agent/runs', body),
  agentRun: (id: string) => request<AgentRun>('GET', `/agent/runs/${id}`),
  // The verdict on a proposed rule, recorded on the RUN and apart from the board
  // write that saves the text. Save appended a sentence with no run link and
  // "Not now" set a local flag, so "do people want the rules the agent
  // proposes" — the direct measure of whether any of the memory work is worth
  // building — had no answer anywhere.
  agentAnswerRule: (id: string, accepted: boolean) =>
    request<AgentRun>('POST', `/agent/runs/${id}/rule`, { accepted }),
  agentRuns: (boardId: string, limit = 10) =>
    request<AgentRun[]>('GET', `/agent/runs?boardId=${boardId}&limit=${limit}`),
  // Cursor-based catch-up after a reconnect: live events ride the board socket.
  agentEvents: (id: string, since = 0) =>
    request<AgentEvent[]>('GET', `/agent/runs/${id}/events?since=${since}`),
  agentApply: (id: string, adjustments: AgentAdjustment[]) =>
    request<AgentRun>('POST', `/agent/runs/${id}/apply`, { adjustments }),
  agentBoardState: (boardId: string) =>
    request<AgentBoardState>('GET', `/agent/boards/${boardId}/state`),
  agentAudit: (boardId: string) => request<AgentAuditEntry[]>('GET', `/agent/boards/${boardId}/audit`),
  // The typed edits ride WITH the note. Dropping three rows and then typing
  // "also make it four columns" used to send only the sentence, so the three
  // drops vanished and the next plan proposed them again.
  agentRefine: (id: string, note: string, adjustments: AgentAdjustment[] = []) =>
    request<AgentRun>('POST', `/agent/runs/${id}/refine`, { note, adjustments }),
  // The adjustments travel even though nothing will be written. "I dropped
  // four rows, reparented two, and then gave up" names exactly which
  // capabilities failed, with typed targets; abandonment is the highest-weight
  // correction label there is, and it was being discarded in the same set()
  // call that made this request.
  agentDiscard: (id: string, adjustments: AgentAdjustment[] = []) =>
    request<AgentRun>('POST', `/agent/runs/${id}/discard`, { adjustments }),
  agentCancel: (id: string) => request<AgentRun>('POST', `/agent/runs/${id}/cancel`, {}),
  // No ids means the whole run. A subset undoes just those elements and leaves
  // the rest of the run standing.
  agentSteer: (id: string, note: string) =>
    request<AgentRun>('POST', `/agent/runs/${id}/steer`, { note }),
  agentRetry: (id: string) => request<AgentRun>('POST', `/agent/runs/${id}/retry`, {}),
  // Keep going from what a run left undone. No body: the new run's request is
  // composed on the server from that run's own unmet list.
  agentContinue: (id: string) => request<AgentRun>('POST', `/agent/runs/${id}/continue`, {}),
  agentRevert: (id: string, elementIds?: string[]) =>
    request<AgentRun>('POST', `/agent/runs/${id}/revert`, { elementIds: elementIds ?? [] }),

  notifications: () => request<QNotification[]>('GET', '/notifications'),
  markNotificationsRead: (ids: string[]) => request<void>('POST', '/notifications/read', { ids }),
};

// exportMyDataBlob downloads the caller's full data bundle (privacy tab).
export async function exportMyDataBlob(): Promise<Blob> {
  const res = await fetch(`${BASE}/me/export`, {
    headers: { Authorization: `Bearer ${await getToken()}` },
  });
  if (!res.ok) throw new ApiError(res.status, 'export failed');
  return res.blob();
}

// exportBoardBlob downloads an export with proper auth (used by the topbar menu).
export async function exportBoardBlob(id: string, format: 'markdown' | 'text' | 'json'): Promise<Blob> {
  const res = await fetch(`${BASE}/boards/${id}/export?format=${format}`, {
    headers: { Authorization: `Bearer ${await getToken()}` },
  });
  if (!res.ok) throw new ApiError(res.status, 'export failed');
  return res.blob();
}

// uploadFile runs the full presign → PUT → complete pipeline (§9.10).
export async function uploadFile(file: File): Promise<{ url: string; attachmentId: string }> {
  const presigned = await api.presign(file.name, file.type || 'application/octet-stream', file.size);
  const headers: Record<string, string> = { 'Content-Type': file.type || 'application/octet-stream' };
  // The local dev driver's endpoint lives on the API; R2 URLs are presigned.
  if (presigned.uploadUrl.startsWith('/') || presigned.uploadUrl.includes('/api/v1/blob/')) {
    headers.Authorization = `Bearer ${await getToken()}`;
  }
  const put = await fetch(presigned.uploadUrl, { method: 'PUT', headers, body: file });
  if (!put.ok) throw new ApiError(put.status, 'upload failed');
  await api.completeUpload(presigned.attachmentId);
  // The element stores an indirection, never the presigned URL.
  //
  // presigned.publicUrl is a bearer credential with a seven-day life: it was
  // written straight into content.url, so anybody who could read the element —
  // or an export, or a copied card — held a key to the bytes that outlived
  // every permission change, and on day eight the picture simply vanished from
  // the board. This route signs a fresh short-lived URL per request behind an
  // ACL check, so revocation is immediate and nothing expires.
  return {
    url: `${BASE}/attachments/${presigned.attachmentId}/blob`,
    attachmentId: presigned.attachmentId,
  };
}
