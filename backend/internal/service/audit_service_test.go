package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

func auditIDs() IDGenerator {
	n := 0
	return func() string { n++; return fmt.Sprintf("%024x", n) }
}

// testAudit is what the service fixtures wire, returned WITH its store: a
// fixture that handed back only the service would make the rows a call site
// writes unreadable, and every one of those call sites is a security event
// somebody will eventually want to pin a test to.
func testAudit() (*AuditService, *memory.AuditRepo) {
	repo := memory.NewAuditRepo()
	return NewAuditService(repo, auditIDs(), zap.NewNop()), repo
}

// Everything that makes a row trustworthy is stamped by the service, so a call
// site that supplies only the verb and the object still produces a complete
// row. The alternative — each call site filling in actor, time and request id —
// is a field that is wrong in whichever of them was written last.
func TestAuditEmit_StampsWhatTheCallSiteDidNotSay(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())

	p := &domain.Principal{Sub: "u-omar", RequestID: "req-77"}
	before := time.Now().UTC()
	svc.Emit(context.Background(), p, &domain.AuditEvent{
		OrgID:  "org-1",
		Action: "share.link_created",
		Target: domain.AuditTarget{Type: "board", ID: "b-1", Label: "Series A"},
	})

	rows, next, err := repo.List(context.Background(), domain.AuditFilter{OrgID: "org-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if next != "" {
		t.Errorf("a single row handed out a next-page cursor %q", next)
	}
	ev := rows[0]
	if ev.ID == "" {
		t.Error("the row has no id, so it can never be a page boundary")
	}
	if ev.Actor.Sub != "u-omar" {
		t.Errorf("actor.sub = %q, want the principal's sub", ev.Actor.Sub)
	}
	if ev.RequestID != "req-77" {
		t.Errorf("requestId = %q, want the principal's — it is the join to the logs", ev.RequestID)
	}
	if ev.At.Before(before) || ev.At.IsZero() {
		t.Errorf("at = %v, want a stamp from this call", ev.At)
	}
	if ev.Outcome != domain.AuditOK {
		t.Errorf("outcome = %q, want the default %q", ev.Outcome, domain.AuditOK)
	}
}

// §9 wants "who did what, when, FROM WHERE". The where reaches the row the same
// way the request id does — off the principal — because no call site is in a
// position to know it, and the data.export row is the one where it matters most.
func TestAuditEmit_StampsWhereTheRequestCameFrom(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())

	p := &domain.Principal{Sub: "u-omar", IP: "203.0.113.7", UserAgent: "Mozilla/5.0 (probe)"}
	svc.Emit(context.Background(), p, &domain.AuditEvent{Action: "data.export"})

	rows, _, err := repo.List(context.Background(), domain.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].IP != "203.0.113.7" {
		t.Errorf("ip = %q, want the principal's — an export row that cannot say from where answers half of §9", rows[0].IP)
	}
	if rows[0].UA != "Mozilla/5.0 (probe)" {
		t.Errorf("ua = %q, want the principal's", rows[0].UA)
	}
}

// The principal is the fallback, not an override. A call site that names an
// address knows something this layer does not.
func TestAuditEmit_AnExplicitOriginWins(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())

	p := &domain.Principal{Sub: "u-omar", IP: "203.0.113.7", UserAgent: "browser"}
	svc.Emit(context.Background(), p, &domain.AuditEvent{
		Action: "data.export",
		IP:     "198.51.100.2",
		UA:     "qomra-cli/1.0",
	})

	rows, _, err := repo.List(context.Background(), domain.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].IP != "198.51.100.2" {
		t.Errorf("ip = %q, want the value the call site set", rows[0].IP)
	}
	if rows[0].UA != "qomra-cli/1.0" {
		t.Errorf("ua = %q, want the value the call site set", rows[0].UA)
	}
}

// The actor is the load-bearing field: a row that can name someone other than
// the authenticated caller is a row that can be made to accuse the wrong
// person. The principal wins, whatever the call site passed.
func TestAuditEmit_ThePrincipalOwnsTheActor(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())

	svc.Emit(context.Background(), &domain.Principal{Sub: "u-real"}, &domain.AuditEvent{
		Action: "org.member_removed",
		Actor:  domain.AuditActor{Sub: "u-someone-else"},
	})

	rows, _, err := repo.List(context.Background(), domain.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Actor.Sub != "u-real" {
		t.Errorf("actor.sub = %q; the call site was allowed to name a different person", rows[0].Actor.Sub)
	}
}

