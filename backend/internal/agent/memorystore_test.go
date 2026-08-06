package agent_test

import (
	"context"
	"testing"
	"time"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/agentmem"
)

// The store's own decay arithmetic, asserted against the port rather than
// against a hand-rolled fake: overriding once is an exception, twice is a
// changed mind. And a citation of an id nothing knows must never create a row —
// a self-report that can write is a write primitive.
func TestMemoryStore_DecayAndTheUnknownID(t *testing.T) {
	ctx := context.Background()
	repo := agentmem.NewMemoryRepo()
	now := time.Now().UTC()

	m := &agent.Memory{ID: "mem_a", Tenant: "omar", BoardID: "b1",
		Text: "Never add a column.", Tier: agent.TierHuman,
		Source: agent.MemoryFromUser, Status: agent.MemoryActive}
	if err := repo.Upsert(ctx, m); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.Record(ctx, "omar", []string{"mem_a", "mem_ghost"}, nil, now); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, _ := repo.List(ctx, "omar", "b1")
	if len(got) != 1 {
		t.Fatalf("a citation of an unknown id created a row: %+v", got)
	}
	if got[0].Hits != 1 {
		t.Errorf("hits = %d, want 1", got[0].Hits)
	}

	_ = repo.Record(ctx, "omar", nil, []string{"mem_a"}, now)
	got, _ = repo.List(ctx, "omar", "b1")
	if got[0].Status != agent.MemoryActive {
		t.Error("one override suspended the rule; departing from your own convention " +
			"once is an exception, not a changed mind")
	}

	_ = repo.Record(ctx, "omar", nil, []string{"mem_a"}, now)
	got, _ = repo.List(ctx, "omar", "b1")
	if got[0].Status != agent.MemorySuspended {
		t.Error("a rule overridden twice stayed in force, so the list can only ever grow")
	}

	// Re-saving the same rule from the suggestion card must not reset its
	// history, or decay is unreachable for exactly the rules people confirm most.
	_ = repo.Upsert(ctx, &agent.Memory{ID: "mem_a", Tenant: "omar", BoardID: "b1",
		Text: "Never add a column.", Tier: agent.TierHuman, Status: agent.MemoryActive})
	got, _ = repo.List(ctx, "omar", "b1")
	if got[0].Hits != 1 || got[0].Misses != 2 {
		t.Errorf("re-saving a rule reset its usage: hits=%d misses=%d", got[0].Hits, got[0].Misses)
	}

	// Another tenant's rules are not this tenant's, and a delete is scoped.
	if err := repo.Delete(ctx, "sara", "mem_a"); err == nil {
		t.Error("one tenant deleted another tenant's standing rule")
	}
}

// A board-scoped rule must not leak onto a different board; an account-wide one
// must reach both.
func TestMemoryStore_ScopeOfARule(t *testing.T) {
	ctx := context.Background()
	repo := agentmem.NewMemoryRepo()
	_ = repo.Upsert(ctx, &agent.Memory{ID: "m1", Tenant: "omar", BoardID: "b1", Text: "board rule"})
	_ = repo.Upsert(ctx, &agent.Memory{ID: "m2", Tenant: "omar", Text: "account rule"})

	here, _ := repo.List(ctx, "omar", "b1")
	if len(here) != 2 {
		t.Fatalf("board b1 sees %d rules, want its own plus the account-wide one", len(here))
	}
	elsewhere, _ := repo.List(ctx, "omar", "b2")
	if len(elsewhere) != 1 || elsewhere[0].ID != "m2" {
		t.Fatalf("board b2 sees %+v — a board rule leaked, or the account rule did not reach it", elsewhere)
	}
}
