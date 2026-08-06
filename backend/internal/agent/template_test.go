package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// A template is a stencil. Writing into one is destructive twice over: the
// template is gone AND every future instantiation of it is wrong. `isTemplate`
// was invisible to the whole agent — grep found it nowhere — so a template board
// nested in the workspace was admitted as ordinary content, because it is a
// BOARD and therefore organizable.

func templateScope() *BoardScope {
	s := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Workspace"}},
		Elements: map[string]*domain.Element{
			"tpl": {ID: "tpl", Type: domain.TypeBoard,
				Content:  domain.Content{"title": "Sprint planning", "isTemplate": true},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
			"tpl-col": {ID: "tpl-col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "To do"},
				Location: domain.Location{ParentID: "tpl"}},
			"tpl-card": {ID: "tpl-card", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "example"},
				Location: domain.Location{ParentID: "tpl-col"}},
			"real-col": {ID: "real-col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "This week"},
				Location: domain.Location{ParentID: "b1"}},
		},
	}
	s.markTemplates()
	return s
}

// The person marked the BOARD. Its columns and cards carry no flag of their own,
// so a guard that checked only the flagged element would refuse a rename of the
// template and permit a rewrite of every card inside it.
func TestTemplate_ProtectionIsInherited(t *testing.T) {
	s := templateScope()
	for _, id := range []string{"tpl", "tpl-col", "tpl-card"} {
		if !s.IsTemplate(id) {
			t.Errorf("%s is inside a stencil and is not protected", id)
		}
	}
	if s.IsTemplate("real-col") {
		t.Error("ordinary work was marked as a template")
	}
}

// Refused where the model can still act on it — a rule enforced only at commit
// fails the whole run for something it would have fixed in one turn if asked.
func TestTemplate_WritesAreRefusedAtTheToolBoundary(t *testing.T) {
	s := capStaging()
	s.scope.Elements["tpl"] = &domain.Element{ID: "tpl", Type: domain.TypeBoard,
		Content:  domain.Content{"title": "Sprint planning", "isTemplate": true},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	s.scope.Elements["tpl-col"] = &domain.Element{ID: "tpl-col", Type: domain.TypeColumn,
		Content: domain.Content{"title": "To do"}, Location: domain.Location{ParentID: "tpl"}}
	s.scope.markTemplates()

	if _, err := s.add(Action{Kind: ActCreateNote, Text: "x", ParentID: "tpl"}); err == nil {
		t.Error("the agent filled in the person's blank template")
	} else if !strings.Contains(err.Error(), "TEMPLATE") {
		t.Errorf("the refusal does not say what a template is:\n%s", err)
	}
	if _, err := s.add(Action{Kind: ActRename, ElementID: "tpl-col", Title: "Doing"}); err == nil {
		t.Error("the agent renamed a column inside a stencil")
	}
	if len(s.plan.Actions) != 0 {
		t.Fatalf("%d refused actions were staged anyway", len(s.plan.Actions))
	}
	// The whole value of templates being visible is that reading one teaches the
	// agent this workspace's conventions. Work OUTSIDE the stencil is untouched.
	if _, err := s.add(Action{Kind: ActCreateNote, Text: "real work", ParentID: "col-1"}); err != nil {
		t.Errorf("ordinary work was refused because a template exists on the board: %v", err)
	}
}

// An adjustment can redirect an action into a container nothing refused at
// staging time, so the pre-commit gate has to hold the same line.
func TestTemplate_PreconditionsBackstopsTheRefusal(t *testing.T) {
	s := templateScope()
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "n1", Text: "x", ParentID: "tpl-col"},
	}}
	v := Preconditions(plan, s, TaskSpec{Autonomy: AutonomyPreview, Budget: DefaultBudget()})
	if v.Passed {
		t.Fatal("a plan writing into a stencil passed the pre-commit gate")
	}
}

// A capability discovered only by being refused costs a turn every time — and
// the useful half is the READ.
func TestTemplate_TheDigestNamesThemAndSaysReadsAreFine(t *testing.T) {
	out := templateScope().Render("")
	if !strings.Contains(out, `TEMPLATE tpl "Sprint planning"`) {
		t.Errorf("the digest never says which boards are stencils:\n%s", out)
	}
	if !strings.Contains(out, "READ them") {
		t.Errorf("the digest forbids without permitting the read that is the whole point:\n%s", out)
	}
}

// Somebody who opened their sprint template and typed "add a Retro column" aimed
// at it deliberately. Refusing that is the product overriding a direct
// instruction, and it is how a guard earns the reputation that gets it deleted.
func TestTemplate_TheRunsOwnRootIsNotProtectedFromIt(t *testing.T) {
	s := &BoardScope{
		Board: &domain.Element{ID: "tpl", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Sprint planning", "isTemplate": true}},
		Elements: map[string]*domain.Element{
			"col": {ID: "col", Type: domain.TypeColumn,
				Content:  domain.Content{"title": "To do"},
				Location: domain.Location{ParentID: "tpl"}},
		},
	}
	s.markTemplates()
	if s.IsTemplate("tpl") || s.IsTemplate("col") {
		t.Error("a run pointed AT a template refused to touch it, so the person cannot " +
			"use the agent to edit their own stencils at all")
	}
	plan := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateColumn, ElementID: "c1", Title: "Retro", ParentID: "tpl"},
	}}
	if v := Preconditions(plan, s, TaskSpec{Autonomy: AutonomyPreview, Budget: DefaultBudget()}); !v.Passed {
		t.Errorf("the explicit request was refused: %+v", v.Criteria)
	}
}
