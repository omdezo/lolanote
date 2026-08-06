package mongo

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"qomranote/backend/internal/domain"
)

// Cursor paging is the kind of change that passes every unit test and is wrong
// in production — the sort, the strict inequality and the over-fetch have to
// agree with each other, and only the database can say whether they do. Skipped
// rather than failed when there is nothing to talk to, like the rest of this
// package's live tests.
//
// What it pins: a page boundary that lands inside a burst — a bulk role change,
// an impersonated session, an agent run applying forty ops — must neither
// repeat a row nor skip one, which is the whole reason the cursor is the
// time-ordered _id and not the timestamp.
func TestAuditList_PagesWithoutRepeatingOrSkipping(t *testing.T) {
	store := testStore(t)
	repo := NewAuditRepo(store)
	ctx := context.Background()
	_, _ = repo.col.DeleteMany(ctx, bson.M{})

	// All at the same instant on purpose: an `at`-keyed cursor cannot page this.
	at := time.Now().UTC()
	const total = 25
	for i := 0; i < total; i++ {
		if err := repo.Insert(ctx, &domain.AuditEvent{
			OrgID:   "org-1",
			Actor:   domain.AuditActor{Sub: "u-omar"},
			Action:  "org.member_role_changed",
			Target:  domain.AuditTarget{Type: "membership", ID: "m-1"},
			Outcome: domain.AuditOK,
			At:      at,
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// One row the filter must not return, so a page that "works" by returning
	// everything is still a failure.
	if err := repo.Insert(ctx, &domain.AuditEvent{
		OrgID: "org-2", Action: "org.member_removed", At: at,
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	f := domain.AuditFilter{OrgID: "org-1", ActionPrefix: "org.member_role", Limit: 10}
	pages := 0
	for {
		rows, next, err := repo.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if pages > 5 {
			t.Fatal("the cursor never emptied — this pages forever")
		}
		for _, ev := range rows {
			if seen[ev.ID] {
				t.Fatalf("row %s came back on two pages", ev.ID)
			}
			seen[ev.ID] = true
			if ev.OrgID != "org-1" {
				t.Fatalf("org-2's row leaked into an org-1 read")
			}
		}
		if next == "" {
			if len(rows) == 0 && pages > 1 {
				t.Error("the last cursor pointed at an empty page")
			}
			break
		}
		f.Cursor = next
	}
	if len(seen) != total {
		t.Errorf("paged %d rows, want %d — the cursor skipped some", len(seen), total)
	}
}

// The prefix is anchored: `share.` must not match `org.share_something`, and a
// dot in the taxonomy is a dot rather than a regex wildcard.
func TestAuditList_PrefixIsAnchoredAndLiteral(t *testing.T) {
	store := testStore(t)
	repo := NewAuditRepo(store)
	ctx := context.Background()
	_, _ = repo.col.DeleteMany(ctx, bson.M{})

	at := time.Now().UTC()
	for _, action := range []string{"share.link_created", "org.share_policy_changed", "shareXlink_created"} {
		if err := repo.Insert(ctx, &domain.AuditEvent{OrgID: "org-1", Action: action, At: at}); err != nil {
			t.Fatal(err)
		}
	}

	rows, _, err := repo.List(ctx, domain.AuditFilter{OrgID: "org-1", ActionPrefix: "share."})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Action != "share.link_created" {
		var got []string
		for _, ev := range rows {
			got = append(got, ev.Action)
		}
		t.Errorf("share. matched %v, want only share.link_created", got)
	}
}
