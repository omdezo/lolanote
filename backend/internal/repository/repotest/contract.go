// Package repotest holds the behaviour every domain.ElementRepository must
// have, written once and run against each implementation.
//
// The reason it exists: the in-memory repo is a test double for the Mongo one,
// and a double that is quietly less capable than the thing it stands in for
// turns green tests into evidence of nothing. That already happened once —
// Mongo listed labelIds as a patchable root and the double ignored it, so a
// label write passed every test and did nothing in production.
//
// A contract is the only way to state "these two agree" in a form the build can
// check. Run it from each implementation's own package.
package repotest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// NewElementRepo builds an empty repository for one test.
type NewElementRepo func(t *testing.T) domain.ElementRepository

// RunElementRepositoryContract exercises the behaviour every implementation
// must share. Anything asserted here is something a caller relies on.
func RunElementRepositoryContract(t *testing.T, newRepo NewElementRepo) {
	t.Helper()
	t.Run("MergePatch", func(t *testing.T) { mergePatchContract(t, newRepo) })
	t.Run("Lifecycle", func(t *testing.T) { lifecycleContract(t, newRepo) })
}

func seedNote(t *testing.T, repo domain.ElementRepository, id string) *domain.Element {
	t.Helper()
	el := &domain.Element{
		ID: id, Type: domain.TypeCard, CreatedBy: "u-1",
		Location: domain.Location{
			ParentID: "board-1", Section: domain.SectionCanvas,
			Position: domain.Point{X: 10, Y: 20}, Width: 240, Height: 120, Index: 1,
		},
		Content:   domain.Content{"text": "hello", "color": "yellow"},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Insert(context.Background(), el); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return el
}

func mergePatchContract(t *testing.T, newRepo NewElementRepo) {
	ctx := context.Background()

	t.Run("merges content keys and leaves the rest alone", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		got, err := repo.MergePatch(ctx, "e1", domain.Content{
			"content": map[string]any{"text": "goodbye"},
		})
		if err != nil {
			t.Fatalf("patch: %v", err)
		}
		if got.Content["text"] != "goodbye" {
			t.Errorf("text = %v, want the patched value", got.Content["text"])
		}
		if got.Content["color"] != "yellow" {
			t.Errorf("color = %v; a merge patch must not drop untouched keys", got.Content["color"])
		}
	})

	// A caller inside Go passes domain.Content; a caller coming off the wire
	// passes map[string]any. They are the same type underneath and must behave
	// identically — reminder_service does the first, the HTTP path the second.
	t.Run("accepts a nested patch as domain.Content as well as a plain map", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"content": domain.Content{"reminderSent": true},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, err := repo.Get(ctx, "e1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Content["reminderSent"] != true {
			t.Errorf("reminderSent = %v, want true — a domain.Content value was ignored",
				got.Content["reminderSent"])
		}
	})

	t.Run("a null value deletes the key", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"content": map[string]any{"color": nil},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, _ := repo.Get(ctx, "e1")
		if _, present := got.Content["color"]; present {
			t.Error("null must delete the key, per RFC 7386")
		}
		if got.Content["text"] != "hello" {
			t.Error("deleting one key removed another")
		}
	})

	// Every move on the board is an update op carrying location.position, so a
	// repository that only reparents is a repository that cannot move anything.
	t.Run("patches every field of location, not just the parent", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"location": map[string]any{
				"position": map[string]any{"x": 500.0, "y": 640.0},
				"section":  "UNSORTED",
				"width":    320.0,
				"index":    7.5,
			},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, _ := repo.Get(ctx, "e1")
		if got.Location.Position.X != 500 || got.Location.Position.Y != 640 {
			t.Errorf("position = %+v, want {500 640}", got.Location.Position)
		}
		if got.Location.Section != domain.SectionUnsorted {
			t.Errorf("section = %q, want UNSORTED", got.Location.Section)
		}
		if got.Location.Width != 320 {
			t.Errorf("width = %v, want 320", got.Location.Width)
		}
		if got.Location.Index != 7.5 {
			t.Errorf("index = %v, want 7.5", got.Location.Index)
		}
		// Untouched by the patch, so it must survive it.
		if got.Location.ParentID != "board-1" {
			t.Errorf("parentId = %q; a partial location patch replaced the whole object",
				got.Location.ParentID)
		}
		if got.Location.Height != 120 {
			t.Errorf("height = %v, want the untouched 120", got.Location.Height)
		}
	})

	t.Run("reparents", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"location": map[string]any{"parentId": "board-2"},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, _ := repo.Get(ctx, "e1")
		if got.Location.ParentID != "board-2" {
			t.Errorf("parentId = %q, want board-2", got.Location.ParentID)
		}
	})

	// labelIds is a patchable root, and it arrives as []string from Go and as
	// []any from JSON. Both are the same write.
	t.Run("patches labelIds from either shape", func(t *testing.T) {
		for name, value := range map[string]any{
			"[]string": []string{"l1", "l2"},
			"[]any":    []any{"l1", "l2"},
		} {
			repo := newRepo(t)
			seedNote(t, repo, "e1")
			if _, err := repo.MergePatch(ctx, "e1", domain.Content{"labelIds": value}); err != nil {
				t.Fatalf("%s: patch: %v", name, err)
			}
			got, _ := repo.Get(ctx, "e1")
			if len(got.LabelIDs) != 2 || got.LabelIDs[0] != "l1" || got.LabelIDs[1] != "l2" {
				t.Errorf("%s: labelIds = %v, want [l1 l2]", name, got.LabelIDs)
			}
		}
	})

	// Identity, ownership, ACL and trash state have dedicated methods. A patch
	// reaching them would be a privilege escalation through the ordinary write
	// path: op.Changes comes straight from the client.
	t.Run("ignores roots that are not patchable", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		before, _ := repo.Get(ctx, "e1")

		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"_id":       "somebody-elses-id",
			"id":        "somebody-elses-id",
			"createdBy": "attacker",
			"type":      "BOARD",
			"acl":       map[string]any{"ownerId": "attacker"},
			"deletedAt": time.Now().UTC(),
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, err := repo.Get(ctx, "e1")
		if err != nil {
			t.Fatalf("the element is no longer addressable by its id: %v", err)
		}
		if got.ID != before.ID {
			t.Errorf("id = %q, want %q", got.ID, before.ID)
		}
		if got.CreatedBy != before.CreatedBy {
			t.Errorf("createdBy = %q, want %q", got.CreatedBy, before.CreatedBy)
		}
		if got.Type != before.Type {
			t.Errorf("type = %q, want %q", got.Type, before.Type)
		}
		if got.ACL != nil {
			t.Errorf("acl = %+v; ACL has a dedicated method for a reason", got.ACL)
		}
		if got.IsDeleted() {
			t.Error("a merge patch trashed an element")
		}
	})

	t.Run("stamps updatedAt", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		before, _ := repo.Get(ctx, "e1")
		time.Sleep(2 * time.Millisecond)
		if _, err := repo.MergePatch(ctx, "e1", domain.Content{
			"content": map[string]any{"text": "x"},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		got, _ := repo.Get(ctx, "e1")
		if !got.UpdatedAt.After(before.UpdatedAt) {
			t.Errorf("updatedAt did not advance (%v → %v)", before.UpdatedAt, got.UpdatedAt)
		}
	})

	t.Run("a missing element is ErrNotFound, not a silent create", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := repo.MergePatch(ctx, "nope", domain.Content{
			"content": map[string]any{"text": "x"},
		}); err == nil {
			t.Fatal("patching a missing element succeeded")
		}
		if _, err := repo.Get(ctx, "nope"); err == nil {
			t.Fatal("the failed patch created the element")
		}
	})

	// The returned element and a fresh read must agree. A repo that returns the
	// patched value but stores something else makes every caller's optimistic
	// state a lie.
	t.Run("what it returns is what it stored", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		returned, err := repo.MergePatch(ctx, "e1", domain.Content{
			"content": map[string]any{"text": "stored?"},
		})
		if err != nil {
			t.Fatalf("patch: %v", err)
		}
		reread, _ := repo.Get(ctx, "e1")
		if returned.Content["text"] != reread.Content["text"] {
			t.Errorf("returned %v, stored %v", returned.Content["text"], reread.Content["text"])
		}
	})

	// A repository that hands out its own storage lets a caller mutate the
	// store by editing what it was given.
	t.Run("does not hand out aliases of its own state", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		got, _ := repo.Get(ctx, "e1")
		got.Content["text"] = "mutated through the returned value"
		got.Location.ParentID = "somewhere-else"

		fresh, _ := repo.Get(ctx, "e1")
		if fresh.Content["text"] != "hello" {
			t.Errorf("content aliased: %v", fresh.Content["text"])
		}
		if fresh.Location.ParentID != "board-1" {
			t.Errorf("location aliased: %v", fresh.Location.ParentID)
		}
	})

	// The one the hub's header always promised and the write never delivered.
	//
	// hub.go states the concurrency model as "two users on different cards merge
	// trivially; the same card resolves server-authoritatively (last writer
	// wins)" — last writer wins THE FIELD, which is what a merge patch means. The
	// Mongo write underneath was a ReplaceOne of the whole document built from a
	// read taken before the other writer's read, so it was last writer wins the
	// DOCUMENT: A moves a card while B tags it, and whichever landed second put
	// back everything the other had changed. On a shared production board that
	// reads as "my label came off by itself", which is unreportable and therefore
	// never got reported.
	//
	// Stated as concurrency because the defect only exists between two writers.
	t.Run("two writers patching disjoint fields both keep their change", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")

		const writers = 8
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Disjoint by construction: each writer owns one content key,
				// so no outcome is ambiguous and any missing key is a lost write.
				_, errs[i] = repo.MergePatch(ctx, "e1", domain.Content{
					"content": map[string]any{fmt.Sprintf("k%d", i): i},
				})
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
		}
		got, err := repo.Get(ctx, "e1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		for i := 0; i < writers; i++ {
			if _, present := got.Content[fmt.Sprintf("k%d", i)]; !present {
				t.Errorf("writer %d's field is gone: a concurrent patch overwrote it wholesale", i)
			}
		}
		if got.Content["text"] != "hello" {
			t.Error("a field nobody patched did not survive the batch")
		}
	})
}

