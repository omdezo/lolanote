package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// JN17's write refusal, which is the half the frontend could not build.
//
// `content.archived` is written by the boards drawer through the ordinary
// transaction path, and until this landed nothing on the server knew the word.
// So a wrapped production sat in the workspace as ordinary content — it is a
// BOARD, therefore organizable — and "tidy up the old stuff" would reorganise
// the record of how a finished film was actually made.
//
// The planner's own prompt makes this likelier than it sounds: it offers
// "Archive the stale stuff" as a worked example of a good request, so the model
// arrives already primed to treat these boards as fair game.
//
// The rule is the template rule reached from the other direction, and it is
// deliberately asymmetric: read freely — a wrapped production is the best record
// the workspace has of how the last one ran — and never write.

func archivedScope() *BoardScope {
	s := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Workspace"}},
		Elements: map[string]*domain.Element{
			"ep1": {ID: "ep1", Type: domain.TypeBoard,
				Content:  domain.Content{"title": "Ep 1 — 2025", "archived": true},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"ep1-col": {ID: "ep1-col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Post"},
				Location: domain.Location{ParentID: "ep1"}},
			"ep1-card": {ID: "ep1-card", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "final grade notes"},
				Location: domain.Location{ParentID: "ep1-col"}},
			"live-col": {ID: "live-col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Ep 2"},
				Location: domain.Location{ParentID: "b1"}},
		},
	}
	s.markTemplates()
	return s
}

// The person archived the BOARD. Its columns and cards carry no flag, so a
// guard checking only the flagged element would refuse a rename of the board
// and permit a rewrite of every card in it — which is the whole of the content.
func TestArchived_ProtectionIsInherited(t *testing.T) {
	s := archivedScope()
	for _, id := range []string{"ep1", "ep1-col", "ep1-card"} {
		if !s.IsArchived(id) {
			t.Errorf("%s is inside finished work and is not protected", id)
		}
	}
	if s.IsArchived("live-col") {
		t.Error("live work was marked as archived")
	}
	// The two protections are separate sets. A board that is archived is not a
	// stencil, and a refusal that named the wrong one would send the model
	// looking for the wrong exit.
	if s.IsTemplate("ep1") {
		t.Error("an archived board was reported as a template")
	}
}

func TestArchived_WritesAreRefusedAtTheToolBoundary(t *testing.T) {
	s := capStaging()
	s.scope.Elements["ep1"] = &domain.Element{ID: "ep1", Type: domain.TypeBoard,
		Content:  domain.Content{"title": "Ep 1 — 2025", "archived": true},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	s.scope.Elements["ep1-col"] = &domain.Element{ID: "ep1-col", Type: domain.TypeColumn,
		Content: domain.Content{"title": "Post"}, Location: domain.Location{ParentID: "ep1"}}
	s.scope.markTemplates()

	if _, err := s.add(Action{Kind: ActCreateNote, Text: "x", ParentID: "ep1"}); err == nil {
		t.Error("the agent added work to a production the person had finished")
	} else if !strings.Contains(err.Error(), "ARCHIVED") {
		t.Errorf("the refusal does not say why:\n%s", err)
	}
	if _, err := s.add(Action{Kind: ActRename, ElementID: "ep1-col", Title: "Editing"}); err == nil {
		t.Error("the agent renamed a column inside finished work")
	} else if !strings.Contains(err.Error(), "unarchiv") {
		// A refusal with no next step is what makes a model try the same write
		// through a different verb.
		t.Errorf("the refusal names no exit:\n%s", err)
	}
	if len(s.plan.Actions) != 0 {
		t.Fatalf("%d refused actions were staged anyway", len(s.plan.Actions))
	}
	if _, err := s.add(Action{Kind: ActCreateNote, Text: "live work", ParentID: "col-1"}); err != nil {
		t.Errorf("live work was refused because an archive exists on the board: %v", err)
	}
}

// An adjustment can hand a plan a parent that staging never saw.
func TestArchived_PreconditionsBackstopsTheRefusal(t *testing.T) {
	s := archivedScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", Text: "x", ParentID: "ep1-col"},
	}}
	v := Preconditions(plan, s, TaskSpec{Autonomy: AutonomyPreview, Budget: DefaultBudget()})
	if v.Passed {
		t.Fatal("a plan writing into an archived board passed the pre-commit gate")
	}
	var named bool
	for _, c := range v.Criteria {
		if c.Name == "archived.untouched" && !c.Passed {
			named = true
		}
	}
	if !named {
		t.Fatalf("the verdict does not name which rule stopped the run: %+v", v.Criteria)
	}
}

func TestArchived_TheDigestNamesThemAndSaysReadsAreFine(t *testing.T) {
	out := archivedScope().Render("")
	if !strings.Contains(out, `ARCHIVED ep1 "Ep 1 — 2025"`) {
		t.Errorf("the digest never says which boards are finished:\n%s", out)
	}
	// Without the read permission the model concludes the board is missing and
	// builds a worse copy of it.
	if !strings.Contains(out, "READ them") {
		t.Errorf("the digest forbids without permitting the read:\n%s", out)
	}
}

// Somebody who opened a wrapped episode and asked for a change is asking on
// purpose. "Unarchive it first" is a rule that teaches people to keep boards
// out of the archive, which loses the feature.
func TestArchived_TheRunsOwnRootIsNotProtectedFromIt(t *testing.T) {
	s := &BoardScope{
		Board: &domain.Element{ID: "ep1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Ep 1 — 2025", "archived": true}},
		Elements: map[string]*domain.Element{
			"col": {ID: "col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "Post"},
				Location: domain.Location{ParentID: "ep1"}},
		},
	}
	s.markTemplates()
	if s.IsArchived("ep1") || s.IsArchived("col") {
		t.Fatal("the run's own root was protected from the person who aimed at it")
	}
}
