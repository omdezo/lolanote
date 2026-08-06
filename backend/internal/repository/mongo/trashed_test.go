package mongo

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"qomranote/backend/internal/domain"
)

// JN18, held against a real database.
//
// The old query was a flat find with SetLimit(500). Trashing a container
// cascades its entire live subtree under one trashBatchId, so ending a
// production — the only way to say "we finished" while there is no archive —
// produced 400 trash rows and pushed everything else out of the list. The card
// somebody deleted by accident last Tuesday was still in the database, still
// restorable by id, and no longer visible in the only UI that can restore it.
//
// This is an aggregation, and an aggregation is exactly the kind of change that
// passes every unit test and is wrong in production, so it is tested against
// Mongo itself. Skipped rather than failed when there is no database to talk
// to: the rest of this package is pure logic and must stay runnable without one.
func testStore(t *testing.T) *Store {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store, err := Connect(ctx, uri, "qomranote_trashed_test")
	if err != nil {
		t.Skipf("no mongo at %s: %v", uri, err)
	}
	if err := store.DB.Client().Ping(ctx, nil); err != nil {
		t.Skipf("no mongo at %s: %v", uri, err)
	}
	t.Cleanup(func() {
		_ = store.DB.Drop(context.Background())
	})
	return store
}

func TestTrashed_OneProjectDeletionDoesNotEvictEverythingElse(t *testing.T) {
	store := testStore(t)
	repo := NewElementRepo(store)
	ctx := context.Background()
	_, _ = repo.col.DeleteMany(ctx, bson.M{})

	const me = "u-producer"
	at := func(d time.Duration) *time.Time { u := time.Now().UTC().Add(d); return &u }

	// The accident: one card, deleted on its own, a week ago.
	accident := &domain.Element{
		ID: "ffffffffffffffffffffff01", Type: domain.TypeCard, CreatedBy: me,
		DeletedBy: me, DeletedAt: at(-7 * 24 * time.Hour),
		Content: domain.Content{"title": "the shot we still want"},
	}
	if err := repo.Insert(ctx, accident); err != nil {
		t.Fatal(err)
	}

	// The wrap: a 900-element production, deleted as one batch, yesterday. Under
	// the old flat cap this alone was almost twice the entire window.
	for i := 0; i < 900; i++ {
		el := &domain.Element{
			ID: primitiveHex(i), Type: domain.TypeCard, CreatedBy: me,
			DeletedBy: me, DeletedAt: at(-24 * time.Hour), TrashBatchID: "batch-ep1",
			Content: domain.Content{"title": "ep1 card"},
		}
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Trashed(ctx, me)
	if err != nil {
		t.Fatal(err)
	}

	// The whole point: the accident is still reachable from the one UI that can
	// restore it, after a deletion nearly twice the size of the old window.
	var foundAccident bool
	for _, el := range got {
		if el.ID == accident.ID {
			foundAccident = true
		}
	}
	if !foundAccident {
		t.Fatalf("one project deletion evicted an unrelated deleted card from the trash (%d rows returned)", len(got))
	}
	if len(got) > maxTrashElements {
		t.Fatalf("payload ceiling ignored: %d elements", len(got))
	}
}

func TestTrashed_CountsDeletionsNotElements(t *testing.T) {
	store := testStore(t)
	repo := NewElementRepo(store)
	ctx := context.Background()
	_, _ = repo.col.DeleteMany(ctx, bson.M{})

	const me = "u-producer"
	now := time.Now().UTC()

	// More distinct DELETIONS than the batch cap, each a lone card, so the cap
	// has to bite on batches — and the newest must be the ones that survive,
	// because recency is the order regret arrives in.
	total := maxTrashBatches + 50
	for i := 0; i < total; i++ {
		when := now.Add(-time.Duration(i) * time.Minute)
		el := &domain.Element{
			ID: primitiveHex(i), Type: domain.TypeCard, CreatedBy: me,
			DeletedBy: me, DeletedAt: &when,
			Content: domain.Content{"title": "lone"},
		}
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Trashed(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxTrashBatches {
		t.Fatalf("expected the %d most recent deletions, got %d", maxTrashBatches, len(got))
	}
	// Element 0 is the most recent; element `total-1` the oldest.
	newest, oldestKept := primitiveHex(0), primitiveHex(maxTrashBatches-1)
	seen := map[string]bool{}
	for _, el := range got {
		seen[el.ID] = true
	}
	if !seen[newest] || !seen[oldestKept] {
		t.Fatal("the batch window did not keep the most recent deletions")
	}
	if seen[primitiveHex(total-1)] {
		t.Fatal("the oldest deletion survived a full window of newer ones")
	}
}

func TestTrashed_ElementsWithNoBatchIdAreTheirOwnBatch(t *testing.T) {
	store := testStore(t)
	repo := NewElementRepo(store)
	ctx := context.Background()
	_, _ = repo.col.DeleteMany(ctx, bson.M{})

	const me = "u-producer"
	now := time.Now().UTC()

	// `trashBatchId` is omitempty, so elements trashed before the batch
	// machinery existed carry no field at all. A grouping key that treated
	// "missing" as one value would file every one of them under a single row —
	// years of unrelated deletions collapsed into one, which is the same data
	// loss this change exists to undo, arriving from the other direction.
	for i := 0; i < 3; i++ {
		when := now.Add(-time.Duration(i) * time.Hour)
		el := &domain.Element{
			ID: primitiveHex(i), Type: domain.TypeCard, CreatedBy: me,
			DeletedBy: me, DeletedAt: &when,
			Content: domain.Content{"title": "legacy"},
		}
		if err := repo.Insert(ctx, el); err != nil {
			t.Fatal(err)
		}
	}
	// Prove the field really is absent, not empty — otherwise this test asserts
	// nothing about the case it was written for.
	n, err := repo.col.CountDocuments(ctx, bson.M{"trashBatchId": bson.M{"$exists": false}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 documents with no trashBatchId, found %d", n)
	}

	got, err := repo.Trashed(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("three separate deletions collapsed into %d rows", len(got))
	}
}

// primitiveHex builds a distinct, valid 24-hex element id from a counter.
func primitiveHex(i int) string { return fmt.Sprintf("%024x", i) }
