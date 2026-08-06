package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// SEC4 / SEC5 — two IDOR reads of the same relationship, one destructive.
//
// SEC4: the trash's restore and destroy paths tested AUTHORSHIP and nothing
// else. Authorship is a fact about the past that no revocation can take back,
// and the trash query is `deletedBy = me OR createdBy = me` over the whole
// database — so every element you ever created on somebody else's board is in
// your trash list forever, including after they remove you. DELETE /trash/:id
// then hard-deletes it AND its whole live subtree, taking the owner's cards, the
// attachment bytes and the journal's copy with it. No board, no ACL, and no
// current relationship was ever consulted.
//
// SEC5: CloneInstances checked view rights on the SOURCE and then returned the
// id and TITLE of every board holding a copy. A collaborator who clones your
// card onto their private board puts that board's name in your reply. The read
// path already fixed the same relationship from the other end (BoardService
// .Children, resolving a clone's source unchecked); this end kept the bug.

type trashFixture struct {
	elements *memory.ElementRepo
	svc      *ElementService
	acl      *domain.ACL
}

// Bob owns the board. Alice was an editor and, while she was, created a column
// on it; Bob later filled the column with his own cards and trashed the lot.
func newTrashFixture(t *testing.T) *trashFixture {
	t.Helper()
	ctx := context.Background()
	elements := memory.NewElementRepo()
	now := time.Now().UTC()

	acl := &domain.ACL{OwnerID: "bob", Editors: []string{"alice"}}
	mk := func(id string, typ domain.ElementType, parent, creator string, acl *domain.ACL) {
		if err := elements.Insert(ctx, &domain.Element{
			ID: id, Type: typ,
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   domain.Content{"title": id},
			ACL:       acl,
			CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("7777777777777777777ab001", domain.TypeBoard, "", "bob", acl)
	mk("7777777777777777777ab002", domain.TypeColumn, "7777777777777777777ab001", "alice", nil)
	mk("7777777777777777777ab003", domain.TypeCard, "7777777777777777777ab002", "bob", nil)
	mk("7777777777777777777ab004", domain.TypeCard, "7777777777777777777ab002", "bob", nil)

	// Bob trashes the column; the cascade takes his cards with it, one batch.
	if err := elements.SoftDelete(ctx,
		[]string{"7777777777777777777ab002", "7777777777777777777ab003", "7777777777777777777ab004"},
		"bob", "7777777777777777777ab002", now); err != nil {
		t.Fatalf("trash: %v", err)
	}

	access := NewAccessResolver(elements)
	svc := NewElementService(elements, access, IDGenerator(func() string { return "gen" }))
	svc.AttachCollector(NewCollector(elements, zap.NewNop()))
	return &trashFixture{elements: elements, svc: svc, acl: acl}
}

func (f *trashFixture) removeAlice(t *testing.T) {
	t.Helper()
	f.acl.Editors = []string{}
	if err := f.elements.SetACL(context.Background(), "7777777777777777777ab001", f.acl); err != nil {
		t.Fatalf("remove editor: %v", err)
	}
}

func TestTrash_ARemovedCollaboratorCannotDestroyWhatTheyOnceCreated(t *testing.T) {
	f := newTrashFixture(t)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}

	// While she is still an editor this is an ordinary, allowed gesture — the
	// guard is about the CURRENT relationship, not about locking creators out.
	if err := f.svc.RestoreFromTrash(ctx, alice, "7777777777777777777ab002"); err != nil {
		t.Fatalf("an editor could not restore what she created: %v", err)
	}
	if err := f.elements.SoftDelete(ctx,
		[]string{"7777777777777777777ab002", "7777777777777777777ab003", "7777777777777777777ab004"},
		"bob", "7777777777777777777ab002", time.Now().UTC()); err != nil {
		t.Fatalf("re-trash: %v", err)
	}

	f.removeAlice(t)

	if err := f.svc.DeletePermanently(ctx, alice, "7777777777777777777ab002"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a removed collaborator destroyed a subtree on a board she cannot open (err = %v)", err)
	}
	for _, id := range []string{"7777777777777777777ab003", "7777777777777777777ab004"} {
		if _, err := f.elements.Get(ctx, id); err != nil {
			t.Errorf("the board owner's card %s was hard-deleted anyway: %v", id, err)
		}
	}
	if err := f.svc.RestoreFromTrash(ctx, alice, "7777777777777777777ab002"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a removed collaborator restored a whole delete batch onto a board she cannot open (err = %v)", err)
	}
}

// Emptying the trash walks the same list, so it must not become the way around
// the check — and it must still empty everything the caller genuinely owns.
func TestTrash_EmptyingSkipsWhatTheCallerMayNoLongerTouch(t *testing.T) {
	f := newTrashFixture(t)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}
	f.removeAlice(t)

	// Alice's own board, her own trashed card: the ordinary case.
	now := time.Now().UTC()
	if err := f.elements.Insert(ctx, &domain.Element{
		ID: "7777777777777777777ac001", Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Alice"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed alice board: %v", err)
	}
	if err := f.elements.Insert(ctx, &domain.Element{
		ID: "7777777777777777777ac002", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: "7777777777777777777ac001"},
		Content:   domain.Content{"textPreview": "hers"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed alice card: %v", err)
	}
	if err := f.elements.SoftDelete(ctx, []string{"7777777777777777777ac002"},
		"alice", "7777777777777777777ac002", now); err != nil {
		t.Fatalf("trash alice card: %v", err)
	}

	n, err := f.svc.EmptyTrash(ctx, alice)
	if err != nil {
		t.Fatalf("empty trash: %v", err)
	}
	if n != 1 {
		t.Fatalf("emptied %d, want 1 — only the card on her own board", n)
	}
	if _, err := f.elements.Get(ctx, "7777777777777777777ab002"); err != nil {
		t.Error("Empty Trash destroyed a column on a board the caller cannot open")
	}
	if _, err := f.elements.Get(ctx, "7777777777777777777ac002"); err == nil {
		t.Error("Empty Trash left the caller's own card behind")
	}
}

func TestCloneInstances_HidesCopiesOnBoardsTheCallerCannotSee(t *testing.T) {
	ctx := context.Background()
	elements := memory.NewElementRepo()
	now := time.Now().UTC()
	mk := func(id string, typ domain.ElementType, parent string, acl *domain.ACL, content domain.Content) {
		if err := elements.Insert(ctx, &domain.Element{
			ID: id, Type: typ,
			Location: domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:  content, ACL: acl,
			CreatedBy: "x", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// Alice's board holds the source card and one visible copy. Bob cloned it
	// onto a private board of his own, whose very name is confidential.
	mk("8888888888888888888ab001", domain.TypeBoard, "",
		&domain.ACL{OwnerID: "alice", Editors: []string{"bob"}}, domain.Content{"title": "Shoot"})
	mk("8888888888888888888ab002", domain.TypeCard, "8888888888888888888ab001", nil,
		domain.Content{"textPreview": "call sheet"})
	mk("8888888888888888888ab003", domain.TypeClone, "8888888888888888888ab001", nil,
		domain.Content{"cloneSourceId": "8888888888888888888ab002"})
	mk("8888888888888888888ac001", domain.TypeBoard, "",
		&domain.ACL{OwnerID: "bob", Editors: []string{}}, domain.Content{"title": "Rival pitch — confidential"})
	mk("8888888888888888888ac002", domain.TypeClone, "8888888888888888888ac001", nil,
		domain.Content{"cloneSourceId": "8888888888888888888ab002"})

	svc := NewElementService(elements, NewAccessResolver(elements), IDGenerator(func() string { return "gen" }))
	got, err := svc.CloneInstances(ctx, &domain.Principal{Sub: "alice"}, "8888888888888888888ab002")
	if err != nil {
		t.Fatalf("clone instances: %v", err)
	}
	for _, entry := range got {
		if entry["parentId"] == "8888888888888888888ac001" {
			t.Fatalf("a copy on a board the caller cannot open was listed: %+v", entry)
		}
		if title, _ := entry["boardTitle"].(string); title == "Rival pitch — confidential" {
			t.Fatalf("the title of a private board leaked through the synced-note footer: %+v", entry)
		}
	}
	if len(got) != 1 {
		t.Fatalf("instances = %d, want 1 — the copy on her own board", len(got))
	}
	// Bob, who can see both, still sees both: the rule is the ACL, not secrecy
	// about one particular board.
	both, err := svc.CloneInstances(ctx, &domain.Principal{Sub: "bob"}, "8888888888888888888ab002")
	if err != nil {
		t.Fatalf("clone instances for bob: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("bob sees %d instances, want 2 — the guard must not hide his own copy", len(both))
	}
}
