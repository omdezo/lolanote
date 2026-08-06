package auth

import (
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// The handshake ticket store was an unbounded, unrated allocator.
//
// POST /api/v1/realtime/ticket has no rate limiter of its own — only the agent
// run endpoint does — and Issue held the global mutex while walking every live
// entry, pinning a copied domain.Principal per ticket. One authenticated caller
// looping the endpoint inside the 30-second TTL therefore drove O(N^2)
// lock-held work, and the cost landed on every other user sharing the process,
// not on the caller.
//
// These tests hold the two properties that make that impossible: a caller's
// live tickets are capped, and the sweep no longer runs on every issue. They
// also hold the two properties the fix must not have broken — single use, and
// a principal snapshotted at issue time.

func principal(sub string) *domain.Principal {
	return &domain.Principal{Sub: sub, Email: sub + "@example.test"}
}

func TestTickets_OneCallerCannotGrowTheStoreWithoutBound(t *testing.T) {
	s := NewTicketStore()
	now := time.Now()

	// Well inside one TTL, so nothing expires and every ticket would have been
	// live under the old code.
	var tokens []string
	for i := 0; i < maxLiveTicketsPerSubject*10; i++ {
		tokens = append(tokens, s.Issue(principal("noisy"), now))
	}

	s.mu.Lock()
	live := len(s.tickets)
	counted := s.bySubject["noisy"]
	s.mu.Unlock()

	if live > maxLiveTicketsPerSubject {
		t.Fatalf("one caller holds %d live tickets; the cap is %d", live, maxLiveTicketsPerSubject)
	}
	// The counter is the thing the cap consults, so a counter that drifts from
	// the map is the cap silently switching itself off.
	if counted != live {
		t.Fatalf("per-subject count %d disagrees with the map's %d", counted, live)
	}

	// Evicting the OLDEST is the direction that keeps the working case working:
	// the ticket a client is about to redeem is the one it just asked for.
	if _, ok := s.Redeem(tokens[len(tokens)-1], now); !ok {
		t.Fatal("the most recently issued ticket was evicted; the eviction order is backwards")
	}
	if _, ok := s.Redeem(tokens[0], now); ok {
		t.Fatal("the oldest ticket survived a 320-ticket flood")
	}
}

func TestTickets_OneCallerCannotEvictAnother(t *testing.T) {
	s := NewTicketStore()
	now := time.Now()

	victim := s.Issue(principal("quiet"), now)
	for i := 0; i < maxLiveTicketsPerSubject*4; i++ {
		s.Issue(principal("noisy"), now)
	}

	// The cap is per subject precisely so that flooding is self-harm rather
	// than a denial of service against everyone else on the process.
	p, ok := s.Redeem(victim, now)
	if !ok {
		t.Fatal("a flood from one caller evicted another caller's live ticket")
	}
	if p.Sub != "quiet" {
		t.Fatalf("redeemed the wrong principal: %q", p.Sub)
	}
}

func TestTickets_TheSweepIsAmortisedNotPerIssue(t *testing.T) {
	s := NewTicketStore()
	start := time.Now()

	// One expired ticket, then a burst inside a single TTL.
	stale := s.Issue(principal("a"), start)
	later := start.Add(s.ttl + time.Second) // `stale` is now expired
	for i := 0; i < 5; i++ {
		s.Issue(principal("b"), later)
	}

	// The first issue after the TTL elapsed sweeps; the four behind it must not,
	// which is the whole point — the old code walked every live entry on every
	// call, under the lock.
	s.mu.Lock()
	swept := s.lastSweep
	s.mu.Unlock()
	if !swept.Equal(later) {
		t.Fatalf("expected one sweep stamped at %v, got %v", later, swept)
	}

	// Amortising must not resurrect anything: an entry that outlives the sweep
	// is unusable, merely unreclaimed, because Redeem re-checks the expiry.
	if _, ok := s.Redeem(stale, later); ok {
		t.Fatal("an expired ticket was redeemable because the sweep had not run yet")
	}
}

// The WebSocket handshake authorises off the ticket's principal, so platform
// roles have to survive Issue/Redeem — a socket opened from a role-stripped
// ticket would quietly lose staff access — and they have to survive it as a
// copy. The struct copy is shallow: an aliased slice would leave a live ticket
// and the caller's principal sharing one array, so a revoked staffer could hold
// an admin socket that disagrees with the principal it was minted from.
func TestTickets_CarryPlatformRolesAsASnapshot(t *testing.T) {
	s := NewTicketStore()
	now := time.Now()

	p := principal("staffer")
	p.PlatformRoles = []string{"platform-admin"}
	token := s.Issue(p, now)

	p.PlatformRoles[0] = "revoked"
	p.PlatformRoles = append(p.PlatformRoles, "platform-support")

	got, ok := s.Redeem(token, now)
	if !ok {
		t.Fatal("a fresh ticket did not redeem")
	}
	if !got.HasPlatformRole("platform-admin") {
		t.Fatalf("platform roles did not survive the ticket copy: %v", got.PlatformRoles)
	}
	if len(got.PlatformRoles) != 1 {
		t.Fatalf("the ticket followed a later mutation of the caller's roles: %v", got.PlatformRoles)
	}
}

func TestTickets_StillSingleUseAndStillSnapshotsThePrincipal(t *testing.T) {
	s := NewTicketStore()
	now := time.Now()

	p := principal("someone")
	token := s.Issue(p, now)

	// A later mutation of the caller's principal must not reach a live ticket.
	p.Sub = "someone-else"

	got, ok := s.Redeem(token, now)
	if !ok {
		t.Fatal("a fresh ticket did not redeem")
	}
	if got.Sub != "someone" {
		t.Fatalf("the ticket followed a later mutation of the principal: %q", got.Sub)
	}
	if _, ok := s.Redeem(token, now); ok {
		t.Fatal("a ticket redeemed twice")
	}
}
