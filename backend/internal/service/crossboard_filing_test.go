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

// Cross-board filing was structurally impossible, and a whole subsystem existed
// to do it: filing.go, Delegation.DestinationBoardIDs, authorizeDestinations,
// the discovered set, maxDestinationsPerRun and the file_to verb. The tool
// staged happily, the review showed the destination, and the commit answered
// forbidden for every destination outside the run's root — which is every case
// the feature exists for.
//
// Two gates designed as AND. verifyDelegation had been widened to Roots(); it
// simply never ran, because verifyOpScope went first and hardcoded the run's
// root as the IDOR boundary. The delegation gate's widening was dead code, and
// nothing tested the two layers TOGETHER — the only coverage was domain-level,
// exercising Roots() without ever reaching a commit.
func filingFixture(t *testing.T) (*TransactionService, *memory.ElementRepo, context.Context) {
	t.Helper()
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()

	mk := func(id, typ, parent string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: "alice", Editors: []string{}}
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	// The run's root, with a loose card on it; a second board Alice owns and
	// which an approved plan named as a destination; and a third she owns but
	// which no plan named.
	mk("f000000000000000000ba001", "BOARD", "", domain.Content{"title": "Root"})
	mk("f000000000000000000ba002", "CARD", "f000000000000000000ba001", domain.Content{"textPreview": "Drone permit"})
	mk("f000000000000000000bb001", "BOARD", "", domain.Content{"title": "Permits"})
	mk("f000000000000000000bc001", "BOARD", "", domain.Content{"title": "Unnamed"})

	svc, _ := partialWriteFixture(t, elements)
	return svc, elements, ctx
}

func filingPrincipal(destinations ...string) *domain.Principal {
	return &domain.Principal{Sub: "alice", Delegation: &domain.Delegation{
		RunID: "r-filing", OnBehalfOf: "alice",
		RootBoardID:         "f000000000000000000ba001",
		DestinationBoardIDs: destinations,
		Capabilities: []domain.Capability{
			domain.CapElementCreate, domain.CapElementUpdate, domain.CapElementMove,
		},
		Consequence: domain.ConsequenceReversibleWrite,
		MaxOps:      10,
		ExpiresAt:   time.Now().UTC().Add(30 * time.Minute),
	}}
}

func TestFiling_ACardMovesIntoADestinationAnApprovedPlanNamed(t *testing.T) {
	svc, elements, ctx := filingFixture(t)

	_, err := svc.ApplyWithMeta(ctx, filingPrincipal("f000000000000000000bb001"),
		"f000000000000000000ba001", "", []domain.Op{{
			ElementID: "f000000000000000000ba002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "f000000000000000000bb001", "section": "CANVAS",
			}},
		}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r-filing"})
	if err != nil {
		t.Fatalf("filing into a granted destination was refused: %v", err)
	}

	el, gerr := elements.Get(ctx, "f000000000000000000ba002")
	if gerr != nil {
		t.Fatalf("read back: %v", gerr)
	}
	if el.Location.ParentID != "f000000000000000000bb001" {
		t.Fatalf("the card is still on %s — the move did not land", el.Location.ParentID)
	}
}

// The widening must stop exactly at what the plan named. A destination the
// human never approved is outside the grant even though Alice owns that board:
// the agent's authority is the grant, never the person's full reach.
func TestFiling_ADestinationNoPlanNamedIsStillRefused(t *testing.T) {
	svc, elements, ctx := filingFixture(t)

	_, err := svc.ApplyWithMeta(ctx, filingPrincipal("f000000000000000000bb001"),
		"f000000000000000000ba001", "", []domain.Op{{
			ElementID: "f000000000000000000ba002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "f000000000000000000bc001", "section": "CANVAS",
			}},
		}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r-filing"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a board no plan named accepted the move: %v", err)
	}

	el, _ := elements.Get(ctx, "f000000000000000000ba002")
	if el.Location.ParentID != "f000000000000000000ba001" {
		t.Error("the card moved anyway")
	}
}

// A grant with no destinations is the ordinary run, and it must behave exactly
// as it did before: the root board and nothing else.
func TestFiling_AGrantWithNoDestinationsIsUnchanged(t *testing.T) {
	svc, _, ctx := filingFixture(t)

	_, err := svc.ApplyWithMeta(ctx, filingPrincipal(),
		"f000000000000000000ba001", "", []domain.Op{{
			ElementID: "f000000000000000000ba002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "f000000000000000000bb001", "section": "CANVAS",
			}},
		}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r-filing"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an ungranted run reached another board: %v", err)
	}
}

// And the human path is untouched: for a principal with no delegation the root
// set is exactly the declared board, so the IDOR guard is what it always was.
func TestFiling_AHumanIsStillBoundedByTheDeclaredBoard(t *testing.T) {
	svc, _, ctx := filingFixture(t)

	_, err := svc.Apply(ctx, &domain.Principal{Sub: "alice"},
		"f000000000000000000ba001", "", []domain.Op{{
			ElementID: "f000000000000000000ba002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "f000000000000000000bb001", "section": "CANVAS",
			}},
		}})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a human moved a card to a board they did not declare: %v", err)
	}
}

