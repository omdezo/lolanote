package agent

import "math"

// Deterministic canvas geometry.
//
// Layout is computed by the server, never by the model and never by the client.
// That matters twice over: the preview a person approves is positioned
// identically to the transaction that later commits, and a plan that sits
// unapplied for a while cannot drift.
//
// The hard part is height. A column is as tall as what the plan puts inside it,
// and nothing on the server can measure a browser. So this file carries a small
// model of the renderer and deliberately rounds every estimate UP: guessing too
// tall only leaves a wider gap, while guessing too short drops the next row of
// columns on top of the previous one.

const (
	// CardWidth and ColumnWidth match what the app's own tools create, so an
	// agent-made element is indistinguishable from a hand-made one.
	CardWidth   = 280
	ColumnWidth = 320
	// Gap is the space left between newly placed elements.
	Gap = 24
	// TopGap is the clearance left below existing content.
	TopGap = 48
	// GridSnap matches the app's snap-to-grid preference.
	GridSnap = 20
	// RowBudget bounds how wide a freshly placed row grows before it wraps.
	// A budget in pixels rather than a count keeps rows even when a plan mixes
	// 320px columns with 280px cards.
	RowBudget = 1600
)

// The height model, measured against the running renderer rather than derived
// from the stylesheet — what matters is the pixel height the user ends up
// looking at, including padding, header and the "Add a note" footer.
const (
	columnChrome    = 104 // column padding + header + add-a-note footer
	cardGapPx       = 8   // .column-body gap
	cardLinePx      = 21  // one wrapped line of card text
	cardChromePx    = 27  // card padding: a single-line card measures 48px
	colCharsPerLine = 34  // conservative wrap width inside a 320px column
	todoRowPx       = 26  // one task row
	todoChromePx    = 60  // to-do title + padding
	boardTilePx     = 140 // a board renders as a fixed tile, not a sized box
	minElementPx    = 48
	maxTextLines    = 14 // long notes are scrolled, not grown, past this
)

// Label budgets, in characters. Headers are fixed-width and clip: a column
// title renders uppercase with a chevron and a count badge beside it, and a
// board renders as a small tile. These are the widths a name has to live in,
// and they belong next to the geometry that produced them.
const (
	ColumnTitleBudget = 20
	BoardTitleBudget  = 24
)

func labelBudget(k ActionKind) int {
	if k == ActCreateBoard {
		return BoardTitleBudget
	}
	return ColumnTitleBudget
}

// LayoutPlan assigns positions to every element the plan places directly on the
// root board's canvas.
//
// Elements that land inside a column or a to-do list need no position — those
// containers order their children by index — so only root-canvas placements are
// given geometry, packed into rows below whatever is already there.
func LayoutPlan(p *Plan, scope *BoardScope) {
	if p == nil || scope == nil {
		return
	}
	startX, startY := 0.0, 0.0
	if !scope.Occupied.Empty {
		startX = scope.Occupied.MinX
		startY = scope.Occupied.MaxY + TopGap
	}

	kids := childIndex(p)

	// Shelf packing: fill a row left to right, then drop below the TALLEST
	// element in the row just closed. A fixed row height is what used to stack
	// a second row of columns on top of the first.
	x, y, rowMax := startX, startY, 0.0
	for i := range p.Actions {
		a := &p.Actions[i]
		if !placesOnCanvas(a, scope) {
			continue
		}
		width := float64(CardWidth)
		if a.Kind == ActCreateColumn {
			width = ColumnWidth
		}
		if x > startX && (x-startX)+width > RowBudget {
			x = startX
			y += rowMax + Gap
			rowMax = 0
		}
		a.Position = &ColumnBox{
			X:     snapTo(x, GridSnap),
			Y:     snapTo(y, GridSnap),
			Width: width,
		}
		if h := estimateHeight(a, kids, scope); h > rowMax {
			rowMax = h
		}
		x += width + Gap
	}
}

// childIndex groups every action that puts something inside a container, keyed
// by that container's id. Moves count: a column's height is driven as much by
// the cards moved into it as by the ones created there.
func childIndex(p *Plan) map[string][]*Action {
	out := make(map[string][]*Action)
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ParentID == "" {
			continue
		}
		if a.Kind.Creates() || a.Kind == ActMove {
			out[a.ParentID] = append(out[a.ParentID], a)
		}
	}
	return out
}

// estimateHeight is the rendered height an action will occupy on the canvas.
func estimateHeight(a *Action, kids map[string][]*Action, scope *BoardScope) float64 {
	switch a.Kind {
	case ActCreateColumn:
		children := kids[a.ElementID]
		h := float64(columnChrome)
		for i, c := range children {
			if i > 0 {
				h += cardGapPx
			}
			h += childHeight(c, scope)
		}
		if len(children) == 0 {
			h += minElementPx
		}
		return h
	case ActCreateBoard:
		return boardTilePx
	case ActCreateTodo:
		return todoChromePx + float64(len(a.Tasks))*todoRowPx
	case ActCreateNote:
		return textHeight(a.Text)
	}
	return minElementPx
}

// childHeight sizes one element sitting inside a column. A created child has
// its text in the plan; a moved one has it on the board already.
func childHeight(a *Action, scope *BoardScope) float64 {
	switch a.Kind {
	case ActCreateTodo:
		return todoChromePx + float64(len(a.Tasks))*todoRowPx
	case ActCreateNote, ActCreateLink:
		return textHeight(firstNonEmpty(a.Text, a.Title))
	case ActMove, ActSetText, ActRename:
		if a.Kind == ActSetText || a.Kind == ActRename {
			return textHeight(firstNonEmpty(a.Text, a.Title))
		}
		if el, ok := scope.Elements[a.ElementID]; ok && el != nil {
			text, _ := el.Content["textPreview"].(string)
			if text == "" {
				text, _ = el.Content["title"].(string)
			}
			return textHeight(text)
		}
	}
	return minElementPx
}

// textHeight rounds a string up to whole wrapped lines. Runes, not bytes, so
// Arabic and CJK cards are not estimated at several times their real height.
func textHeight(s string) float64 {
	lines := math.Ceil(float64(len([]rune(s))) / colCharsPerLine)
	if lines < 1 {
		lines = 1
	}
	if lines > maxTextLines {
		lines = maxTextLines
	}
	return cardChromePx + lines*cardLinePx
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// placesOnCanvas reports whether an action needs a coordinate: it creates
// something, and it puts it straight onto the board rather than inside a
// container.
func placesOnCanvas(a *Action, scope *BoardScope) bool {
	// ActPlace already carries a computed position from ComputeArrangement.
	// Re-laying it out here would overwrite the arrangement with the default
	// shelf pack, which is the opposite of what was asked for.
	if a.Kind == ActPlace {
		return false
	}
	if !a.Kind.Creates() {
		return false
	}
	if a.Section == string(sectionUnsorted) {
		return false // the tray is a list, not a canvas
	}
	return a.ParentID == scope.Board.ID
}

func snapTo(v, step float64) float64 { return math.Round(v/step) * step }
