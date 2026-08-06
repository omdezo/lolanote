package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// SEC2 / SEC3 — a share link's CONDITIONS were not part of the link.
//
// The sharing dialog offers two: a password, and "requires a QomraNote account".
// Both were stored on the ViewLink and both round-tripped back to the dialog, so
// the owner had every reason to believe they were switches. Neither was.
//
// The password was compared in exactly ONE place — the GET /shared/:token
// resolver a browser happens to call first — and never in the ACL resolver every
// other route goes through. So the password gated the doormat and nothing behind
// it: with the token alone, /boards/:id, /children, /unsorted and, worst,
// /export?format=json all answered. And because regenerating a view link
// deliberately keeps the token stable so the URL does not change, CHANGING the
// password revoked nothing from anyone who had opened the board once.
//
// requireAccount was worse: a grep across the whole repository found it written
// by the dialog, stored, serialized back — and read by no code at all.
//
// Each of these tests fails without the fix, at the resolver, which is the layer
// every route shares.

type sharedBoardFixture struct {
	elements *memory.ElementRepo
	access   *AccessResolver
	share    *ShareService
	boardID  string
	cardID   string
	acl      *domain.ACL
}

func sharedBoard(t *testing.T, vl *domain.ViewLink) *sharedBoardFixture {
	t.Helper()
	ctx := context.Background()
	elements := memory.NewElementRepo()
	access := NewAccessResolver(elements)
	now := time.Now().UTC()

	acl := &domain.ACL{OwnerID: "alice", Editors: []string{}, ViewLink: vl}
	if err := elements.Insert(ctx, &domain.Element{
		ID: "5555555555555555555ab001", Type: domain.TypeBoard,
		Content: domain.Content{"title": "Client cut"}, ACL: acl,
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if err := elements.Insert(ctx, &domain.Element{
		ID: "5555555555555555555ab002", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: "5555555555555555555ab001", Section: domain.SectionCanvas},
		Content:   domain.Content{"textPreview": "the unreleased edit"},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	audit, _ := testAudit()
	return &sharedBoardFixture{
		elements: elements, access: access,
		share:   NewShareService(elements, nil, nil, access, audit),
		boardID: "5555555555555555555ab001", cardID: "5555555555555555555ab002",
		acl: acl,
	}
}

func hashOf(t *testing.T, password string) string {
	t.Helper()
	// The minimum cost keeps the suite fast; the code under test never looks at
	// the cost, only at whether the comparison succeeds.
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return string(h)
}

func TestViewLink_PasswordGuardsEveryRouteNotJustTheEntryPoint(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view", PasswordHash: hashOf(t, "s3cret")})
	ctx := context.Background()

	// The shape of the attack: the visitor has the URL (so they have the token)
	// and does not have the password. They skip /shared/:token entirely.
	withoutPassword := &domain.Principal{ShareToken: "tok-view"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, withoutPassword); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a password-protected board opened with the bare token (err = %v)", err)
	}
	// The nested read is the one that hurts: it is what /children and
	// /export?format=json are built on.
	if _, _, err := f.access.RequireView(ctx, f.cardID, withoutPassword); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("the board's contents were readable with the bare token (err = %v)", err)
	}

	// With the password, everything works exactly as before.
	withPassword := &domain.Principal{ShareToken: "tok-view", SharePassword: "s3cret"}
	role, _, err := f.access.RequireView(ctx, f.cardID, withPassword)
	if err != nil {
		t.Fatalf("the right password was refused: %v", err)
	}
	if role != RoleView {
		t.Fatalf("role = %v, want view", role)
	}

	// A wrong password is not "no password".
	wrong := &domain.Principal{ShareToken: "tok-view", SharePassword: "not it"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, wrong); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a wrong password was accepted (err = %v)", err)
	}
}

