package agent

import (
	"fmt"
	"sort"
	"strings"
)

// The agent's view of its own work.
//
// Everything else in this package flows one way: the model emits calls, the
// server stages them, and the result appears on a canvas the model never sees.
// That asymmetry is what produced eight columns of eight cards with clipped
// titles — every individual call was correct, and the arrangement was
// unusable.
//
// RenderSelfView closes the loop. It runs the same layout pass that will be
// committed, then describes the outcome in the terms a person would use looking
// at the board: how wide it got, how many rows, which columns are lopsided.
//
// The division of labour matters. The SERVER states facts — counts, widths,
// row assignments — because it can compute them exactly and deterministically.
// The MODEL decides what, if anything, to do about them. A server that judged
// would be a second, worse planner; a model asked to measure would hallucinate
// numbers.

// selfViewLimits are the thresholds at which the server volunteers an
// observation. They are deliberately loose: an assistant that comments on every
// arrangement trains the reader to ignore it.
const (
	// crowdedColumnCount is where a row of columns stops reading as a board and
	// starts reading as a wall.
	crowdedColumnCount = 7
	// lopsidedRatio flags a container holding this many times more than the
	// median of its siblings.
	lopsidedRatio = 3
	// wideBoardPx is roughly two screens at default zoom.
	wideBoardPx = 2600
)

// RenderSelfView describes the plan's committed geometry, plus any observations
// worth the model's attention. Returns the empty string when the plan places
// nothing on a canvas — there is no arrangement to review.
func RenderSelfView(p *Plan, scope *BoardScope) string {
	if p == nil || scope == nil {
		return ""
	}
	LayoutPlan(p, scope)

	kids := childIndex(p)
	var items []placedItem
	minX, maxX := 0.0, 0.0
	rows := map[float64]int{}
	first := true
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Position == nil {
			continue
		}
		items = append(items, placedItem{a: a, children: len(kids[a.ElementID])})
		if first || a.Position.X < minX {
			minX = a.Position.X
		}
		if first || a.Position.X+a.Position.Width > maxX {
			maxX = a.Position.X + a.Position.Width
		}
		first = false
		rows[a.Position.Y]++
	}
	if len(items) == 0 {
		return ""
	}

	// Number the rows top to bottom so the description matches reading order.
	ys := make([]float64, 0, len(rows))
	for y := range rows {
		ys = append(ys, y)
	}
	sort.Float64s(ys)
	rowOf := map[float64]int{}
	for i, y := range ys {
		rowOf[y] = i + 1
	}
	for i := range items {
		items[i].row = rowOf[items[i].a.Position.Y]
	}

	var b strings.Builder
	width := int(maxX - minX)
	fmt.Fprintf(&b, "ARRANGEMENT — %d px wide · %d row(s)\n", width, len(ys))

	for _, y := range ys {
		var line []string
		for _, it := range items {
			if it.a.Position.Y != y {
				continue
			}
			label := it.a.Title
			if label == "" {
				label = truncate(it.a.Text, 18)
			}
			if it.a.Kind.Container() {
				line = append(line, fmt.Sprintf("%s(%d)", label, it.children))
			} else {
				line = append(line, label)
			}
		}
		fmt.Fprintf(&b, "  row %d  %s\n", rowOf[y], strings.Join(line, "  "))
	}

	if obs := observations(items, width, len(ys)); len(obs) > 0 {
		b.WriteString("\n")
		for _, o := range obs {
			fmt.Fprintf(&b, "! %s\n", o)
		}
	}
	// Unplaced work still counts: a plan that files everything into one column
	// looks tidy here and is not.
	if loose := looseCount(p, scope); loose > 0 {
		fmt.Fprintf(&b, "! %d item(s) stay where they are, outside anything you made\n", loose)
	}
	return b.String()
}

// placedItem is one element with a coordinate, plus what the plan puts in it.
type placedItem struct {
	a        *Action
	children int
	row      int
}