// A delegated write is the human's action AND the run's. Dropping the run id
// leaves "Omar deleted forty cards" with no way back to the plan he approved.
func TestAuditEmit_CarriesTheDelegationRun(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())

	p := &domain.Principal{Sub: "u-omar", Delegation: &domain.Delegation{RunID: "run-9", OnBehalfOf: "u-omar"}}
	svc.Emit(context.Background(), p, &domain.AuditEvent{Action: "agent.run_applied"})

	rows, _, err := repo.List(context.Background(), domain.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Actor.Sub != "u-omar" {
		t.Errorf("actor.sub = %q, want the human the grant attenuates", rows[0].Actor.Sub)
	}
	if rows[0].Actor.AgentRunID != "run-9" {
		t.Errorf("actor.agentRunId = %q, want run-9", rows[0].Actor.AgentRunID)
	}
}

// failingAudit is the store on a bad day.
type failingAudit struct{ calls int }

var errAuditDown = errors.New("audit store unavailable")

func (f *failingAudit) Insert(context.Context, *domain.AuditEvent) error {
	f.calls++
	return errAuditDown
}

func (f *failingAudit) List(context.Context, domain.AuditFilter) ([]*domain.AuditEvent, string, error) {
	return nil, "", errAuditDown
}

// Audit failure must not fail the user's action. The share has already been
// revoked by the time Emit is called; turning a hiccup on the log into an error
// the caller could propagate would lie about the outcome and invite a retry of
// something that already succeeded. Emit has no error return at all, so this
// pins the other half: the hole is LOUD.
func TestAuditEmit_SwallowsTheInsertErrorAndSaysSo(t *testing.T) {
	core, logs := observer.New(zapcore.ErrorLevel)
	repo := &failingAudit{}
	svc := NewAuditService(repo, auditIDs(), zap.New(core))

	svc.Emit(context.Background(), &domain.Principal{Sub: "u-omar", RequestID: "req-3"},
		&domain.AuditEvent{Action: "share.link_revoked"})

	if repo.calls != 1 {
		t.Fatalf("insert attempted %d times, want 1", repo.calls)
	}
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want exactly one Error line for a dropped audit row, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["action"] != "share.link_revoked" {
		t.Errorf("the log line does not name the action: %v", fields)
	}
	if fields["requestId"] != "req-3" {
		t.Errorf("the log line does not carry the request id, so the missing row cannot be found: %v", fields)
	}
}

// The read the org console and the Enterprise JSON API both sit on: newest
// first, filtered by taxonomy prefix, paged on a cursor that is empty at the
// end. The empty cursor is the assertion — a viewer that is handed one at every
// exact multiple of the limit walks into a blank page.
func TestAuditList_PrefixFilterAndCursorPaging(t *testing.T) {
	repo := memory.NewAuditRepo()
	svc := NewAuditService(repo, auditIDs(), zap.NewNop())
	ctx := context.Background()
	p := &domain.Principal{Sub: "u-omar"}

	for i := 0; i < 4; i++ {
		svc.Emit(ctx, p, &domain.AuditEvent{OrgID: "org-1", Action: "org.member_role_changed"})
	}
	svc.Emit(ctx, p, &domain.AuditEvent{OrgID: "org-1", Action: "billing.plan_changed"})
	svc.Emit(ctx, p, &domain.AuditEvent{OrgID: "org-2", Action: "org.member_removed"})

	f := domain.AuditFilter{OrgID: "org-1", ActionPrefix: "org.", Limit: 3}
	page, next, err := repo.List(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 {
		t.Fatalf("first page had %d rows, want 3", len(page))
	}
	if next == "" {
		t.Fatal("no cursor after a full page, so the fourth row is unreachable")
	}
	if page[0].ID <= page[2].ID {
		t.Error("the page is not newest-first")
	}

	f.Cursor = next
	page2, next2, err := repo.List(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 {
		t.Fatalf("second page had %d rows, want the 1 remaining org. row", len(page2))
	}
	if next2 != "" {
		t.Errorf("cursor %q handed out at the end of the log", next2)
	}
	if page2[0].Action != "org.member_role_changed" {
		t.Errorf("prefix filter leaked %q into an org.-only read", page2[0].Action)
	}
}
