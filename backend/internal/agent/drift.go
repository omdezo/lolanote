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
	// driftTrayMin is where a capture inbox stops being a working set and starts
	// being a backlog. Lower than driftLooseMin because the tray has no layout to
	// justify itself with: twelve cards arranged on a canvas may be a diagram,
	// and eight in a tray is eight things nobody has filed.
	driftTrayMin = 8
)

// trayCount is how many items sit in this board's Unsorted tray, counting what
// the budget elided as well as what it printed.
//
// The count is the whole point: a queue's LENGTH is the fact that decides
// whether to act on it, so a tray rendered as a handful of visible lines plus an
// elision is a tray whose only load-bearing property has been hidden.
func trayCount(scope *BoardScope) int {
	n := 0
	for _, el := range scope.Elements {
		if el != nil && el.Location.ParentID == scope.Board.ID &&
			el.Location.Section == domain.SectionUnsorted {
			n++
		}
	}
	if e := scope.ElidedFacts[scope.Board.ID]; e != nil {
		// What the budget cut counts too. A tray long enough to overflow the
		// digest is the exact board this hint exists for, and counting only what
		// got printed would silence it precisely there.
		n += e.Unsorted
	}
	return n
}

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

	// The tray, before anything about the canvas.
	//
	// DetectDrift measured canvas clutter and skipped the tray entirely — so the
	// one board state that most obviously wants an agent never triggered the
	// ambient suggestion. A capture inbox filling up is a truer drift signal than
	// density: loose cards on a canvas may be a layout somebody meant, and a
	// twenty-item tray is nobody's intention. It is also the destination `file_to`
	// already writes into, so the hint and the capability line up.
	if n := trayCount(scope); n >= driftTrayMin {
		return &Drift{
			Kind:    "tray",
			Message: fmt.Sprintf("%d items are waiting in the tray", n),
			Intent:  "File the items in the unsorted tray onto this board where they belong",
		}
	}

	// Overlap next: it is the only one that makes content literally
	// unreadable, and the only one nobody arranges on purpose.
	if n := overlappingPairs(loose); n >= driftOverlapMin {
		return &Drift{
			Kind:    "overlap",
			Message: fmt.Sprintf("%d cards are overlapping here", n),
			Intent:  "Tidy the canvas — fix the overlaps and spacing, without restructuring anything",
		}
	}

	// The wall hint, unless the wall is a document.
	//
	// "Past about seven columns you are building a wall" was tuned on
	// knowledge-work boards and stated as universal law. A shooting schedule for
	// a modest feature is eighteen to thirty day-columns and a Day Out of Days
	// is twenty to forty — correct, deliberate, and the shape the trade decided
	// long before this product existed. Offering to "group these columns into
	// nested boards" there is the product offering to destroy a schedule, in one
	// click, on a board somebody spent a week building. So the aesthetic is
	// conditional on the ARTEFACT rather than on the count.
	if containers >= driftWallColumns && boardFixedShape(scope) == "" {
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
