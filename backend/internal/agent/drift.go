package agent

import (
	"fmt"
	"sort"

	"qomranote/backend/internal/domain"
)

// Ambient suggestion: noticing a board has drifted, without being asked.
//
// The design constraint is cost and trust, in that order. A suggestion that
// costs a model call cannot run on every board load, and one that nags is worse
// than none — people learn to dismiss the bar itself, and then miss it when it
// matters.
//
// So the TRIGGER is free: a pure function over the board's geometry and shape,
// computed from data the client already has. The model only runs if a person
// acts on what it says. That inversion is what makes an ambient hint affordable
// and quiet enough to be worth having.
//
// It also only reports things a person would agree with on sight. A hint that
// argues with the user about a board they deliberately arranged is a hint that
// gets turned off.

// Drift is one observation about a board's shape, with the request that would
// fix it — so acting on it is a click rather than a sentence to compose.
type Drift struct {
	// Kind lets the client pick an icon and lets telemetry count which
	// observations people actually act on.
	Kind string `json:"kind"`
	// Message is what the user reads. One clause, no jargon.
	Message string `json:"message"`
	// Intent is the request to send if they accept.
	Intent string `json:"intent"`
}

// Drift thresholds are deliberately generous. Every one of these should read as
// obviously true to somebody looking at the board, or the hint is noise.
const (
	driftOverlapMin  = 3  // overlapping pairs before "messy" is fair
	driftLooseMin    = 12 // loose cards before a board wants grouping
	driftLopsided    = 5  // a column holding this many times the median
	driftWallColumns = 8  // containers on one board before it reads as a wall
)

// DetectDrift returns at most ONE observation, the most worth making. Several
// hints at once is a report nobody asked for; one is a nudge.
func DetectDrift(scope *BoardScope) *Drift {
	if scope == nil || len(scope.Items) == 0 {
		return nil
	}

	var loose []*domain.Element
	containers := 0
	counts := map[string]int{}
	for _, it := range scope.Items {
		el, ok := scope.Elements[it.ID]
		if !ok || el == nil {
			continue
		}
		if el.Location.ParentID == scope.Board.ID &&
			el.Location.Section == domain.SectionCanvas && el.Type != domain.TypeLine {
			loose = append(loose, el)
		}
		if el.Location.ParentID != scope.Board.ID {
			counts[el.Location.ParentID]++
		}
	}
	containers = len(scope.ExistingColumns)

	// Overlap first: it is the only one that makes content literally
	// unreadable, and the only one nobody arranges on purpose.
	if n := overlappingPairs(loose); n >= driftOverlapMin {
		return &Drift{
			Kind:    "overlap",
			Message: fmt.Sprintf("%d cards are overlapping here", n),
			Intent:  "Tidy the canvas — fix the overlaps and spacing, without restructuring anything",
		}
	}

	if containers >= driftWallColumns {
		return &Drift{
			Kind:    "wall",
			Message: fmt.Sprintf("%d columns on one board is a lot to scan", containers),
			Intent:  "Group these columns into nested boards so the top level is easier to scan",
		}
	}

	if len(counts) >= 3 {
		if big, median := lopsided(counts); big > 0 {
			return &Drift{
				Kind:    "lopsided",
				Message: fmt.Sprintf("one column holds %d items, most hold about %d", big, median),
				Intent:  "The columns are lopsided — suggest a better way to split them",
			}
		}
	}

	if len(loose) >= driftLooseMin {
		return &Drift{
			Kind:    "loose",
			Message: fmt.Sprintf("%d cards are sitting loose on this board", len(loose)),
			Intent:  "Organize the loose cards on this board into clear columns",
		}
	}
	return nil
}

// overlappingPairs counts elements that visually cover each other. Sizes fall
// back to the same defaults the arrange pass uses, so the two agree about what
// "overlapping" means.
func overlappingPairs(els []*domain.Element) int {
	type box struct{ x1, y1, x2, y2 float64 }
	boxes := make([]box, 0, len(els))
	for _, el := range els {
		w := sizeOr(el.Location.Width, defaultArrangeWidth(el.Type))
		h := sizeOr(el.Location.Height, defaultArrangeHeight(el.Type))
		x, y := el.Location.Position.X, el.Location.Position.Y
		boxes = append(boxes, box{x, y, x + w, y + h})
	}
	n := 0
	for i := range boxes {
		for j := i + 1; j < len(boxes); j++ {
			a, b := boxes[i], boxes[j]
			if a.x1 < b.x2 && b.x1 < a.x2 && a.y1 < b.y2 && b.y1 < a.y2 {
				n++
			}
		}
	}
	return n
}

// lopsided returns the largest container's count and the median, when the gap
// between them is wide enough to be worth saying.
func lopsided(counts map[string]int) (int, int) {
	vals := make([]int, 0, len(counts))
	for _, v := range counts {
		vals = append(vals, v)
	}
	sort.Ints(vals)
	median := vals[len(vals)/2]
	big := vals[len(vals)-1]
	if median > 0 && big >= median*driftLopsided {
		return big, median
	}
	return 0, 0
}