// The token stays stable across a password change on purpose — the URL people
// already have must keep working. That is exactly why the password has to be
// checked per request: otherwise "I changed the password" changes nothing for
// the person you changed it because of.
func TestViewLink_ChangingThePasswordRevokesTheOldOne(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view", PasswordHash: hashOf(t, "old-pass")})
	ctx := context.Background()

	visitor := &domain.Principal{ShareToken: "tok-view", SharePassword: "old-pass"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, visitor); err != nil {
		t.Fatalf("setup: the original password did not work: %v", err)
	}

	f.acl.ViewLink.PasswordHash = hashOf(t, "new-pass")
	if err := f.elements.SetACL(ctx, f.boardID, f.acl); err != nil {
		t.Fatalf("change password: %v", err)
	}

	if _, _, err := f.access.RequireView(ctx, f.boardID, visitor); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("the old password still opens the board (err = %v)", err)
	}
	moved := &domain.Principal{ShareToken: "tok-view", SharePassword: "new-pass"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, moved); err != nil {
		t.Fatalf("the new password was refused: %v", err)
	}
}

func TestViewLink_RequireAccountRefusesAnonymousCallers(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view", RequireAccount: true})
	ctx := context.Background()

	anon := &domain.Principal{ShareToken: "tok-view"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, anon); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a link marked account-only opened for an anonymous caller (err = %v)", err)
	}
	if _, _, err := f.share.ResolveToken(ctx, anon, "tok-view"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("the entry point resolved an account-only link anonymously (err = %v)", err)
	}

	signedIn := &domain.Principal{Sub: "carol", ShareToken: "tok-view"}
	if _, _, err := f.access.RequireView(ctx, f.boardID, signedIn); err != nil {
		t.Fatalf("a signed-in visitor was refused an account-only link: %v", err)
	}
	if _, kind, err := f.share.ResolveToken(ctx, signedIn, "tok-view"); err != nil || kind != "view" {
		t.Fatalf("entry point for a signed-in visitor: kind=%q err=%v", kind, err)
	}
}

// A link with neither condition is unchanged: this is the ordinary case, and the
// account-free view link is a product feature (§6.1 mechanism 4), not an
// oversight.
func TestViewLink_AnUnconditionalLinkStillOpensForAnyone(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view"})
	ctx := context.Background()

	anon := &domain.Principal{ShareToken: "tok-view"}
	if _, _, err := f.access.RequireView(ctx, f.cardID, anon); err != nil {
		t.Fatalf("a plain view link stopped working: %v", err)
	}
	if _, kind, err := f.share.ResolveToken(ctx, anon, "tok-view"); err != nil || kind != "view" {
		t.Fatalf("entry point: kind=%q err=%v", kind, err)
	}
}

// bcrypt at the default cost is ~100ms by design, and the password question is
// now asked on every read of a shared board. Without memoisation that is a board
// load measured in seconds and a CPU amplifier for anyone holding the link.
func TestViewLink_PasswordIsNotRehashedOnEveryRead(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view", PasswordHash: hashOf(t, "s3cret")})
	ctx := context.Background()
	visitor := &domain.Principal{ShareToken: "tok-view", SharePassword: "s3cret"}

	if _, _, err := f.access.RequireView(ctx, f.boardID, visitor); err != nil {
		t.Fatalf("setup: %v", err)
	}
	before := len(f.access.unlocks.ok)
	for i := 0; i < 50; i++ {
		if _, _, err := f.access.RequireView(ctx, f.cardID, visitor); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if got := len(f.access.unlocks.ok); got != before || got != 1 {
		t.Fatalf("verified-password entries = %d (was %d); one link and one password is one entry", got, before)
	}
	// A wrong password must never be remembered, or the memo becomes the bypass.
	wrong := &domain.Principal{ShareToken: "tok-view", SharePassword: "guess"}
	_, _, _ = f.access.RequireView(ctx, f.boardID, wrong)
	if got := len(f.access.unlocks.ok); got != 1 {
		t.Fatalf("a failed password was recorded: entries = %d", got)
	}
}

// A resolver built by struct literal rather than by its constructor has no memo.
// It must refuse rather than wave the password through — the failure mode of a
// half-built dependency has to be "no access", never "all access".
func TestViewLink_AResolverWithNoPasswordVerifierFailsClosed(t *testing.T) {
	f := sharedBoard(t, &domain.ViewLink{Token: "tok-view", PasswordHash: hashOf(t, "s3cret")})
	bare := &AccessResolver{elements: f.elements}

	_, _, err := bare.RequireView(context.Background(), f.boardID,
		&domain.Principal{ShareToken: "tok-view", SharePassword: "s3cret"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a resolver that cannot check passwords answered yes (err = %v)", err)
	}
}
