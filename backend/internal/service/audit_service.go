package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// AuditService is the one way a row reaches the audit log (§9). Call sites
// describe what happened; everything that makes the row trustworthy — who,
// when, under which request — is stamped here, because a field a hundred call
// sites each fill in themselves is a field that is wrong in some of them.
type AuditService struct {
	repo  domain.AuditRepository
	newID IDGenerator
	log   *zap.Logger
}

// NewAuditService constructs the service.
func NewAuditService(repo domain.AuditRepository, newID IDGenerator, log *zap.Logger) *AuditService {
	return &AuditService{repo: repo, newID: newID, log: log.Named("audit")}
}

// Emit records one event and returns nothing.
//
// The missing error return is the design, not an omission: audit failure must
// never fail the user's action. A share revoked, a member removed, an account
// deleted — those have already happened by the time we get here, and turning a
// database hiccup on the log into a 500 would both lie about the outcome and
// invite a retry of an operation that already succeeded. So a failed insert
// logs at Error and the caller carries on: availability over completeness for
// v1. Enterprise hash-chain mode (§12.5) inverts this deliberately — a chain
// with a hole attests to nothing, so that mode fails closed — and it will need
// a second entry point rather than a change to this one.
//
// The principal is an explicit argument rather than something fished out of the
// context because the actor is the load-bearing field of the row: a call site
// that has no principal has to say so.
// A nil receiver is a no-op for the same reason the insert error is swallowed:
// the action has already happened, and a wiring gap must cost the trail a row
// rather than cost the user their share revocation. Every call site can then
// emit unconditionally instead of guarding, which is what keeps the guards from
// drifting apart.
func (s *AuditService) Emit(ctx context.Context, p *domain.Principal, ev *domain.AuditEvent) {
	if s == nil || ev == nil {
		return
	}
	if ev.ID == "" {
		ev.ID = s.newID()
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Outcome == "" {
		ev.Outcome = domain.AuditOK
	}
	if p != nil {
		// The principal wins over whatever the caller put in Actor.Sub: the actor
		// is who the request authenticated as, and a row that can name someone
		// else is a row that can be made to accuse the wrong person.
		ev.Actor.Sub = p.Sub
		if ev.RequestID == "" {
			ev.RequestID = p.RequestID
		}
		// "From where" is half of §9's requirement and no call site is in a
		// position to supply it, so it rides the principal like the request id.
		// An explicit value wins: a call site that names an address knows
		// something this layer does not — a webhook's origin, a replayed job's
		// original caller.
		if ev.IP == "" {
			ev.IP = p.IP
		}
		if ev.UA == "" {
			ev.UA = p.UserAgent
		}
		// A delegated write is the human's action AND the run's; the run id is
		// what turns "Omar deleted forty cards" back into the plan he approved.
		if p.Delegation != nil && ev.Actor.AgentRunID == "" {
			ev.Actor.AgentRunID = p.Delegation.RunID
		}
	}
	if err := s.repo.Insert(ctx, ev); err != nil {
		// The metric this increments is II.0.4's; until it exists the log line is
		// the only signal that the trail has a hole, so it carries enough to find
		// the missing row in the request logs.
		s.log.Error("audit insert failed",
			zap.String("action", ev.Action),
			zap.String("actor", ev.Actor.Sub),
			zap.String("requestId", ev.RequestID),
			zap.Error(err))
	}
}