func lifecycleContract(t *testing.T, newRepo NewElementRepo) {
	ctx := context.Background()

	t.Run("inserting the same id twice conflicts", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		el := &domain.Element{ID: "e1", Type: domain.TypeCard, Location: domain.Location{ParentID: "board-1"}}
		if err := repo.Insert(ctx, el); err == nil {
			t.Fatal("a duplicate id was accepted")
		}
	})

	t.Run("soft delete hides an element from its parent's children", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		seedNote(t, repo, "e2")
		if err := repo.SoftDelete(ctx, []string{"e1"}, "u-1", "batch-1", time.Now().UTC()); err != nil {
			t.Fatalf("soft delete: %v", err)
		}
		kids, err := repo.Children(ctx, domain.ElementFilter{ParentID: "board-1"})
		if err != nil {
			t.Fatalf("children: %v", err)
		}
		if len(kids) != 1 || kids[0].ID != "e2" {
			t.Fatalf("children = %v, want only e2", ids(kids))
		}
		// Still readable by id — the trash view needs it.
		if _, err := repo.Get(ctx, "e1"); err != nil {
			t.Errorf("a trashed element must remain addressable: %v", err)
		}
		withDeleted, _ := repo.Children(ctx, domain.ElementFilter{ParentID: "board-1", IncludeDeleted: true})
		if len(withDeleted) != 2 {
			t.Errorf("IncludeDeleted returned %d, want 2", len(withDeleted))
		}
	})

	t.Run("restore by batch brings back exactly that batch", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		seedNote(t, repo, "e2")
		now := time.Now().UTC()
		_ = repo.SoftDelete(ctx, []string{"e1"}, "u-1", "batch-1", now)
		_ = repo.SoftDelete(ctx, []string{"e2"}, "u-1", "batch-2", now)

		if err := repo.RestoreBatch(ctx, "batch-1"); err != nil {
			t.Fatalf("restore batch: %v", err)
		}
		kids, _ := repo.Children(ctx, domain.ElementFilter{ParentID: "board-1"})
		if len(kids) != 1 || kids[0].ID != "e1" {
			t.Fatalf("children = %v, want only the restored e1", ids(kids))
		}
	})

	t.Run("children filter by section", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		seedNote(t, repo, "e2")
		if _, err := repo.MergePatch(ctx, "e2", domain.Content{
			"location": map[string]any{"section": "UNSORTED"},
		}); err != nil {
			t.Fatalf("patch: %v", err)
		}
		unsorted, _ := repo.Children(ctx, domain.ElementFilter{
			ParentID: "board-1", Section: domain.SectionUnsorted,
		})
		if len(unsorted) != 1 || unsorted[0].ID != "e2" {
			t.Fatalf("unsorted = %v, want only e2", ids(unsorted))
		}
	})

	t.Run("GetMany skips ids that do not exist rather than failing", func(t *testing.T) {
		repo := newRepo(t)
		seedNote(t, repo, "e1")
		got, err := repo.GetMany(ctx, []string{"e1", "missing"})
		if err != nil {
			t.Fatalf("get many: %v", err)
		}
		if len(got) != 1 || got[0].ID != "e1" {
			t.Fatalf("got %v, want just e1", ids(got))
		}
	})
}

func ids(els []*domain.Element) []string {
	out := make([]string, 0, len(els))
	for _, e := range els {
		out = append(out, e.ID)
	}
	return out
}
