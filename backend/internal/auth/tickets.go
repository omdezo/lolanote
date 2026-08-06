package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
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
	// bySubject counts live tickets per caller, so the cap below can be enforced
	// without walking the whole map on every issue.
	bySubject map[string]int
	ttl       time.Duration
	// lastSweep amortises the expiry walk. See Issue.
	lastSweep time.Time
}

type ticketEntry struct {
	principal *domain.Principal
	subject   string
	expires   time.Time
	// issued orders a subject's own tickets so the cap can evict the oldest
	// rather than an arbitrary map entry.
	issued time.Time
}

// maxLiveTicketsPerSubject caps how many unredeemed tickets one caller may hold.
//
// A ticket exists to bridge a single WebSocket handshake and lives 30 seconds,
// so a real client holds one, occasionally two across a reconnect. Thirty-two is
// far above any honest use and far below the point where the map costs anything.
//
// Without it, POST /api/v1/realtime/ticket was an unbounded, unrated allocator:
// the endpoint has no limiter of its own (only /agent/runs does), every issue
// held the global mutex while walking every live entry, and each entry pinned a
// copied domain.Principal. One authenticated caller looping the endpoint inside
// the 30-second TTL therefore drove O(N^2) lock-held work — degrading the
// handshake for every other user on the process, not only for themselves.
const maxLiveTicketsPerSubject = 32

// NewTicketStore creates a store with a 30-second ticket lifetime.
func NewTicketStore() *TicketStore {
	s := &TicketStore{
		tickets:   map[string]ticketEntry{},
		bySubject: map[string]int{},
		ttl:       30 * time.Second,
	}
	return s
}

// Issue mints a ticket bound to the principal.
func (s *TicketStore) Issue(p *domain.Principal, now time.Time) string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Amortised, not per-issue. The sweep is O(live tickets) and used to run on
	// every single call while holding this mutex — which made the cost of one
	// caller's traffic quadratic in that same caller's traffic, and paid by
	// everybody. Nothing is kept past its TTL by waiting: Redeem re-checks the
	// expiry on the way out, so an entry that outlives the sweep is unusable,
	// merely unreclaimed, and at most one TTL of them accumulates.
	if now.Sub(s.lastSweep) >= s.ttl {
		s.sweep(now)
		s.lastSweep = now
	}

	// Copy the principal so a later request mutation cannot alter a live ticket.
	// The struct copy is shallow, so the role slice has to be copied too: a
	// ticket authorises a socket, and it must snapshot what it was issued for
	// rather than stay a window onto a slice its caller still holds.
	cp := *p
	cp.PlatformRoles = append([]string(nil), p.PlatformRoles...)
	s.evictOldestOver(p.Sub, maxLiveTicketsPerSubject-1, now)
	s.tickets[token] = ticketEntry{principal: &cp, subject: p.Sub, expires: now.Add(s.ttl), issued: now}
	s.bySubject[p.Sub]++
	return token
}

// evictOldestOver drops this subject's oldest tickets until at most `keep`
// remain, so one caller can never grow the map without bound.
//
// Evicting the OLDEST is the direction that preserves the working case: the
// ticket a client is about to redeem is the one it just asked for, and a
// 30-second-old unredeemed ticket is one whose handshake already failed.
func (s *TicketStore) evictOldestOver(sub string, keep int, now time.Time) {
	if s.bySubject[sub] <= keep {
		return
	}
	// Only this subject's entries, so a noisy caller's cap costs a walk
	// proportional to their own holdings and never to everyone else's.
	mine := make([]string, 0, s.bySubject[sub])
	for token, entry := range s.tickets {
		if entry.subject == sub {
			mine = append(mine, token)
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		return s.tickets[mine[i]].issued.Before(s.tickets[mine[j]].issued)
	})
	for i := 0; i < len(mine)-keep; i++ {
		s.drop(mine[i])
	}
}

// drop removes one ticket and keeps the per-subject count honest. Callers hold
// the lock.
func (s *TicketStore) drop(token string) {
	entry, ok := s.tickets[token]
	if !ok {
		return
	}
	delete(s.tickets, token)
	if n := s.bySubject[entry.subject] - 1; n > 0 {
		s.bySubject[entry.subject] = n
	} else {
		// Deleted rather than left at zero: the counter map is keyed by subject
		// and would otherwise be a second unbounded structure with exactly the
		// growth problem this change exists to fix.
		delete(s.bySubject, entry.subject)
	}
}

// Redeem consumes a ticket exactly once, returning its principal.
func (s *TicketStore) Redeem(token string, now time.Time) (*domain.Principal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tickets[token]
	if !ok {
		return nil, false
	}
	s.drop(token) // single use
	if now.After(entry.expires) {
		return nil, false
	}
	return entry.principal, true
}

// sweep drops expired tickets; callers hold the lock.
func (s *TicketStore) sweep(now time.Time) {
	for token, entry := range s.tickets {
		if now.After(entry.expires) {
			s.drop(token)
		}
	}
}
