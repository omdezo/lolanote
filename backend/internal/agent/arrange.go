package agent

import (
	"fmt"
	"math"
	"sort"

	"qomranote/backend/internal/domain"
)

// Composition: putting things WHERE they read well, as opposed to filing them
// into containers.
//
// The whole of the design category was blocked on one thing — nothing ever gave
// a move a coordinate. This file is that capability, built so the model never
// emits geometry.
//
// That constraint is deliberate and load-bearing. A model asked for x/y values
// produces overlapping boxes, drifting gutters and arithmetic that is subtly
// wrong in ways nobody notices until the board is unusable. So the model
// expresses INTENT — "these six, as a grid" — and the server computes the
// positions. Same division as everywhere else in this package: the model
// decides what should be true, the server decides what that means in pixels.
//
// It also keeps the existing invariant intact. Geometry is computed once,
// server-side, so the ghost preview a person approves is positioned identically
// to the transaction that commits.

// Layout is the closed set of arrangements the server can compute.
type Layout string

const (
	// LayoutGrid is the default: rows of roughly equal length, reading
	// left-to-right then down.
	LayoutGrid Layout = "grid"
	// LayoutRow is a single horizontal band — a sequence, a timeline, a funnel.
	LayoutRow Layout = "row"
	// LayoutColumn is a single vertical stack — a ranking, a priority order.
	LayoutColumn Layout = "column"
	// LayoutTidy keeps each element roughly where the user put it and only
	// removes overlap and ragged spacing. The least destructive option, and the
	// right answer for "clean this up" on a board somebody arranged by hand.
	LayoutTidy Layout = "tidy"
)

// ValidLayout reports whether a name is one the server can compute.
func ValidLayout(l Layout) bool {
	switch l {
	case LayoutGrid, LayoutRow, LayoutColumn, LayoutTidy:
		return true
	}
	return false
}

// Arrange geometry. Gutters are wider than the gap used when stacking cards
// inside a column: elements loose on a canvas need visible separation to read
// as separate, where a column's border already does that work.
const (
	arrangeGutterX = 40
	arrangeGutterY = 40
	// arrangeRowBudget bounds how wide a grid grows before wrapping, in pixels.
	// Roughly one and a half screens: past that, finding anything means
	// scrolling sideways, which no arrangement can compensate for.
	arrangeRowBudget = 1900
)

// arrangeTarget is one element to place, with the size it will occupy.
type arrangeTarget struct {
	id   string
	w, h float64
	// x, y are where it sits NOW. Tidy preserves their ordering so a hand-made
	// arrangement survives being cleaned up.
	x, y float64
}

// ComputeArrangement returns the position each element should move to.
//
// It never returns overlapping boxes, and it is idempotent: running it twice on
// its own output produces the same result, so "tidy" is safe to repeat and does
// not drift the board a little further each time.
func ComputeArrangement(ids []string, layout Layout, scope *BoardScope) (map[string]ColumnBox, error) {
	if scope == nil {
		return nil, fmt.Errorf("no board")
	}
	if !ValidLayout(layout) {
		return nil, fmt.Errorf("%q is not a layout this server can compute", layout)
	}

	targets := make([]arrangeTarget, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue // the same element twice is a request that cannot be honoured
		}
		el, ok := scope.Elements[id]
		if !ok || el == nil {
			return nil, fmt.Errorf("%s is not on this board", id)
		}
		if el.Location.Section == domain.SectionUnsorted {
			return nil, fmt.Errorf("%s is in the tray, which is a list and has no positions", id)
		}
		if el.Type == domain.TypeLine {
			return nil, fmt.Errorf("connector lines follow the elements they join and are not placed")
		}
		seen[id] = true
		targets = append(targets, arrangeTarget{
			id: id,
			w:  sizeOr(el.Location.Width, defaultArrangeWidth(el.Type)),
			h:  sizeOr(el.Location.Height, defaultArrangeHeight(el.Type)),
			x:  el.Location.Position.X,
			y:  el.Location.Position.Y,
		})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("nothing to arrange")
	}

	// Anchor the arrangement at the top-left of what it is rearranging, so the
	// result appears where the user was already looking rather than jumping to
	// the origin.
	originX, originY := targets[0].x, targets[0].y
	for _, t := range targets {
		originX = math.Min(originX, t.x)
		originY = math.Min(originY, t.y)
	}
	originX = snapTo(originX, GridSnap)
	originY = snapTo(originY, GridSnap)

	switch layout {
	case LayoutRow:
		return packRow(targets, originX, originY), nil
	case LayoutColumn:
		return packColumn(targets, originX, originY), nil
	case LayoutTidy:
		// NOT a grid. A grid repacks everything into as few rows as fit, which
		// silently collapses a two-row arrangement into one — destroying the
		// structure tidy exists to preserve. Instead, keep the rows the user
		// made and clean up inside them.
		return packBands(targets, originX, originY), nil
	default:
		return packGrid(targets, originX, originY), nil
	}
}

