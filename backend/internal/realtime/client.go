package realtime

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"qomranote/backend/internal/domain"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 45 * time.Second
	maxMessageSize = 1 << 20 // element payloads can carry rich-text docs

	// A connection authorized once must not outlive its credential: the
	// socket closes at the verified token's expiry (clamped below) and the
	// client reconnects with a fresh ticket, re-running the access check.
	minAuthLifetime     = 30 * time.Second
	maxAuthLifetime     = 30 * time.Minute
	defaultAuthLifetime = 15 * time.Minute
)

// Client is one WebSocket connection scoped to one board room.
type Client struct {
	ID        string // client-generated uuid; matches transactions' clientId
	BoardID   string
	Principal *domain.Principal
	// Invisible clients (privacy → ShowPresence off) receive every broadcast
	// but never appear in presence lists, cursors, or editing indicators.
	Invisible bool

	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	once sync.Once

	authTimer *time.Timer

	mu      sync.Mutex
	cursor  *Cursor
	editing string
}

// NewClient wires a connection into the hub and starts its pumps.
func NewClient(hub *Hub, conn *websocket.Conn, id, boardID string, p *domain.Principal, invisible bool) *Client {
	c := &Client{
		ID: id, BoardID: boardID, Principal: p, Invisible: invisible,
		hub: hub, conn: conn, send: make(chan []byte, 64),
	}
	hub.Register(c)

	// End the session when the credential ends (JWT exp), clamped so a
	// misconfigured realm can neither churn reconnects nor grant immortality.
	lifetime := defaultAuthLifetime
	if !p.ExpiresAt.IsZero() {
		lifetime = time.Until(p.ExpiresAt)
	}
	if lifetime < minAuthLifetime {
		lifetime = minAuthLifetime
	}
	if lifetime > maxAuthLifetime {
		lifetime = maxAuthLifetime
	}
	c.authTimer = time.AfterFunc(lifetime, func() {
		// 4401 tells the client this is a credential rollover, not an error;
		// its reconnect path fetches a fresh ticket and refetches the board.
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4401, "credential expired"), time.Now().Add(writeWait))
		c.close()
		_ = c.conn.Close()
	})

	go c.writePump()
	go c.readPump()
	return c
}

func (c *Client) presenceUser() PresenceUser {
	c.mu.Lock()
	defer c.mu.Unlock()
	return PresenceUser{
		ClientID: c.ID, Sub: c.Principal.Sub, Name: c.Principal.Name,
		Cursor: c.cursor, Editing: c.editing,
	}
}

// Send queues an envelope; slow consumers are disconnected rather than
// allowed to backpressure the room.
func (c *Client) Send(msg []byte) {
	select {
	case c.send <- msg:
	default:
		c.close()
	}
}

func (c *Client) close() {
	c.once.Do(func() {
		if c.authTimer != nil {
			c.authTimer.Stop()
		}
		c.hub.Unregister(c)
		close(c.send)
	})
}

func (c *Client) readPump() {
	defer func() {
		c.close()
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		c.handle(env)
	}
}

// handle processes client→server events. Cursor moves are the hot path — the
// client throttles them (the SOCKET_THROTTLE analog for continuous drags).
func (c *Client) handle(env Envelope) {
	switch env.Event {
	case "presence.cursor":
		var cur Cursor
		if json.Unmarshal(env.Data, &cur) == nil {
			c.mu.Lock()
			c.cursor = &cur
			c.mu.Unlock()
			if c.Invisible {
				return
			}
			c.hub.broadcast(c.BoardID, mustEnvelope("presence.cursor", map[string]any{
				"clientId": c.ID, "cursor": cur,
			}), c)
		}
	case "editing":
		var payload struct {
			ElementID string `json:"elementId"`
			On        bool   `json:"on"`
		}
		if json.Unmarshal(env.Data, &payload) == nil {
			c.mu.Lock()
			if payload.On {
				c.editing = payload.ElementID
			} else {
				c.editing = ""
			}
			c.mu.Unlock()
			if c.Invisible {
				return
			}
			c.hub.broadcast(c.BoardID, mustEnvelope("element.editing", map[string]any{
				"clientId": c.ID, "sub": c.Principal.Sub, "elementId": payload.ElementID, "on": payload.On,
			}), c)
		}
	case "ping":
		c.Send(mustEnvelope("pong", nil))
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