// observations are the facts worth volunteering. Each is a measurement, never a
// verdict — "8 columns in one row" rather than "this is badly organised".
func observations(items []placedItem, width, rowCount int) []string {
	var out []string

	counts := []int{}
	for _, it := range items {
		if it.a.Kind.Container() {
			counts = append(counts, it.children)
		}
	}
	// Count containers on the BOARD, not per row. Wrapping eight columns into
	// two tidy rows of four fixes the collision and leaves the wall — the
	// reader still has to hold eight groups in their head to find anything.
	if len(counts) >= crowdedColumnCount {
		out = append(out, fmt.Sprintf(
			"%d containers on one board (%d row(s)) — past about %d this reads as a wall rather than a board; consider grouping them into nested boards",
			len(counts), rowCount, crowdedColumnCount-1))
	}
	if width > wideBoardPx {
		out = append(out, fmt.Sprintf(
			"%d px wide — roughly %.1f screens; anything past the first needs scrolling to find", width, float64(width)/1280))
	}

	if len(counts) >= 3 {
		sorted := append([]int(nil), counts...)
		sort.Ints(sorted)
		median := sorted[len(sorted)/2]
		if median > 0 {
			var big, empty []string
			for _, it := range items {
				if !it.a.Kind.Container() {
					continue
				}
				switch {
				case it.children == 0:
					empty = append(empty, it.a.Title)
				case it.children >= median*lopsidedRatio:
					big = append(big, fmt.Sprintf("%s(%d)", it.a.Title, it.children))
				}
			}
			if len(big) > 0 {
				out = append(out, fmt.Sprintf(
					"%s hold(s) far more than the median of %d — usually the grouping is too coarse, not the content lopsided",
					strings.Join(big, ", "), median))
			}
			if len(empty) > 0 {
				out = append(out, fmt.Sprintf("%s will be created empty", strings.Join(empty, ", ")))
			}
		}
	}
	return out
}

// looseCount is how many in-scope items the plan leaves untouched. Creating
// elaborate structure and filing almost nothing into it is the failure mode
// this catches.
func looseCount(p *Plan, scope *BoardScope) int {
	touched := map[string]bool{}
	for _, a := range p.Actions {
		if a.Kind == ActMove || a.Kind == ActDelete {
			touched[a.ElementID] = true
		}
	}
	loose := 0
	for _, it := range scope.Items {
		if it.ID == scope.Board.ID || touched[it.ID] {
			continue
		}
		if el, ok := scope.Elements[it.ID]; ok && el != nil {
			// Only count things sitting loose on the board itself; anything
			// already inside a container is filed by definition.
			if el.Location.ParentID == scope.Board.ID {
				loose++
			}
		}
	}
	return loose
}

// reviewTurn returns the one prompt that makes the model look at its own work,
// or "" when the look is unnecessary or already taken.
//
// The design constraint is that this must be MANDATORY BUT CHEAP. Optional, and
// the model skips it under step pressure — exactly when it is rushing and most
// likely to have built something clumsy. Expensive, and every run pays double
// for a check that usually finds nothing.
//
// So it fires at most once, only when there is an arrangement to see, and it
// asks a SPECIFIC question. "Is this good?" reliably produces agreement with
// whatever was just built; "name the weakest grouping" produces information,
// because naming nothing is a visibly different answer from naming something.
func (s *staging) reviewTurn(stepsLeft int) string {
	if s.reviewed || len(s.plan.Actions) == 0 {
		return ""
	}
	// Never spend the last step on a review. A run that reviews and then has no
	// turn left to act on what it saw has paid for the insight and thrown it
	// away — and would report a finished plan as incomplete.
	if stepsLeft < 2 {
		return ""
	}
	s.reviewed = true // fires once, whatever the model does with it

	view := RenderSelfView(s.plan, s.scope)
	if view == "" {
		return "" // nothing lands on a canvas; there is no arrangement to judge
	}
	return "Before this reaches the user, here is what you have actually built:\n\n" +
		view +
		"\nName the weakest grouping in one clause. If it is worth changing, change it with " +
		"more tool calls now. If the arrangement is right as it stands, call finish again and " +
		"say in your summary why this shape suits the material."
}
