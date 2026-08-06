package agent

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
)

// DA7 — sharing cascades downward and the agent's people list did not.
//
// `attachPeople` read `scope.Board.ACL` and stopped, while AccessResolver.Resolve
// walks the containment chain and takes the MAX role across every ancestor. And
// `applyCreate` stamps every new BOARD with `ACL{OwnerID: creator, Editors: []}`
// — so the broken case is the NORMAL one: any sub-board, including the ones the
// agent creates itself during the organizing run this list exists to serve.
//
// The sharp end is not the missing feature. `assign` rejected every real
// teammate with "X is not one of this board's people", and an unrecognised id
// counts toward the injection tally — so asking the agent to give a colleague a
// task looked like an attempted prompt injection and pushed a legitimate run
// toward quarantine.

type peopleFixture struct {
	elements *memory.ElementRepo
	svc      *Service
}

// stubUsers resolves a sub to a display name, and knows nobody else.
type stubUsers map[string]string

func (u stubUsers) GetBySub(_ context.Context, sub string) (*domain.User, error) {
	name, ok := u[sub]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &domain.User{KeycloakSub: sub, DisplayName: name}, nil
}
func (u stubUsers) GetByEmail(context.Context, string) (*domain.User, error) {
	return nil, domain.ErrNotFound
}
func (u stubUsers) Insert(context.Context, *domain.User) error { return nil }
func (u stubUsers) Update(context.Context, *domain.User) error { return nil }
func (u stubUsers) UpdateSettings(context.Context, string, *domain.UserSettings) error {
	return nil
}
func (u stubUsers) Delete(context.Context, string) error { return nil }

func newPeopleFixture(t *testing.T) *peopleFixture {
	t.Helper()
	elements := memory.NewElementRepo()
	return &peopleFixture{
		elements: elements,
		svc: NewService(Config{
			Elements: elements, Access: service.NewAccessResolver(elements),
			Users: stubUsers{"alice": "Alice", "sara": "Sara", "omar": "Omar", "stranger": "Stranger"},
			Log:   zap.NewNop(),
		}),
	}
}

func (f *peopleFixture) board(t *testing.T, id, parent string, acl *domain.ACL) *domain.Element {
	t.Helper()
	now := time.Now().UTC()
	el := &domain.Element{
		ID: id, Type: domain.TypeBoard,
		Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
		Content:   domain.Content{"title": id},
		ACL:       acl,
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}
	if err := f.elements.Insert(context.Background(), el); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return el
}

func namesIn(scope *BoardScope) map[string]bool {
	out := map[string]bool{}
	for _, p := range scope.People {
		out[p.Name] = true
	}
	return out
}

func TestPeople_ASubBoardInheritsTheBoardsCollaborators(t *testing.T) {
	f := newPeopleFixture(t)
	// The shared production board, and the sub-board one organizing run made
	// inside it — stamped, as applyCreate stamps every board, with its creator
	// and an EMPTY editor list.
	f.board(t, "bbbbbbbbbbbbbbbbbbbbbb01", "",
		&domain.ACL{OwnerID: "alice", Editors: []string{"sara", "omar"}})
	sub := f.board(t, "bbbbbbbbbbbbbbbbbbbbbb02", "bbbbbbbbbbbbbbbbbbbbbb01",
		&domain.ACL{OwnerID: "alice", Editors: []string{}})

	scope := &BoardScope{Board: sub}
	f.svc.attachPeople(context.Background(), &domain.Principal{Sub: "alice"}, scope)

	got := namesIn(scope)
	for _, want := range []string{"Alice", "Sara", "Omar"} {
		if !got[want] {
			t.Errorf("%s can edit this sub-board and is not in its people list: %v", want, got)
		}
	}
	// The board's own owner stays person1 — the aliases are published to the
	// model and must not reshuffle because an ancestor gained an editor.
	if len(scope.People) == 0 || scope.People[0].Name != "Alice" {
		t.Fatalf("the board's own owner is not person1: %+v", scope.People)
	}
}

func TestPeople_NobodyIsNamedTwice(t *testing.T) {
	f := newPeopleFixture(t)
	f.board(t, "bbbbbbbbbbbbbbbbbbbbbb01", "",
		&domain.ACL{OwnerID: "alice", Editors: []string{"sara"}})
	sub := f.board(t, "bbbbbbbbbbbbbbbbbbbbbb02", "bbbbbbbbbbbbbbbbbbbbbb01",
		&domain.ACL{OwnerID: "alice", Editors: []string{"sara"}})

	scope := &BoardScope{Board: sub}
	f.svc.attachPeople(context.Background(), &domain.Principal{Sub: "alice"}, scope)

	if len(scope.People) != 2 {
		t.Fatalf("expected Alice and Sara once each, got %+v", scope.People)
	}
	// Aliases are the handles the model uses for a whole run; two rows carrying
	// the same person would make "assign it to person3" ambiguous.
	seen := map[string]bool{}
	for _, p := range scope.People {
		if seen[p.Alias] {
			t.Fatalf("duplicate alias %q", p.Alias)
		}
		seen[p.Alias] = true
	}
}

func TestPeople_ABoardWithNoAclOfItsOwnStillHasPeople(t *testing.T) {
	f := newPeopleFixture(t)
	f.board(t, "bbbbbbbbbbbbbbbbbbbbbb01", "",
		&domain.ACL{OwnerID: "alice", Editors: []string{"sara"}})
	// A nested board with no ACL document at all. The old guard returned early
	// on exactly this, so the list was not merely short — it was empty, and
	// every name the person typed became an unrecognised id.
	sub := f.board(t, "bbbbbbbbbbbbbbbbbbbbbb02", "bbbbbbbbbbbbbbbbbbbbbb01", nil)

	scope := &BoardScope{Board: sub}
	f.svc.attachPeople(context.Background(), &domain.Principal{Sub: "alice"}, scope)

	if got := namesIn(scope); !got["Alice"] || !got["Sara"] {
		t.Fatalf("a board with no ACL of its own listed nobody: %v", got)
	}
}

func TestPeople_StopsAtABoardTheRunMayNotRead(t *testing.T) {
	f := newPeopleFixture(t)
	// A private workspace above the shared board. Its collaborators are not
	// this run's to name — the same boundary attachAncestry stops at, for the
	// same reason: publishing them leaks the membership of a board the run
	// cannot open.
	f.board(t, "bbbbbbbbbbbbbbbbbbbbbb00", "",
		&domain.ACL{OwnerID: "stranger", Editors: []string{"stranger"}})
	f.board(t, "bbbbbbbbbbbbbbbbbbbbbb01", "bbbbbbbbbbbbbbbbbbbbbb00",
		&domain.ACL{OwnerID: "alice", Editors: []string{"sara"}})
	sub := f.board(t, "bbbbbbbbbbbbbbbbbbbbbb02", "bbbbbbbbbbbbbbbbbbbbbb01",
		&domain.ACL{OwnerID: "alice", Editors: []string{}})

	scope := &BoardScope{Board: sub}
	f.svc.attachPeople(context.Background(), &domain.Principal{Sub: "alice"}, scope)

	if namesIn(scope)["Stranger"] {
		t.Fatalf("named somebody from a board above the share boundary: %+v", scope.People)
	}
}
