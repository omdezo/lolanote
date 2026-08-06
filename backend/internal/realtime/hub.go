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
	// virtual holds participants with no socket, keyed by their synthetic sub.
	//
	// Every human editor in this product claims what it touches — sendEditing →
	// element.editing → "X is editing…" on the card — and the agent was the only
	// writer that did not. So while a run rewrote a note, a colleague could be
	// typing in that same note with no badge, no cursor and no warning, and the
	// run's transaction landed as a merge-patch over their in-flight edit.
	//
	// A run executes in a server goroutine with no connection, which is why
	// presence could not express it: PresenceUser was derived from a *Client and
	// *Client was the only producer.
	virtual map[string]PresenceUser
}

// NewHub constructs the hub.
func NewHub(log *zap.Logger) *Hub {
	return &Hub{rooms: map[string]*room{}, log: log.Named("realtime")}
}

var (
	_ domain.TransactionBroadcaster = (*Hub)(nil)
	_ domain.EventBroadcaster       = (*Hub)(nil)
)

// BroadcastEvent pushes an ad-hoc event to everyone in a board room.
func (h *Hub) BroadcastEvent(boardID, event string, data any) {
	h.broadcast(boardID, mustEnvelope(event, data), nil)
}

// NotifyUser pushes an event to every live connection of one user, whatever
// board each connection is on (notification badges update everywhere).
func (h *Hub) NotifyUser(sub, event string, data any) {
	env := mustEnvelope(event, data)
	h.mu.RLock()
	targets := make([]*Client, 0, 4)
	for _, r := range h.rooms {
		for c := range r.clients {
			if c.Principal != nil && c.Principal.Sub == sub {
				targets = append(targets, c)
			}
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.Send(env)
	}
}

// Evict closes one person's connections to one board.
//
// Authorization for a socket is resolved exactly once, at handshake, and room
// membership is keyed on the connection and never re-checked — so a removed
// collaborator went on receiving the full text of every card edited on the
// board they had just been cut off from, for as long as they left the tab open.
// "Remove" in the sharing dialog means now.
func (h *Hub) Evict(boardID, sub string) {
	h.evict(boardID, "access removed", func(c *Client) bool {
		return c.Principal != nil && c.Principal.Sub == sub
	})
}

// EvictByShareToken closes the connections that joined on a link that has just
// been revoked. The token is the only handle on those sessions: an anonymous
// link holder has no sub to name.
func (h *Hub) EvictByShareToken(boardID, token string) {
	if token == "" {
		return
	}
	h.evict(boardID, "share link revoked", func(c *Client) bool {
		return c.Principal != nil && c.Principal.ShareToken == token
	})
}

// evict closes every client in a room matching the predicate. The targets are
// collected under the read lock and closed outside it, because close()
// re-enters the hub through Unregister.
func (h *Hub) evict(boardID, reason string, match func(*Client) bool) {
	h.mu.RLock()
	r, ok := h.rooms[boardID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	targets := make([]*Client, 0, 4)
	for c := range r.clients {
		if match(c) {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.evict(reason)
	}
	if len(targets) > 0 {
		h.log.Info("evicted sessions",
			zap.String("board", boardID), zap.String("reason", reason), zap.Int("count", len(targets)))
	}
}

// Register adds a client to its board room and announces it.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	r, ok := h.rooms[c.BoardID]
	if !ok {
		r = &room{clients: map[*Client]struct{}{}, virtual: map[string]PresenceUser{}}
		h.rooms[c.BoardID] = r
	}
	r.clients[c] = struct{}{}
	h.mu.Unlock()

	h.log.Debug("client joined", zap.String("board", c.BoardID), zap.String("client", c.ID))
	c.Send(mustEnvelope("presence.state", h.presence(c.BoardID)))
	if !c.Invisible {
		h.broadcast(c.BoardID, mustEnvelope("presence.join", c.presenceUser()), c)
	}
}

// Unregister removes a client, tearing the room down when empty.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if r, ok := h.rooms[c.BoardID]; ok {
		delete(r.clients, c)
		if len(r.clients) == 0 && len(r.virtual) == 0 {
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

// presence snapshots everyone currently in a room (invisible clients are
// omitted — privacy → ShowPresence off).
func (h *Hub) presence(boardID string) []PresenceUser {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []PresenceUser{}
	if r, ok := h.rooms[boardID]; ok {
		for c := range r.clients {
			if c.Invisible {
				continue
			}
			out = append(out, c.presenceUser())
		}
		for _, v := range r.virtual {
			out = append(out, v)
		}
	}
	return out
}

// RegisterVirtual publishes a participant that has no socket.
//
// Room-safe by construction: a name and the element currently being worked on,
// and nothing else. No intent, no plan, no request text — so it survives the
// journal redaction that keeps a colleague's prompt off the room channel, and
// it is the honest version of "show me what the assistant is doing", built from
// what the room may already see rather than from the owner's private stream.
//
// Idempotent, so a run can call it once per step to move the badge onto the
// card it is rewriting.
func (h *Hub) RegisterVirtual(boardID string, p PresenceUser) {
	if boardID == "" || p.Sub == "" {
		return
	}
	h.mu.Lock()
	r, ok := h.rooms[boardID]
	if !ok {
		r = &room{clients: map[*Client]struct{}{}, virtual: map[string]PresenceUser{}}
		h.rooms[boardID] = r
	}
	if r.virtual == nil {
		r.virtual = map[string]PresenceUser{}
	}
	_, existed := r.virtual[p.Sub]
	if p.ClientID == "" {
		p.ClientID = p.Sub
	}
	r.virtual[p.Sub] = p
	h.mu.Unlock()

	event := "presence.update"
	if !existed {
		event = "presence.join"
	}
	h.broadcast(boardID, mustEnvelope(event, p), nil)
}

// UnregisterVirtual removes it. A stuck synthetic participant is worse than
// none — it claims a card nobody is working on and it cannot be closed by
// reloading — so every exit path of a run calls this, including the crash
// reconciler.
func (h *Hub) UnregisterVirtual(boardID, sub string) {
	if boardID == "" || sub == "" {
		return
	}
	h.mu.Lock()
	r, ok := h.rooms[boardID]
	if !ok {
		h.mu.Unlock()
		return
	}
	if _, live := r.virtual[sub]; !live {
		h.mu.Unlock()
		return
	}
	delete(r.virtual, sub)
	if len(r.clients) == 0 && len(r.virtual) == 0 {
		delete(h.rooms, boardID)
	}
	h.mu.Unlock()
	h.broadcast(boardID, mustEnvelope("presence.leave", map[string]string{"clientId": sub}), nil)
}

// Watching reports whether this person has a live connection to a board.
//
// The notification etiquette needs it: somebody sitting on the board watching
// the change happen does not also need a bell about it, and "is anyone looking"
// is a fact only the hub holds.
func (h *Hub) Watching(boardID, sub string) bool {
	if boardID == "" || sub == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	r, ok := h.rooms[boardID]
	if !ok {
		return false
	}
	for c := range r.clients {
		if c.Principal != nil && c.Principal.Sub == sub {
			return true
		}
	}
	return false
}

func mustEnvelope(event string, data any) []byte {
	raw, _ := json.Marshal(data)
	env, _ := json.Marshal(Envelope{Event: event, Data: raw})
	return env
}
