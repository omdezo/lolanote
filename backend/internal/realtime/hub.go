// Package realtime is the sync engine: per-board rooms over WebSockets that
// broadcast element transactions and presence. Deliberately no CRDT/OT —
// concurrency is element-granular, exactly Milanote's trade-off (§9.9): two
// users on different cards merge trivially; the same card resolves
// server-authoritatively (last writer wins).
package realtime

import (
	"encoding/json"
	"sync"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// Envelope is the wire format both directions: {"event": ..., "data": ...}.
type Envelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// PresenceUser is what peers see about a connected client.
type PresenceUser struct {
	ClientID string  `json:"clientId"`
	Sub      string  `json:"sub"`
	Name     string  `json:"name"`
	Cursor   *Cursor `json:"cursor,omitempty"`
	Editing  string  `json:"editing,omitempty"` // element id being edited
}

// Cursor is a live pointer position on the board canvas.
type Cursor struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Hub owns every room. One instance per process.
type Hub struct {
	mu    sync.RWMutex
	rooms map[string]*room
	log   *zap.Logger
}

type room struct {
	clients map[*Client]struct{}
}

// NewHub constructs the hub.
func NewHub(log *zap.Logger) *Hub {
	return &Hub{rooms: map[string]*room{}, log: log.Named("realtime")}
}

var _ domain.TransactionBroadcaster = (*Hub)(nil)

// Register adds a client to its board room and announces it.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	r, ok := h.rooms[c.BoardID]
	if !ok {
		r = &room{clients: map[*Client]struct{}{}}
		h.rooms[c.BoardID] = r
	}
	r.clients[c] = struct{}{}
	h.mu.Unlock()

	h.log.Debug("client joined", zap.String("board", c.BoardID), zap.String("client", c.ID))
	c.Send(mustEnvelope("presence.state", h.presence(c.BoardID)))
	h.broadcast(c.BoardID, mustEnvelope("presence.join", c.presenceUser()), c)
}

// Unregister removes a client, tearing the room down when empty.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if r, ok := h.rooms[c.BoardID]; ok {
		delete(r.clients, c)
		if len(r.clients) == 0 {
			delete(h.rooms, c.BoardID)
		}
	}
	h.mu.Unlock()
	h.broadcast(c.BoardID, mustEnvelope("presence.leave", map[string]string{"clientId": c.ID}), nil)
}

// BroadcastTransaction pushes a committed transaction to every client in the
// board room except its originator — remote clients apply the same changes
// payloads their own dispatches would produce.
func (h *Hub) BroadcastTransaction(boardID string, t *domain.Transaction) {
	env := mustEnvelope("transaction.applied", t)
	h.mu.RLock()
	r, ok := h.rooms[boardID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(r.clients))
	for c := range r.clients {
		if c.ID != t.ClientID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.Send(env)
	}
}

// broadcast sends to everyone in a room except skip.
func (h *Hub) broadcast(boardID string, env []byte, skip *Client) {
	h.mu.RLock()
	r, ok := h.rooms[boardID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, len(r.clients))
	for c := range r.clients {
		if c != skip {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.Send(env)
	}
}

// presence snapshots everyone currently in a room.
func (h *Hub) presence(boardID string) []PresenceUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []PresenceUser{}
	if r, ok := h.rooms[boardID]; ok {
		for c := range r.clients {
			out = append(out, c.presenceUser())
		}
	}
	return out
}

func mustEnvelope(event string, data any) []byte {
	raw, _ := json.Marshal(data)
	env, _ := json.Marshal(Envelope{Event: event, Data: raw})
	return env
}