// recordingBroadcaster remembers which rooms a transaction reached.
type recordingBroadcaster struct{ rooms []string }

func (r *recordingBroadcaster) BroadcastTransaction(boardID string, _ *domain.Transaction) {
	r.rooms = append(r.rooms, boardID)
}

// A card filed from A into B produces a journal entry stamped A. Before the
// destination fan-out, B's room heard nothing: anyone looking at the board the
// card ARRIVED on saw it only if they reloaded. The human path had solved this
// in MoveAcrossBoards; the agent path had no equivalent because until the IDOR
// boundary widened, a transaction could only ever touch one board.
func TestFiling_TheDestinationRoomHearsAboutIt(t *testing.T) {
	elements := memory.NewElementRepo()
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, typ, parent string, content domain.Content) {
		el := &domain.Element{
			ID: id, Type: domain.ElementType(typ),
			Location:  domain.Location{ParentID: parent, Section: domain.SectionCanvas},
			Content:   content,
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}
		if typ == "BOARD" {
			el.ACL = &domain.ACL{OwnerID: "alice", Editors: []string{}}
		}
		if err := elements.Insert(ctx, el); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("f000000000000000000ba001", "BOARD", "", domain.Content{"title": "Root"})
	mk("f000000000000000000ba002", "CARD", "f000000000000000000ba001", domain.Content{"textPreview": "Drone permit"})
	mk("f000000000000000000bb001", "BOARD", "", domain.Content{"title": "Permits"})

	bus := &recordingBroadcaster{}
	n := 0
	svc := NewTransactionService(elements, memory.NewTransactionRepo(), NewAccessResolver(elements), bus,
		IDGenerator(func() string { n++; return time_hex(950 + n) }), zap.NewNop())

	if _, err := svc.ApplyWithMeta(ctx, filingPrincipal("f000000000000000000bb001"),
		"f000000000000000000ba001", "", []domain.Op{{
			ElementID: "f000000000000000000ba002", Action: domain.ActionMove,
			Changes: domain.Content{"location": map[string]any{
				"parentId": "f000000000000000000bb001", "section": "CANVAS",
			}},
		}}, TxnMeta{Origin: domain.OriginAgent, AgentRunID: "r-filing"}); err != nil {
		t.Fatalf("filing: %v", err)
	}

	var sawRoot, sawDestination bool
	for _, room := range bus.rooms {
		switch room {
		case "f000000000000000000ba001":
			sawRoot = true
		case "f000000000000000000bb001":
			sawDestination = true
		}
	}
	if !sawRoot {
		t.Error("the board the transaction declares was not told")
	}
	if !sawDestination {
		t.Error("the board the card ARRIVED on never heard about it — anyone " +
			"looking at it sees the card only after a reload")
	}
}

// MoveAcrossBoards was the only service method that legitimately crossed board
// boundaries AND the only write path with no delegation check — while Apply's
// own comment says "there is deliberately no separate agent write path". It was
// exactly that, and once cross-board filing works the natural implementation of
// any roadmap item is "just call MoveAcrossBoards": one line handing the caller
// unattenuated reach with no capability, consequence or budget check.
func TestMoveAcrossBoards_IsSubjectToTheDelegationGate(t *testing.T) {
	svc, elements, ctx := filingFixture(t)

	// A grant that may MOVE but names no destination. Before the collapse this
	// went through untouched, because the method never looked at a delegation.
	p := filingPrincipal() // no DestinationBoardIDs
	err := svc.MoveAcrossBoards(ctx, p,
		[]string{"f000000000000000000ba002"}, "f000000000000000000bb001")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a delegated principal crossed boards through the ungated path: %v", err)
	}
	el, _ := elements.Get(ctx, "f000000000000000000ba002")
	if el.Location.ParentID != "f000000000000000000ba001" {
		t.Error("the card moved anyway")
	}
}

// And the human gesture it exists for still works — collapsing the path must
// not break the drag it was written for. The journal entry now carries an
// explicit Origin, so "empty" stops being a state that silently means human.
func TestMoveAcrossBoards_TheHumanDragStillWorksAndIsAttributed(t *testing.T) {
	svc, elements, ctx := filingFixture(t)

	if err := svc.MoveAcrossBoards(ctx, &domain.Principal{Sub: "alice"},
		[]string{"f000000000000000000ba002"}, "f000000000000000000bb001"); err != nil {
		t.Fatalf("the drag-onto-a-board-tile gesture broke: %v", err)
	}
	el, _ := elements.Get(ctx, "f000000000000000000ba002")
	if el.Location.ParentID != "f000000000000000000bb001" {
		t.Fatalf("the card is still on %s", el.Location.ParentID)
	}
}
