package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The delegation's content guard was a two-item denylist over a schemaless map,
// and the roadmap kept proving it the wrong shape: content.locked was not on it
// (a lock the write path would then not enforce), content.cloneSourceId was not
// on it (the field the cross-tenant clone escalation turned on), and
// content.agentInstructions was not on it — the standing rules that CONSTRAIN
// the agent live in freely-patchable content, so the only thing stopping a run
// from rewriting its own instructions was that no tool happened to emit that key.
func TestDelegation_PrivilegedContentKeysAreRefused(t *testing.T) {
	elements := memory.NewElementRepo()
	ctx := context.Background()
	seedOwnedBoard(t, elements, "3333333333333333333ab001")
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: "3333333333333333333ab002", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: "3333333333333333333ab001"},
		Content:   domain.Content{"textPreview": "a note"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	svc, _ := partialWriteFixture(t, elements)

	// The grant now carries the compiler's own answer to "what does this action
	// kind write". Stated by hand here rather than imported from the planner,
	// because this package is BELOW the planner and the guard has to be provable
	// from the grant alone; that the allowance matches what the compiler emits
	// is agent/contentkeys_test.go's job.
	agentPrincipal := func() *domain.Principal {
		return &domain.Principal{Sub: "alice", Delegation: &domain.Delegation{
			RunID: "r1", OnBehalfOf: "alice", RootBoardID: "3333333333333333333ab001",
			Capabilities: []domain.Capability{
				domain.CapElementCreate, domain.CapElementUpdate, domain.CapElementMove,
			},
			Consequence: domain.ConsequenceReversibleWrite,
			MaxOps:      10,
			ContentKeys: map[string][]string{
				"set_text":  {"doc", "searchText", "textPreview"},
				"duplicate": {domain.ContentKeysVerbatim},
			},
			ExpiresAt: now.Add(30 * time.Minute),
		}}
	}

	for _, key := range []string{"isHome", "isTemplate", "locked", "cloneSourceId", "agentInstructions"} {
		t.Run(key, func(t *testing.T) {
			_, err := svc.ApplyWithMeta(ctx, agentPrincipal(), "3333333333333333333ab001", "", []domain.Op{{
				ElementID: "3333333333333333333ab002", Action: domain.ActionUpdate, Kind: "set_text",
				Changes: domain.Content{"content": map[string]any{key: true}},
			}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r1"})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("an agent op writing content.%s was answered with %v", key, err)
			}
		})
		// Same key, arriving through the one kind whose content cannot be
		// enumerated. A copy carries its source verbatim, so the allowlist has
		// nothing to check it against and the denylist is what is left.
		t.Run(key+"/verbatim", func(t *testing.T) {
			_, err := svc.ApplyWithMeta(ctx, agentPrincipal(), "3333333333333333333ab001", "", []domain.Op{{
				ElementID: "3333333333333333333ab002", Action: domain.ActionUpdate, Kind: "duplicate",
				Changes: domain.Content{"content": map[string]any{key: true}},
			}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r1"})
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("a copy carrying content.%s was answered with %v", key, err)
			}
		})
	}

	// The polarity itself. An op whose kind the grant does not name is refused
	// even though the KEY it writes is perfectly ordinary — because "we were
	// never told what this writes" and "this writes nothing dangerous" are
	// different answers, and treating the first as the second is exactly how the
	// old denylist accumulated its exceptions.
	for _, kind := range []string{"", "some_future_capability"} {
		_, err := svc.ApplyWithMeta(ctx, agentPrincipal(), "3333333333333333333ab001", "", []domain.Op{{
			ElementID: "3333333333333333333ab002", Action: domain.ActionUpdate, Kind: kind,
			Changes: domain.Content{"content": map[string]any{"textPreview": "edited"}},
		}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r1"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("an op of undeclared kind %q was answered with %v; undeclared must refuse", kind, err)
		}
	}

	// The ordinary case still works, or the guard is a wall.
	if _, err := svc.ApplyWithMeta(ctx, agentPrincipal(), "3333333333333333333ab001", "", []domain.Op{{
		ElementID: "3333333333333333333ab002", Action: domain.ActionUpdate, Kind: "set_text",
		Changes:     domain.Content{"content": map[string]any{"textPreview": "edited"}},
		UndoChanges: domain.Content{"content": map[string]any{"textPreview": "a note"}},
	}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r1"}); err != nil {
		t.Fatalf("an ordinary agent edit was refused: %v", err)
	}
}

// The human half of the same hole: content is schemaless, so a TABLE with no
// cells and a LINE with no endpoints were created without complaint and then
// rendered as an empty box or as nothing at all. Silent acceptance is the
// failure shape this codebase has shipped five times.
func TestApplyCreate_RefusesAnElementThatCannotRender(t *testing.T) {
	elements := memory.NewElementRepo()
	seedOwnedBoard(t, elements, "4444444444444444444ab001")
	svc, _ := partialWriteFixture(t, elements)
	ctx := context.Background()
	alice := &domain.Principal{Sub: "alice"}

	cases := []struct {
		name    string
		id      string
		typ     domain.ElementType
		content map[string]any
		ok      bool
	}{
		{"a table with no cells", "4444444444444444444ab010", domain.TypeTable, map[string]any{"title": "Budget"}, false},
		{"a table with cells", "4444444444444444444ab011", domain.TypeTable, map[string]any{"cells": []any{}}, true},
		{"a line with one end", "4444444444444444444ab012", domain.TypeLine, map[string]any{"fromId": "x"}, false},
		{"a line with two ends", "4444444444444444444ab013", domain.TypeLine, map[string]any{"fromId": "x", "toId": "y"}, true},
		{"a swatch with no colour", "4444444444444444444ab014", domain.TypeColorSwatch, map[string]any{}, false},
		{"a swatch with a colour", "4444444444444444444ab015", domain.TypeColorSwatch, map[string]any{"hex": "#ff0000"}, true},
		{"a link with an empty url", "4444444444444444444ab016", domain.TypeLink, map[string]any{"url": ""}, false},
		{"an ordinary card", "4444444444444444444ab017", domain.TypeCard, map[string]any{"textPreview": "hi"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Apply(ctx, alice, "4444444444444444444ab001", "c1", []domain.Op{{
				ElementID: tc.id, Action: domain.ActionCreate,
				Changes: domain.Content{
					"type":     string(tc.typ),
					"location": map[string]any{"parentId": "4444444444444444444ab001"},
					"content":  tc.content,
				},
			}})
			if tc.ok {
				if err != nil {
					t.Fatalf("a well-formed %s was refused: %v", tc.typ, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("a %s that cannot render was accepted (%v) — it would appear on the board as nothing", tc.typ, err)
			}
			if _, gerr := elements.Get(ctx, tc.id); gerr == nil {
				t.Errorf("the malformed %s reached the board anyway", tc.typ)
			}
		})
	}
}

// spyEvictor records what a revocation asked to disconnect.
type spyEvictor struct {
	subs   []string
	tokens []string
}

func (s *spyEvictor) Evict(_, sub string)               { s.subs = append(s.subs, sub) }
func (s *spyEvictor) EvictByShareToken(_, token string) { s.tokens = append(s.tokens, token) }

// "Remove" in a sharing dialog means now. Authorization for a socket is checked
// exactly once, at handshake, and room membership is never re-resolved — so a
// removed collaborator kept receiving the full text of every card anyone edited
// on the board they had just been cut off from, for the remaining lifetime of a
// token nobody could see.
func TestShare_RevocationDisconnectsTheSessionItRevoked(t *testing.T) {
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := elements.Insert(ctx, &domain.Element{
		ID: "5555555555555555555ab001", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Production"},
		ACL: &domain.ACL{
			OwnerID: "alice", Editors: []string{"bob"},
			PublicEditLink: "edit-token-abc",
			ViewLink:       &domain.ViewLink{Token: "view-token-xyz"},
		},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	access := NewAccessResolver(elements)
	spy := &spyEvictor{}
	audit, _ := testAudit()
	share := NewShareService(elements, nil, nil, access, audit)
	share.AttachEvictor(spy)
	alice := &domain.Principal{Sub: "alice"}

	if _, err := share.RemoveEditor(ctx, alice, "5555555555555555555ab001", "bob"); err != nil {
		t.Fatalf("remove editor: %v", err)
	}
	if len(spy.subs) != 1 || spy.subs[0] != "bob" {
		t.Errorf("removing an editor evicted %v; the stream keeps flowing to a revoked session", spy.subs)
	}

	if _, err := share.RevokeLink(ctx, alice, "5555555555555555555ab001", "view"); err != nil {
		t.Fatalf("revoke view link: %v", err)
	}
	if _, err := share.RevokeLink(ctx, alice, "5555555555555555555ab001", "edit"); err != nil {
		t.Fatalf("revoke edit link: %v", err)
	}
	if len(spy.tokens) != 2 {
		t.Fatalf("revoked links evicted %v; a token cleared from the ACL is still live on its sockets", spy.tokens)
	}
	// The token has to be read BEFORE it is cleared, or there is no handle left.
	for _, want := range []string{"view-token-xyz", "edit-token-abc"} {
		found := false
		for _, got := range spy.tokens {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("eviction did not name %s — it was cleared before it was captured", want)
		}
	}
}
