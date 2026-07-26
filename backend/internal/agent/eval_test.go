package agent_test

import (
	"fmt"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// Structural evals.
//
// Organizing quality was previously judged by looking at a screenshot and
// noticing something was wrong. That is not a feedback loop: it cannot tell you
// whether the next prompt change made things better or worse, so every
// improvement is a guess.
//
// These assert on STRUCTURE, never on wording. An eval that pins exact titles
// fails on every model update and gets deleted within a month; one that asserts
// "no column overlaps another" stays true for as long as the product does.
//
// Each check is written against a Plan, so it runs against a scripted fixture
// today and against a recorded real transcript the moment one exists.

// evalCheck is one structural property a good plan must have.
type evalCheck struct {
	name string
	// fn returns "" when the property holds, or the reason it does not.
	fn func(*agent.Plan, *agent.BoardScope) string
}

var evalChecks = []evalCheck{
	{"no-overlapping-boxes", func(p *agent.Plan, s *agent.BoardScope) string {
		type box struct {
			label          string
			x1, y1, x2, y2 float64
		}
		var boxes []box
		kids := map[string]int{}
		for _, a := range p.Actions {
			if a.ParentID != "" {
				kids[a.ParentID]++
			}
		}
		for _, a := range p.Actions {
			if a.Position == nil {
				continue
			}
			// Independent height model: chrome plus two lines per child. If the
			// layout code and this ever disagree, that is the bug.
			h := 104.0 + float64(kids[a.ElementID])*(27.0+2*21.0+8.0)
			if h < 120 {
				h = 120
			}
			boxes = append(boxes, box{a.Title, a.Position.X, a.Position.Y,
				a.Position.X + a.Position.Width, a.Position.Y + h})
		}
		for i := range boxes {
			for j := i + 1; j < len(boxes); j++ {
				a, b := boxes[i], boxes[j]
				if a.x1 < b.x2 && b.x1 < a.x2 && a.y1 < b.y2 && b.y1 < a.y2 {
					return fmt.Sprintf("%q overlaps %q", a.label, b.label)
				}
			}
		}
		return ""
	}},

	{"labels-fit-their-header", func(p *agent.Plan, _ *agent.BoardScope) string {
		for _, a := range p.Actions {
			budget := agent.ColumnTitleBudget
			if a.Kind == agent.ActCreateBoard {
				budget = agent.BoardTitleBudget
			} else if a.Kind != agent.ActCreateColumn {
				continue
			}
			if n := len([]rune(a.Title)); n > budget {
				return fmt.Sprintf("%q is %d chars, over the %d budget", a.Title, n, budget)
			}
		}
		return ""
	}},

	{"no-empty-containers", func(p *agent.Plan, _ *agent.BoardScope) string {
		kids := map[string]int{}
		for _, a := range p.Actions {
			if a.ParentID != "" {
				kids[a.ParentID]++
			}
		}
		for _, a := range p.Actions {
			if a.Kind == agent.ActCreateColumn && kids[a.ElementID] == 0 {
				return fmt.Sprintf("column %q would be created empty", a.Title)
			}
		}
		return ""
	}},

	{"most-items-get-filed", func(p *agent.Plan, s *agent.BoardScope) string {
		if len(s.Items) == 0 {
			return ""
		}
		moved := map[string]bool{}
		for _, a := range p.Actions {
			if a.Kind == agent.ActMove {
				moved[a.ElementID] = true
			}
		}
		// Building structure and filing almost nothing into it is worse than
		// doing nothing, because it leaves the board messier than it started.
		if len(moved)*2 < len(s.Items) && containerCount(p) > 0 {
			return fmt.Sprintf("built %d container(s) but filed only %d of %d items",
				containerCount(p), len(moved), len(s.Items))
		}
		return ""
	}},

	{"not-a-wall", func(p *agent.Plan, _ *agent.BoardScope) string {
		if n := containerCount(p); n > 8 {
			return fmt.Sprintf("%d containers on one board", n)
		}
		return ""
	}},

	{"balanced-within-reason", func(p *agent.Plan, _ *agent.BoardScope) string {
		kids := map[string]int{}
		for _, a := range p.Actions {
			if a.ParentID != "" {
				kids[a.ParentID]++
			}
		}
		var counts []int
		for _, a := range p.Actions {
			if a.Kind == agent.ActCreateColumn {
				counts = append(counts, kids[a.ElementID])
			}
		}
		if len(counts) < 3 {
			return ""
		}
		lo, hi := counts[0], counts[0]
		for _, c := range counts {
			if c < lo {
				lo = c
			}
			if c > hi {
				hi = c
			}
		}
		if lo > 0 && hi > lo*6 {
			return fmt.Sprintf("largest column holds %d, smallest %d", hi, lo)
		}
		return ""
	}},
}

func containerCount(p *agent.Plan) int {
	n := 0
	for _, a := range p.Actions {
		if a.Kind == agent.ActCreateColumn || a.Kind == agent.ActCreateBoard {
			n++
		}
	}
	return n
}

// runEval applies every structural check and reports all failures at once —
// fixing one at a time is how a suite becomes a chore.
func runEval(t *testing.T, name string, p *agent.Plan, s *agent.BoardScope) {
	t.Helper()
	var failures []string
	for _, c := range evalChecks {
		if why := c.fn(p, s); why != "" {
			failures = append(failures, fmt.Sprintf("  %s: %s", c.name, why))
		}
	}
	if len(failures) > 0 {
		t.Errorf("%s failed %d structural check(s):\n%s",
			name, len(failures), strings.Join(failures, "\n"))
	}
}

// A good plan passes everything; the screenplay wall fails the checks that
// describe exactly what was wrong with it. Both directions matter: a suite that
// only ever passes is not measuring anything.
func TestEval_ScoresGoodAndBadPlans(t *testing.T) {
	const board = "b0000000000000000000000009"
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: board, Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Occupied: agent.Rect{Empty: true},
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("card%04d", i)
		el := &domain.Element{
			ID: id, Type: domain.TypeCard,
			Location: domain.Location{ParentID: board},
			Content:  domain.Content{"textPreview": "a beat of the story"},
		}
		scope.Elements[id] = el
		scope.Items = append(scope.Items, agent.Item{ID: id, Type: domain.TypeCard})
	}

	t.Run("good", func(t *testing.T) {
		p := &agent.Plan{}
		seq := 0
		add := func(a agent.Action) { a.Seq = seq; seq++; p.Actions = append(p.Actions, a) }
		cols := []string{"Setup", "Pressure", "Payoff"}
		for i, title := range cols {
			add(agent.Action{Kind: agent.ActCreateColumn, ElementID: fmt.Sprintf("col%d", i),
				ParentID: board, Title: title})
		}
		for i := 0; i < 12; i++ {
			add(agent.Action{Kind: agent.ActMove, ElementID: fmt.Sprintf("card%04d", i),
				ParentID: fmt.Sprintf("col%d", i%3)})
		}
		agent.LayoutPlan(p, scope)
		runEval(t, "three balanced acts", p, scope)
	})

	t.Run("the-wall-is-caught", func(t *testing.T) {
		p := &agent.Plan{}
		seq := 0
		add := func(a agent.Action) { a.Seq = seq; seq++; p.Actions = append(p.Actions, a) }
		for i := 0; i < 10; i++ {
			add(agent.Action{Kind: agent.ActCreateColumn, ElementID: fmt.Sprintf("col%d", i),
				ParentID: board, Title: fmt.Sprintf("SCENE %d: THE LONG TITLE", i+1)})
		}
		// Everything piled into one column, nine left empty.
		for i := 0; i < 12; i++ {
			add(agent.Action{Kind: agent.ActMove, ElementID: fmt.Sprintf("card%04d", i), ParentID: "col0"})
		}
		agent.LayoutPlan(p, scope)

		var caught []string
		for _, c := range evalChecks {
			if why := c.fn(p, scope); why != "" {
				caught = append(caught, c.name)
			}
		}
		for _, want := range []string{"labels-fit-their-header", "no-empty-containers", "not-a-wall"} {
			found := false
			for _, got := range caught {
				if got == want {
					found = true
				}
			}
			if !found {
				t.Errorf("eval missed %q on a plan that plainly violates it (caught: %v)", want, caught)
			}
		}
	})
}
