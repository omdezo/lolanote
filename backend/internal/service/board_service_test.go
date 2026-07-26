package service

import (
	"context"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The read-path twin of TestApply_IDOR_RejectsCrossBoardOp.
//
// A CLONE names its source by id, and the write path deliberately only checks
// that the CLONE's *parent* is inside the caller's own board. So Bob can put a
// clone on his own board pointing at Alice's private card. Rendering that board
// must not resolve the source into the payload — doing so is a full
// cross-tenant read bypass (GAPS_AUDIT_2026-07 §0).
func TestChildren_ForgedCloneCannotReadAnotherTenant(t *testing.T) {
	_, elements, items := fixture(t)
	ctx := context.Background()
	boards := NewBoardService(elements, nil, NewAccessResolver(elements))

	// Alice's card carries something Bob must never see.
	alicesCard := items["cardA"]
	alicesCard.Content = domain.Content{"textPreview": "SERIES A TERMS — $4M at 20M pre"}
	if _, err := elements.MergePatch(ctx, alicesCard.ID, domain.Content{
		"content": map[string]any{"textPreview": "SERIES A TERMS — $4M at 20M pre"},
	}); err != nil {
		t.Fatalf("seed alice card: %v", err)
	}

	// Bob forges a clone on his own board pointing at it.
	now := time.Now().UTC()
	forged := &domain.Element{
		ID:   "cccccccccccccccccccccc01",
		Type: domain.TypeClone,
		Location: domain.Location{
			ParentID: items["boardB"].ID, Section: domain.SectionCanvas,
		},
		Content:   domain.Content{"cloneSourceId": alicesCard.ID},
		CreatedBy: "bob", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, forged); err != nil {
		t.Fatalf("insert forged clone: %v", err)
	}

	bob := &domain.Principal{Sub: "bob"}
	got, err := boards.Children(ctx, bob, items["boardB"].ID)
	if err != nil {
		t.Fatalf("children: %v", err)
	}

	// The clone itself is Bob's and may render; the SOURCE must not appear.
	for _, el := range got {
		if el.ID == alicesCard.ID {
			t.Fatalf("cross-tenant disclosure: Alice's card %s was returned to Bob", el.ID)
		}
	}

	// And the guard must not be so blunt that it breaks legitimate clones.
	alice := &domain.Principal{Sub: "alice"}
	ownClone := &domain.Element{
		ID:   "cccccccccccccccccccccc02",
		Type: domain.TypeClone,
		Location: domain.Location{
			ParentID: items["boardA"].ID, Section: domain.SectionCanvas,
		},
		Content:   domain.Content{"cloneSourceId": alicesCard.ID},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	// Put the source somewhere else on Alice's own board so it is not already
	// in the direct-children set — this is the case the resolution exists for.
	if err := elements.Insert(ctx, ownClone); err != nil {
		t.Fatalf("insert alice clone: %v", err)
	}
	nested := &domain.Element{
		ID:   "cccccccccccccccccccccc03",
		Type: domain.TypeBoard,
		Location: domain.Location{
			ParentID: items["boardA"].ID, Section: domain.SectionCanvas,
		},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		Content:   domain.Content{"title": "Deal room"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, nested); err != nil {
		t.Fatalf("insert nested board: %v", err)
	}
	deep := &domain.Element{
		ID:   "cccccccccccccccccccccc04",
		Type: domain.TypeCard,
		Location: domain.Location{
			ParentID: nested.ID, Section: domain.SectionCanvas,
		},
		Content:   domain.Content{"textPreview": "own note"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, deep); err != nil {
		t.Fatalf("insert deep card: %v", err)
	}
	deepClone := &domain.Element{
		ID:   "cccccccccccccccccccccc05",
		Type: domain.TypeClone,
		Location: domain.Location{
			ParentID: items["boardA"].ID, Section: domain.SectionCanvas,
		},
		Content:   domain.Content{"cloneSourceId": deep.ID},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := elements.Insert(ctx, deepClone); err != nil {
		t.Fatalf("insert deep clone: %v", err)
	}

	mine, err := boards.Children(ctx, alice, items["boardA"].ID)
	if err != nil {
		t.Fatalf("children for owner: %v", err)
	}
	var resolved bool
	for _, el := range mine {
		if el.ID == deep.ID {
			resolved = true
		}
	}
	if !resolved {
		t.Fatal("owner's own clone source was dropped; the guard is too blunt")
	}
}

var _ = memory.NewElementRepo
