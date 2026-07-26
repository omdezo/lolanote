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

// P1: without coordinates, "put this next to that" and "tidy the left cluster"
// are unanswerable, and the agent cannot see the arrangement it is disturbing.
func TestDigest_CarriesCoarseGeometry(t *testing.T) {
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Plan"}},
		Elements: map[string]*domain.Element{},
	}
	at := func(id string, x, y float64) *domain.Element {
		return &domain.Element{
			ID: id, Type: domain.TypeCard,
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas,
				Position: domain.Point{X: x, Y: y}},
			Content: domain.Content{"textPreview": "note " + id},
		}
	}
	// Two neighbours in one bucket, one far away, one in the tray.
	for _, el := range []*domain.Element{at("c1", 10, 10), at("c2", 90, 40), at("c3", 1400, 700)} {
		scope.Elements[el.ID] = el
		scope.Items = append(scope.Items, agent.ItemFor(el))
	}
	tray := &domain.Element{
		ID: "c4", Type: domain.TypeCard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted},
		Content:  domain.Content{"textPreview": "loose"},
	}
	scope.Elements[tray.ID] = tray
	scope.Items = append(scope.Items, agent.ItemFor(tray))

	out := scope.Render("")
	if !strings.Contains(out, "@A1") {
		t.Errorf("no cell reference for an element at the origin:\n%s", out)
	}
	if !strings.Contains(out, "LAYOUT:") || !strings.Contains(out, "2 around A1") {
		t.Errorf("neighbourhoods not summarized:\n%s", out)
	}
	// The tray is a list, not a canvas; giving it coordinates would be a lie.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "c4 ") && strings.Contains(line, "@") {
			t.Errorf("an unsorted item was given geometry: %s", line)
		}
	}
}

// I3: board conventions are author-written and arrive as trust-labelled data,
// never as instructions the agent inferred for itself.
func TestDigest_CarriesBoardInstructionsAsData(t *testing.T) {
	scope := &agent.BoardScope{
		Board:        &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Pipeline"}},
		Elements:     map[string]*domain.Element{},
		Instructions: "Columns are pipeline stages; never add one.",
	}
	out := scope.Render("")
	if !strings.Contains(out, "HOW THIS BOARD WORKS ⟨user⟩") {
		t.Errorf("board instructions missing or unlabelled:\n%s", out)
	}
	if !strings.Contains(out, "never add one") {
		t.Errorf("instruction text not rendered:\n%s", out)
	}
}

// R5: a ragged table is rejected by shape validation rather than reaching the
// canvas as a broken grid.
func TestTable_RaggedRowsAreRejected(t *testing.T) {
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	good := &agent.Plan{Actions: []agent.Action{{
		Seq: 0, Kind: agent.ActCreateTable, ElementID: "t1", ParentID: "b1",
		Rows: [][]string{{"Tier", "Price"}, {"Team", "$10"}},
	}}}
	if v := agent.Preconditions(good, scope, agent.TaskSpec{Budget: agent.DefaultBudget()}); !v.Passed {
		t.Fatalf("a well-formed table was rejected: %+v", v.Criteria)
	}
	ragged := &agent.Plan{Actions: []agent.Action{{
		Seq: 0, Kind: agent.ActCreateTable, ElementID: "t1", ParentID: "b1",
		Rows: [][]string{{"Tier", "Price"}, {"Team"}},
	}}}
	if v := agent.Preconditions(ragged, scope, agent.TaskSpec{Budget: agent.DefaultBudget()}); v.Passed {
		t.Fatal("a ragged table passed validation")
	}
}

// R4: a connection to itself is meaningless, and both ends must be real.
func TestConnect_RequiresTwoDistinctEnds(t *testing.T) {
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	self := &agent.Plan{Actions: []agent.Action{{
		Seq: 0, Kind: agent.ActConnect, ElementID: "l1", ParentID: "b1",
		FromID: "c1", ToID: "c1",
	}}}
	if v := agent.Preconditions(self, scope, agent.TaskSpec{Budget: agent.DefaultBudget()}); v.Passed {
		t.Fatal("an element was allowed to be connected to itself")
	}
}

// P2: this is the render that silently did not ship once already — the Item
// fields existed and CompileScope populated them, but Render never emitted
// them, so the agent was blind to tags it could set. Pin it.
func TestDigest_CarriesLabelsAndColours(t *testing.T) {
	el := &domain.Element{
		ID: "c1", Type: domain.TypeCard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
		Content:  domain.Content{"textPreview": "waiting on legal", "color": "#fff9db"},
		LabelIDs: []string{"lbl1", "lbl2"},
	}
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{"c1": el},
		Items:    []agent.Item{agent.ItemFor(el)},
		Labels:   []agent.LabelRef{{ID: "lbl1", Name: "Blocked"}},
	}
	out := scope.Render("")

	// The vocabulary, so the agent reuses a tag instead of coining a duplicate.
	if !strings.Contains(out, "lbl1=Blocked") {
		t.Errorf("label vocabulary missing:\n%s", out)
	}
	// What is already tagged, so it does not re-tag or contradict.
	if !strings.Contains(out, "⚑lbl1,lbl2") {
		t.Errorf("existing tags missing from the item line:\n%s", out)
	}
	// Colour by NAME: "#fff9db" means nothing to a language model, and the
	// same names are what set_color accepts.
	if !strings.Contains(out, "◧yellow") {
		t.Errorf("colour missing or not named:\n%s", out)
	}
}