// packGrid lays elements out in rows that wrap at the width budget, with each
// row's height set by its tallest member — the same shelf discipline as the
// create-time layout, for the same reason.
func packGrid(targets []arrangeTarget, originX, originY float64) map[string]ColumnBox {
	out := make(map[string]ColumnBox, len(targets))
	x, y, rowMax := originX, originY, 0.0
	for _, t := range targets {
		if x > originX && (x-originX)+t.w > arrangeRowBudget {
			x = originX
			y += rowMax + arrangeGutterY
			rowMax = 0
		}
		out[t.id] = ColumnBox{X: snapTo(x, GridSnap), Y: snapTo(y, GridSnap), Width: t.w}
		x += t.w + arrangeGutterX
		if t.h > rowMax {
			rowMax = t.h
		}
	}
	return out
}

func packRow(targets []arrangeTarget, originX, originY float64) map[string]ColumnBox {
	out := make(map[string]ColumnBox, len(targets))
	x := originX
	for _, t := range targets {
		out[t.id] = ColumnBox{X: snapTo(x, GridSnap), Y: snapTo(originY, GridSnap), Width: t.w}
		x += t.w + arrangeGutterX
	}
	return out
}

func packColumn(targets []arrangeTarget, originX, originY float64) map[string]ColumnBox {
	out := make(map[string]ColumnBox, len(targets))
	y := originY
	for _, t := range targets {
		out[t.id] = ColumnBox{X: snapTo(originX, GridSnap), Y: snapTo(y, GridSnap), Width: t.w}
		y += t.h + arrangeGutterY
	}
	return out
}

// packBands preserves the rows the user made and only cleans up within them:
// left-aligned to a common origin, evenly gutted, no overlap, snapped to grid.
// Each band's vertical position is recomputed from the tallest member of the
// bands above it, which is what removes overlap between rows.
//
// This is the difference between "tidy" and "rearrange". A grid is free to put
// everything on one line if it fits; a tidy must not, because the number of
// rows is information the user put there.
func packBands(targets []arrangeTarget, originX, originY float64) map[string]ColumnBox {
	bands := groupIntoBands(targets)
	out := make(map[string]ColumnBox, len(targets))
	y := originY
	for _, band := range bands {
		sort.SliceStable(band, func(i, j int) bool {
			if band[i].x != band[j].x {
				return band[i].x < band[j].x
			}
			return band[i].id < band[j].id
		})
		x, rowMax := originX, 0.0
		for _, t := range band {
			out[t.id] = ColumnBox{X: snapTo(x, GridSnap), Y: snapTo(y, GridSnap), Width: t.w}
			x += t.w + arrangeGutterX
			if t.h > rowMax {
				rowMax = t.h
			}
		}
		y += rowMax + arrangeGutterY
	}
	return out
}

// groupIntoBands splits elements into the horizontal rows a person would see.
//
// The band boundary walks up from the topmost element and starts a new band
// whenever the next one sits more than a tolerance below the current band's
// top. A fixed tolerance on raw Y would split a row whose members differ by a
// few pixels; growing the band as members join it would eventually swallow the
// whole board.
func groupIntoBands(targets []arrangeTarget) [][]arrangeTarget {
	const bandTolerance = 140.0
	byY := append([]arrangeTarget(nil), targets...)
	sort.SliceStable(byY, func(i, j int) bool {
		if byY[i].y != byY[j].y {
			return byY[i].y < byY[j].y
		}
		return byY[i].x < byY[j].x
	})

	var bands [][]arrangeTarget
	var current []arrangeTarget
	bandTop := 0.0
	for i, t := range byY {
		if i == 0 || t.y-bandTop > bandTolerance {
			if len(current) > 0 {
				bands = append(bands, current)
			}
			current = []arrangeTarget{t}
			bandTop = t.y
			continue
		}
		current = append(current, t)
	}
	if len(current) > 0 {
		bands = append(bands, current)
	}
	return bands
}

// sortReadingOrder puts elements in the order a person would read them, with a
// tolerance band on Y so two things at nearly the same height count as the same
// row. Without the band, a three-pixel difference reorders a row arbitrarily
// and a "tidy" scrambles the meaning it was asked to preserve.
func sortReadingOrder(targets []arrangeTarget) {
	const band = 120.0
	sort.SliceStable(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if math.Abs(a.y-b.y) > band {
			return a.y < b.y
		}
		if a.x != b.x {
			return a.x < b.x
		}
		return a.id < b.id // total order, so the result is deterministic
	})
}

func sizeOr(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}

// defaultArrangeWidth is what an element occupies when it has no stored width.
// Most elements are created without one and are sized by their content, so
// these mirror what the renderer produces.
func defaultArrangeWidth(t domain.ElementType) float64 {
	switch t {
	case domain.TypeColumn:
		return ColumnWidth
	case domain.TypeBoard:
		return 160
	case domain.TypeTable:
		return 300
	case domain.TypeColorSwatch:
		return 140
	}
	return CardWidth
}

func defaultArrangeHeight(t domain.ElementType) float64 {
	switch t {
	case domain.TypeColumn:
		return 320
	case domain.TypeBoard:
		return boardTilePx
	case domain.TypeTable:
		return 200
	case domain.TypeColorSwatch:
		return 140
	case domain.TypeTaskList:
		return 160
	}
	return 120
}
