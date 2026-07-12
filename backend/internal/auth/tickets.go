package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"qomranote/backend/internal/domain"
)

// TicketStore issues short-lived, single-use tickets for the WebSocket
// handshake. Browsers cannot set Authorization headers on a WS upgrade, so
// instead of putting the long-lived bearer token in the query string (where
// it leaks into access logs), the client exchanges its token for a ticket
// over the authenticated REST channel and presents the ticket at handshake.
type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]ticketEntry
	ttl     time.Duration
}

type ticketEntry struct {
	principal *domain.Principal
	expires   time.Time
}

// NewTicketStore creates a store with a 30-second ticket lifetime.
func NewTicketStore() *TicketStore {
	s := &TicketStore{tickets: map[string]ticketEntry{}, ttl: 30 * time.Second}
	return s
}

// Issue mints a ticket bound to the principal.
func (s *TicketStore) Issue(p *domain.Principal, now time.Time) string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	// Copy the principal so a later request mutation cannot alter a live ticket.
	cp := *p
	s.tickets[token] = ticketEntry{principal: &cp, expires: now.Add(s.ttl)}
	return token
}

// Redeem consumes a ticket exactly once, returning its principal.
func (s *TicketStore) Redeem(token string, now time.Time) (*domain.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tickets[token]
	if !ok {
		return nil, false
	}
	delete(s.tickets, token) // single use
	if now.After(entry.expires) {
		return nil, false
	}
	return entry.principal, true
}

// sweep drops expired tickets; callers hold the lock.
func (s *TicketStore) sweep(now time.Time) {
	for token, entry := range s.tickets {
		if now.After(entry.expires) {
			delete(s.tickets, token)
		}
	}
}
