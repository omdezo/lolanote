package service

import (
	"context"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The journal row is the commit record, and it used to be written LAST — after
// every op had landed. A retried commit therefore re-ran the whole batch and
// only then hit the duplicate id: creates failed mid-loop on an element
// duplicate-key, leaving a partial write with no journal row and no inverse.
// The run state machine was the only thing preventing it, which made durability
// a property of an in-process goroutine — and a scheduled run has no human to
// not-double-click.
func idempotencyFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, context.Context) {
	t.Helper()
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: "cd00000000000000000ba001", Type: domain.TypeBoard,
		Location:  domain.Location{Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Board"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc, _ := partialWriteFixture(t, elements)
	return svc, elements, ctx
}

func TestIdempotency_ARetriedCommitObservesTheFirstOne(t *testing.T) {
	svc, _, ctx := idempotencyFixture(t)
	alice := &domain.Principal{Sub: "alice"}
	op := func() domain.Op {
		return domain.Op{
			ElementID: "cd00000000000000000bc001", Action: domain.ActionCreate,
			Changes: domain.Content{
				"type": string(domain.TypeCard),
				"location": map[string]any{
					"parentId": "cd00000000000000000ba001", "section": "CANVAS",
				},
				"content": map[string]any{"textPreview": "a note"},
			},
		}
	}
	meta := TxnMeta{TxnID: "txn-pinned-1", Origin: domain.OriginAgent, AgentRunID: "r1"}

	first, err := svc.ApplyWithMeta(ctx, alice, "cd00000000000000000ba001", "", []domain.Op{op()}, meta)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// The retry: same pinned id, same ops. It must NOT re-run the batch.
	second, err := svc.ApplyWithMeta(ctx, alice, "cd00000000000000000ba001", "", []domain.Op{op()}, meta)
	if err != nil {
		t.Fatalf("the retry failed instead of observing the first commit: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the retry produced a second transaction %s", second.ID)
	}
	if len(second.Ops) != len(first.Ops) {
		t.Errorf("the retry returned a different transaction body")
	}
}

// And an ordinary human write, whose id is generated rather than pinned, is
// untouched — two identical drags are two real transactions.
func TestIdempotency_GeneratedIDsAreNotDeduplicated(t *testing.T) {
	svc, _, ctx := idempotencyFixture(t)
	alice := &domain.Principal{Sub: "alice"}
	mk := func(id string) []domain.Op {
		return []domain.Op{{
			ElementID: id, Action: domain.ActionCreate,
			Changes: domain.Content{
				"type": string(domain.TypeCard),
				"location": map[string]any{
					"parentId": "cd00000000000000000ba001", "section": "CANVAS",
				},
				"content": map[string]any{"textPreview": "a note"},
			},
		}}
	}

	first, err := svc.Apply(ctx, alice, "cd00000000000000000ba001", "", mk("cd00000000000000000bd001"))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Apply(ctx, alice, "cd00000000000000000ba001", "", mk("cd00000000000000000bd002"))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two separate human writes collapsed into one transaction")
	}
}
