package agent_test

import (
	"fmt"
	"math"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// scatter builds a board of loose canvas cards at the given coordinates.
func scatter(board string, at ...[2]float64) *agent.BoardScope {
	scope := &agent.BoardScope{
		Board:    &domain.Element{ID: board, Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	for i, p := range at {
		el := &domain.Element{
			ID: fmt.Sprintf("card%03d", i), Type: domain.TypeCard,
			Location: domain.Location{
				ParentID: board, Section: domain.SectionCanvas,
				Position: domain.Point{X: p[0], Y: p[1]},
				Width:    280, Height: 120,
			},
			Content: domain.Content{"textPreview": fmt.Sprintf("card %d", i)},
		}
		scope.Elements[el.ID] = el
		scope.Items = append(scope.Items, agent.ItemFor(el))
	}
	return scope
}

func idsOf(scope *agent.BoardScope) []string {
	out := make([]string, 0, len(scope.Items))
	for _, it := range scope.Items {
		out = append(out, it.ID)
	}
	return out
}

// overlaps reports the first pair of boxes that intersect. An arrangement that
// overlaps is worse than no arrangement: it hides content that was visible.
func overlaps(boxes map[string]agent.ColumnBox, scope *agent.BoardScope) string {
	type b struct {
		id             string
		x1, y1, x2, y2 float64
	}
	var list []b
	for id, box := range boxes {
		h := 120.0
		if el, ok := scope.Elements[id]; ok && el.Location.Height > 0 {
			h = el.Location.Height
		}
		list = append(list, b{id, box.X, box.Y, box.X + box.Width, box.Y + h})
	}
	for i := range list {
		for j := i + 1; j < len(list); j++ {
			p, q := list[i], list[j]
			if p.x1 < q.x2 && q.x1 < p.x2 && p.y1 < q.y2 && q.y1 < p.y2 {
				return fmt.Sprintf("%s and %s overlap", p.id, q.id)
			}
		}
	}
	return ""
}

func TestArrange_NeverOverlaps(t *testing.T) {
	// Deliberately pathological: everything piled on the same point.
	piled := make([][2]float64, 12)
	for i := range piled {
		piled[i] = [2]float64{100, 100}
	}
	scope := scatter("b1", piled...)

	for _, layout := range []agent.Layout{agent.LayoutGrid, agent.LayoutRow, agent.LayoutColumn, agent.LayoutTidy} {
		boxes, err := agent.ComputeArrangement(idsOf(scope), layout, scope)
		if err != nil {
			t.Fatalf("%s: %v", layout, err)
		}
		if len(boxes) != 12 {
			t.Errorf("%s placed %d of 12", layout, len(boxes))
		}
		if why := overlaps(boxes, scope); why != "" {
			t.Errorf("%s produced an overlap: %s", layout, why)
		}
	}
}

// Tidy must be safe to repeat. If it drifted, every run would nudge the board a
// little further and "clean this up" would become a destructive habit.
func TestArrange_TidyIsIdempotent(t *testing.T) {
	scope := scatter("b1",
		[2]float64{13, 7}, [2]float64{305, 19}, [2]float64{602, 3},
		[2]float64{27, 260}, [2]float64{330, 271}, [2]float64{900, 255})

	first, err := agent.ComputeArrangement(idsOf(scope), agent.LayoutTidy, scope)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Apply the result, then tidy again.
	for id, box := range first {
		scope.Elements[id].Location.Position = domain.Point{X: box.X, Y: box.Y}
	}
	second, err := agent.ComputeArrangement(idsOf(scope), agent.LayoutTidy, scope)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	for id, want := range first {
		got := second[id]
		if got.X != want.X || got.Y != want.Y {
			t.Errorf("%s moved on a second tidy: (%.0f,%.0f) → (%.0f,%.0f)", id, want.X, want.Y, got.X, got.Y)
		}
	}
}

// Tidy must preserve meaning: a hand-made arrangement has an order, and cleaning
// it up must not scramble that order.
func TestArrange_TidyPreservesReadingOrder(t *testing.T) {
	// Two bands, slightly ragged vertically — the case a naive sort ruins.
	scope := scatter("b1",
		[2]float64{600, 12}, [2]float64{20, 4}, [2]float64{310, 18}, // band 1: should read 1,2,0
		[2]float64{315, 400}, [2]float64{18, 396}) // band 2: should read 4,3

	boxes, err := agent.ComputeArrangement(idsOf(scope), agent.LayoutTidy, scope)
	if err != nil {
		t.Fatalf("tidy: %v", err)
	}
	// Reading order is (row, then column). Assert it strictly increases along
	// the order the user's layout implied: top band left-to-right, then the
	// bottom band left-to-right.
	rank := func(id string) (float64, float64) { return boxes[id].Y, boxes[id].X }
	order := []string{"card001", "card002", "card000", "card004", "card003"}
	for i := 1; i < len(order); i++ {
		py, px := rank(order[i-1])
		cy, cx := rank(order[i])
		after := cy > py || (cy == py && cx > px)
		if !after {
			t.Errorf("%s (%.0f,%.0f) should read after %s (%.0f,%.0f)",
				order[i], cx, cy, order[i-1], px, py)
		}
	}
	// The two bands the user made must survive as two bands.
	rows := map[float64]bool{}
	for _, b := range boxes {
		rows[b.Y] = true
	}
	if len(rows) != 2 {
		t.Errorf("tidy produced %d row(s); the user had 2 and tidy must not collapse them", len(rows))
	}
}

// The arrangement anchors where the content already is, so the board does not
// jump to the origin under the user's cursor.
func TestArrange_AnchorsAtExistingContent(t *testing.T) {
	scope := scatter("b1", [2]float64{1200, 800}, [2]float64{1500, 820})
	boxes, err := agent.ComputeArrangement(idsOf(scope), agent.LayoutGrid, scope)
	if err != nil {
		t.Fatalf("grid: %v", err)
	}
	minX, minY := math.Inf(1), math.Inf(1)
	for _, b := range boxes {
		minX, minY = math.Min(minX, b.X), math.Min(minY, b.Y)
	}
	if minX < 1100 || minY < 700 {
		t.Errorf("arrangement jumped to (%.0f,%.0f); it should stay near the content at (1200,800)", minX, minY)
	}
}

// Things without positions must be refused rather than silently given one.
func TestArrange_RefusesWhatHasNoPosition(t *testing.T) {
	scope := scatter("b1", [2]float64{0, 0})
	tray := &domain.Element{
		ID: "tray1", Type: domain.TypeCard,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionUnsorted},
		Content:  domain.Content{"textPreview": "loose"},
	}
	scope.Elements[tray.ID] = tray
	line := &domain.Element{
		ID: "line1", Type: domain.TypeLine,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
		Content:  domain.Content{"fromId": "card000", "toId": "card000"},
	}
	scope.Elements[line.ID] = line

	for _, c := range []struct{ id, want string }{
		{"tray1", "tray"},
		{"line1", "connector"},
		{"nope", "not on this board"},
	} {
		if _, err := agent.ComputeArrangement([]string{c.id}, agent.LayoutGrid, scope); err == nil {
			t.Errorf("%s was arranged; it should be refused", c.id)
		} else if got := err.Error(); !contains(got, c.want) {
			t.Errorf("%s: %q does not explain the refusal (want %q)", c.id, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A plan that both positions an element on the canvas and files it into a
// container contradicts itself: whichever op lands second wins, so the other was
// a change the user was asked to approve for nothing. Composing and
// restructuring in one pass produces exactly this.
func TestPlan_RejectsPlacingAndFilingTheSameElement(t *testing.T) {
	scope := scatter("b1", [2]float64{0, 0})
	col := &domain.Element{
		ID: "col1", Type: domain.TypeColumn,
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas},
		Content:  domain.Content{"title": "Pricing"},
	}
	scope.Elements[col.ID] = col

	box := agent.ColumnBox{X: 40, Y: 40, Width: 280}
	plan := &agent.Plan{Actions: []agent.Action{
		{Seq: 0, Kind: agent.ActPlace, ElementID: "card000", Position: &box},
		{Seq: 1, Kind: agent.ActMove, ElementID: "card000", ParentID: "col1"},
	}}
	v := agent.Preconditions(plan, scope, agent.TaskSpec{Budget: agent.DefaultBudget()})
	if v.Passed {
		t.Fatal("a plan that places and files the same element passed validation")
	}
	var found bool
	for _, c := range v.Criteria {
		if c.Name == "plan.coherent" && !c.Passed {
			found = true
			if !contains(c.Detail, "pick one") {
				t.Errorf("the failure does not say what to do: %q", c.Detail)
			}
		}
	}
	if !found {
		t.Errorf("the contradiction was not reported as plan.coherent: %+v", v.Criteria)
	}

	// And the ordinary case still passes: place one, file a different one.
	plan.Actions[1].ElementID = "col1"
	plan.Actions[1].ParentID = "b1"
	if v := agent.Preconditions(plan, scope, agent.TaskSpec{Budget: agent.DefaultBudget()}); !v.Passed {
		t.Errorf("a coherent plan was rejected: %+v", v.Criteria)
	}
}
