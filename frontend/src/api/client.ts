// Thin typed API client. Every call carries the Keycloak bearer token; the
// share token (for boards opened via share links) rides along when present.
import { getToken } from '../auth/keycloak';
import type {
  BoardView, Label, LinkMetadata, Op, PresignResult, QComment, QElement,
  QNotification, ShareState, TrashItem, Txn, User,
} from './types';

const BASE = '/api/v1';

let shareToken = '';
export function setShareToken(token: string) { shareToken = token; }
export function getShareToken(): string { return shareToken; }

let sharePassword = '';
export function setSharePassword(pw: string) { sharePassword = pw; }

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${await getToken()}`,
  };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (shareToken) headers['X-Share-Token'] = shareToken;
  if (sharePassword) headers['X-Share-Password'] = sharePassword;

  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
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

export const api = {
  me: () => request<User>('GET', '/me'),
  realtimeTicket: () => request<{ ticket: string }>('POST', '/realtime/ticket'),

  board: (id: string) => request<BoardView>('GET', `/boards/${id}`),
  boardChildren: (id: string) => request<QElement[]>('GET', `/boards/${id}/children`),
  boardUnsorted: (id: string) => request<QElement[]>('GET', `/boards/${id}/unsorted`),
  boardChildStats: (id: string) => request<Record<string, Record<string, number>>>('GET', `/boards/${id}/childstats`),
  myBoards: () => request<QElement[]>('GET', '/boards'),
  templates: () => request<QElement[]>('GET', '/templates'),
  useTemplate: (id: string, boardId: string, position: { x: number; y: number }) =>
    request<QElement>('POST', `/templates/${id}/use`, { boardId, position }),

  applyTransaction: (boardId: string, clientId: string, ops: Op[]) =>
    request<Txn>('POST', '/transactions', { boardId, clientId, ops }),

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

  comments: (threadId: string) => request<QComment[]>('GET', `/threads/${threadId}/comments`),
  addComment: (threadId: string, body: string) =>
    request<QComment>('POST', `/threads/${threadId}/comments`, { body }),
  editComment: (id: string, body: string) => request<QComment>('PATCH', `/comments/${id}`, { body }),
  react: (id: string, emoji: string) => request<QComment>('POST', `/comments/${id}/reactions`, { emoji }),

  labels: () => request<Label[]>('GET', '/labels'),
  createLabel: (name: string, color?: string) => request<Label>('POST', '/labels', { name, color }),
  attachLabel: (elementId: string, labelId: string) =>
    request<void>('POST', `/elements/${elementId}/labels`, { labelId }),
  detachLabel: (elementId: string, labelId: string) =>
    request<void>('DELETE', `/elements/${elementId}/labels/${labelId}`),

  notifications: () => request<QNotification[]>('GET', '/notifications'),
  markNotificationsRead: (ids: string[]) => request<void>('POST', '/notifications/read', { ids }),
};

// exportBoardBlob downloads an export with proper auth (used by the topbar menu).
export async function exportBoardBlob(id: string, format: 'markdown' | 'text'): Promise<Blob> {
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
  return { url: presigned.publicUrl, attachmentId: presigned.attachmentId };
}
