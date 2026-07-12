// WebSocket sync client: one connection per open board, reconnect with
// backoff, cursor throttling (the SOCKET_THROTTLE analog for continuous
// drags, §9.9). Remote transactions run through the same applyOps reducer
// local mutations use.
import { api, getShareToken } from '../api/client';
import { clientId, useBoard } from '../store/boardStore';
import type { PresenceUser, Txn } from '../api/types';

let socket: WebSocket | null = null;
let currentBoard = '';
let reconnectDelay = 1000;
let closedByUs = false;
let hadDisconnect = false;

export async function connectBoard(boardId: string) {
  disconnect();
  currentBoard = boardId;
  closedByUs = false;
  hadDisconnect = false;
  await open();
}

export function disconnect() {
  closedByUs = true;
  socket?.close();
  socket = null;
}

async function open() {
  if (!currentBoard) return;
  // Exchange the bearer token for a single-use ticket so it never rides in
  // the WebSocket URL (which lands in proxy/access logs).
  let ticket: string;
  try {
    ticket = (await api.realtimeTicket()).ticket;
  } catch {
    // Retry shortly; the token may be mid-refresh.
    if (!closedByUs) setTimeout(() => { void open(); }, reconnectDelay);
    return;
  }
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  const share = getShareToken();
  const url = `${proto}://${window.location.host}/ws?board=${currentBoard}&clientId=${clientId}&ticket=${encodeURIComponent(ticket)}${share ? `&shareToken=${encodeURIComponent(share)}` : ''}`;
  const ws = new WebSocket(url);
  socket = ws;

  ws.onopen = () => {
    reconnectDelay = 1000;
    // After a reconnect (not the first connect), the board may have missed
    // transactions — refetch truth. This matches Milanote's wake-refresh (§9.6).
    if (hadDisconnect) {
      hadDisconnect = false;
      void useBoard.getState().refreshBoard();
    }
  };

  ws.onmessage = (event) => {
    let env: { event: string; data: any };
    try { env = JSON.parse(event.data); } catch { return; }
    const store = useBoard.getState();
    switch (env.event) {
      case 'transaction.applied': {
        const txn = env.data as Txn;
        if (txn.clientId !== clientId) store.applyOps(txn.ops);
        break;
      }
      case 'presence.state':
        store.setPresence((env.data ?? []) as PresenceUser[]);
        break;
      case 'presence.join':
        store.upsertPresence(env.data as PresenceUser);
        break;
      case 'presence.leave':
        store.removePresence(env.data.clientId);
        break;
      case 'presence.cursor': {
        const { clientId: peer, cursor } = env.data;
        const existing = store.presence[peer];
        if (existing) store.upsertPresence({ ...existing, cursor });
        break;
      }
      case 'element.editing': {
        const { elementId, on, sub } = env.data;
        const peer = Object.values(store.presence).find((p) => p.sub === sub);
        store.setRemoteEditing(elementId, peer?.name ?? 'Someone', on);
        break;
      }
    }
  };

  ws.onclose = () => {
    if (closedByUs) return;
    hadDisconnect = true;
    setTimeout(() => { void open(); }, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 15_000);
  };
}

function send(event: string, data: unknown) {
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ event, data }));
  }
}

// Cursor updates are throttled to ~20/s so drags do not flood the room.
let lastCursorSent = 0;
export function sendCursor(x: number, y: number) {
  const now = performance.now();
  if (now - lastCursorSent < 50) return;
  lastCursorSent = now;
  send('presence.cursor', { x, y });
}

export function sendEditing(elementId: string, on: boolean) {
  send('editing', { elementId, on });
}
