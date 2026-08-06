package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// A read-only link holder could read the board's own EDIT token.
//
// ACL.PublicEditLink and ViewLink.Token were ordinary json fields on Element,
// and GET /boards/:id, /children, /unsorted and /export?format=json all sit in
// the optional-auth group — so an anonymous caller holding a view token got the
// edit token, every nested board's tokens, and every collaborator's subject id
// back in the same response. No exploit: `?format=json`.
//
// These assert on the MARSHALLED bytes rather than on the struct, because the
// whole defect lived in what serialization emitted. A field withheld from the
// struct but present in the json is exactly the shape that shipped.

const (
	editToken = "e0edit0token0000000000000000000000000000000000000"
	viewToken = "f0view0token0000000000000000000000000000000000000"
	nestToken = "a0nest0token0000000000000000000000000000000000000"
)

// aclFixture builds Alice's board (shared with Bob, both links live), a nested
// sub-board with its own links, and one card.
func aclFixture(t *testing.T) (*memory.ElementRepo, *AccessResolver) {
	t.Helper()
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id, typ, parent string, acl *domain.ACL, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			ACL:       acl,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	mk("dddddddddddddddddddddd01", "BOARD", "", &domain.ACL{
		OwnerID:        "alice",
		Editors:        []string{"bob"},
		PublicEditLink: editToken,
		ViewLink:       &domain.ViewLink{Token: viewToken},
	}, domain.Content{"title": "Pitch"})
	mk("dddddddddddddddddddddd02", "CARD", "dddddddddddddddddddddd01", nil,
		domain.Content{"textPreview": "the pitch"})
	mk("dddddddddddddddddddddd03", "BOARD", "dddddddddddddddddddddd01", &domain.ACL{
		OwnerID:        "alice",
		Editors:        []string{"bob"},
		PublicEditLink: nestToken,
	}, domain.Content{"title": "Budget"})
	mk("dddddddddddddddddddddd04", "CARD", "dddddddddddddddddddddd01", nil,
		domain.Content{"textPreview": "tray note"})
	if _, err := elements.MergePatch(ctx, "dddddddddddddddddddddd04", domain.Content{
		"location": map[string]any{"section": string(domain.SectionUnsorted)},
	}); err != nil {
		t.Fatalf("tray note: %v", err)
	}
	return elements, NewAccessResolver(elements)
}

// leaks reports every secret string that survived into the payload.
func leaks(t *testing.T, payload any) []string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var found []string
	for name, secret := range map[string]string{
		"the edit token":       editToken,
		"the view token":       viewToken,
		"a sub-board's token":  nestToken,
		"a collaborator's sub": "bob",
	} {
		if strings.Contains(string(raw), secret) {
			found = append(found, name)
		}
	}
	return found
}

func TestACL_ViewLinkHolderNeverSeesATokenOrAMemberList(t *testing.T) {
	elements, access := aclFixture(t)
	boards := NewBoardService(elements, nil, access)
	items := NewElementService(elements, access, func() string { return "eeeeeeeeeeeeeeeeeeeeee01" })
	ctx := context.Background()

	// Anonymous: no account at all, just the read-only link.
	viewer := &domain.Principal{ShareToken: viewToken}

	t.Run("GET /boards/:id", func(t *testing.T) {
		view, err := boards.Get(ctx, viewer, "dddddddddddddddddddddd01")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if view.Role != "view" {
			t.Fatalf("role = %q, want view — the fixture is not exercising the link path", view.Role)
		}
		if got := leaks(t, view); len(got) > 0 {
			t.Errorf("the board read disclosed %v", got)
		}
		if view.Board.ACL == nil || view.Board.ACL.OwnerID != "alice" {
			t.Error("the owner id was dropped; the drawer renders from it")
		}
	})

	t.Run("GET /boards/:id/children", func(t *testing.T) {
		kids, err := boards.Children(ctx, viewer, "dddddddddddddddddddddd01")
		if err != nil {
			t.Fatalf("children: %v", err)
		}
		if got := leaks(t, kids); len(got) > 0 {
			t.Errorf("the children payload disclosed %v", got)
		}
	})

	t.Run("GET /boards/:id/unsorted", func(t *testing.T) {
		tray, err := boards.Unsorted(ctx, viewer, "dddddddddddddddddddddd01")
		if err != nil {
			t.Fatalf("unsorted: %v", err)
		}
		if got := leaks(t, tray); len(got) > 0 {
			t.Errorf("the tray payload disclosed %v", got)
		}
	})

	t.Run("GET /boards/:id/export?format=json", func(t *testing.T) {
		body, _, err := boards.Export(ctx, viewer, "dddddddddddddddddddddd01", "json")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		for name, secret := range map[string]string{
			"the edit token":       editToken,
			"the view token":       viewToken,
			"a sub-board's token":  nestToken,
			"a collaborator's sub": "bob",
		} {
			if strings.Contains(body, secret) {
				t.Errorf("the json export disclosed %s", name)
			}
		}
	})

	t.Run("GET /elements/:id", func(t *testing.T) {
		el, err := items.Get(ctx, viewer, "dddddddddddddddddddddd03")
		if err != nil {
			t.Fatalf("element get: %v", err)
		}
		if got := leaks(t, el); len(got) > 0 {
			t.Errorf("the element read disclosed %v", got)
		}
	})
}

// Closing the hole must not close the feature: the owner still gets the member
// list on the board read, and the Share dialog is still the one place the tokens
// live.
func TestACL_OwnerKeepsTheMemberListAndTheShareDialogKeepsTheTokens(t *testing.T) {
	elements, access := aclFixture(t)
	boards := NewBoardService(elements, nil, access)
	audit, _ := testAudit()
	share := NewShareService(elements, nil, nil, access, audit)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}

	view, err := boards.Get(ctx, alice, "dddddddddddddddddddddd01")
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if view.Board.ACL == nil || len(view.Board.ACL.Editors) != 1 || view.Board.ACL.Editors[0] != "bob" {
		t.Errorf("the owner lost the member list: %+v", view.Board.ACL)
	}
	if raw, _ := json.Marshal(view); strings.Contains(string(raw), editToken) {
		t.Error("the board read still carries the edit token; ShareState is the only door")
	}

	state, err := share.State(ctx, alice, "dddddddddddddddddddddd01")
	if err != nil {
		t.Fatalf("share state: %v", err)
	}
	if state.PublicEditLink != editToken {
		t.Errorf("the Share dialog lost the edit link: %q", state.PublicEditLink)
	}
	if state.ViewLink == nil || state.ViewLink.Token != viewToken {
		t.Errorf("the Share dialog lost the view link: %+v", state.ViewLink)
	}
}

// An editor sits between the two: they need the member list for the assignee
// picker and have no business holding the link that mints more editors.
func TestACL_EditorGetsTheMemberListAndStillNoTokens(t *testing.T) {
	elements, access := aclFixture(t)
	boards := NewBoardService(elements, nil, access)
	ctx := context.Background()

	view, err := boards.Get(ctx, &domain.Principal{Sub: "bob"}, "dddddddddddddddddddddd01")
	if err != nil {
		t.Fatalf("editor read: %v", err)
	}
	if view.Board.ACL == nil || len(view.Board.ACL.Editors) != 1 {
		t.Errorf("the assignee picker lost its people: %+v", view.Board.ACL)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), editToken) || strings.Contains(string(raw), viewToken) {
		t.Error("an editor was handed a share token")
	}
}
