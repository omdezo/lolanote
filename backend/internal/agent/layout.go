package agent

import (
	"math"

	"qomranote/backend/internal/domain"
)

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

// LayoutPlan assigns positions to every element the plan places on a canvas —
// the run's root board, a board the plan creates, or a board that was already
// there.
//
// Elements that land inside a column or a to-do list need no position: those
// containers order their children by index, and a coordinate would be a second,
// contradictory answer to "where does this go". Everything else is packed into
// rows clear of what its own canvas already holds.
func LayoutPlan(p *Plan, scope *BoardScope) {
	if p == nil || scope == nil {
		return
	}
	kids := childIndex(p)

	// A plan that designed a SHAPE lays its new elements out as that shape
	// rather than packing them into rows. The graph is the plan's own
	// connections: these elements do not exist yet, so there is nothing on the
	// board to read it from.
	if p.Shape.needsEdges() {
		sx, sy := startPoint(scope)
		if layoutShaped(p, scope, kids, sx, sy) {
			return
		}
	}

	// EVERY canvas this plan writes to gets laid out, not just the root board's.
	//
	// This used to position only elements whose parent was the run's root
	// board. The agent's normal answer to "set up X" is a nested board with
	// columns inside it — and every one of those columns came out with no
	// position and no width, so they stacked at the origin. Not a bad
	// arrangement: no arrangement at all.
	groups := map[string][]*Action{}
	var canvases []string
	for i := range p.Actions {
		a := &p.Actions[i]
		if !placesOnCanvas(a, scope, p) {
			continue
		}
		if _, seen := groups[a.ParentID]; !seen {
			canvases = append(canvases, a.ParentID)
		}
		groups[a.ParentID] = append(groups[a.ParentID], a)
	}
	for _, canvas := range canvases {
		packCanvas(groups[canvas], canvas, scope, kids)
	}
}

// packCanvas shelf-packs one board's canvas.
//
// Every canvas is packed against ITS OWN history. The root board has a viewport
// saying where the person is looking; a nested board that already exists has
// whatever is drawn on it and possibly a row of columns to join; a board this
// plan is creating has nothing at all.
//
// Only the root used to get any of that, and everything else started at (0,0) —
// right for a board being created and badly wrong for one that already exists,
// which is how a column filed into a live sub-board landed on top of the
// columns already there.
func packCanvas(actions []*Action, canvasID string, scope *BoardScope, kids map[string][]*Action) {
	startX, startY := canvasStart(scope, canvasID)
	rowY, rowX, joining := columnRow(scope, canvasID)

	x, y, rowMax := startX, startY, 0.0
	for _, a := range actions {
		// A new column joins the row of columns already on the board rather
		// than opening a band beneath them. On a kanban board a second row
		// reads as the columns stacking rather than lining up.
		if a.Kind == ActCreateColumn && joining {
			a.Position = &ColumnBox{
				X: snapTo(rowX, GridSnap), Y: snapTo(rowY, GridSnap), Width: ColumnWidth,
			}
			rowX += ColumnWidth + Gap
			continue
		}
		width := float64(CardWidth)
		if a.Kind == ActCreateColumn {
			width = ColumnWidth
		}
		// Shelf packing: fill a row left to right, then drop below the TALLEST
		// element in the row just closed. A fixed row height is what used to
		// stack a second row of columns on the previous one.
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

// layoutShaped positions the plan's new canvas elements as a tree or a flow,
// using the connections the same plan stages. Reports whether it could.
//
// A connector is not itself positioned — it is drawn between its endpoints — so
// it is excluded from the node set and contributes only an edge.
func layoutShaped(p *Plan, scope *BoardScope, kids map[string][]*Action, origin ...float64) bool {
	originX, originY := 0.0, 0.0
	if len(origin) == 2 {
		originX, originY = origin[0], origin[1]
	}
	var nodes []node
	var edges []edge
	positionable := map[string]bool{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind == ActConnect {
			continue
		}
		if !placesOnCanvas(a, scope, p) {
			continue
		}
		w := float64(CardWidth)
		if a.Kind == ActCreateColumn {
			w = ColumnWidth
		}
		nodes = append(nodes, node{id: a.ElementID, w: w, h: estimateHeight(a, kids, scope)})
		positionable[a.ElementID] = true
	}
	if len(nodes) < 2 {
		return false // one box is not a shape
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind != ActConnect {
			continue
		}
		if positionable[a.FromID] && positionable[a.ToID] {
			edges = append(edges, edge{from: a.FromID, to: a.ToID})
		}
	}
	if len(edges) == 0 {
		return false // nothing connected: fall back to packing rather than
		// drawing an empty shape
	}

	var boxes map[string]ColumnBox
	if p.Shape == LayoutTree {
		boxes = layoutTree(nodes, edges, originX, originY)
	} else {
		boxes = layoutFlow(nodes, edges, originX, originY)
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		box, ok := boxes[a.ElementID]
		if !ok {
			continue
		}
		a.Position = &ColumnBox{
			X: snapTo(box.X, GridSnap), Y: snapTo(box.Y, GridSnap), Width: box.Width,
		}
	}
	return true
}

// canvasStart is where a fresh block of content begins on one canvas.
//
// The viewport anchor is deliberately ROOT-ONLY. A viewport is a rectangle in
// the ROOT board's coordinates — it says nothing about where anybody is looking
// inside a nested board, and reusing it there would place that board's new
// columns at numbers read off a different canvas. So sub-canvases pack against
// their own occupied bounds, and a canvas with nothing on it — one this plan is
// creating, or an empty one — begins at its own origin.
func canvasStart(scope *BoardScope, canvasID string) (x, y float64) {
	if canvasID == scope.Board.ID {
		return startPoint(scope)
	}
	occ := scope.CanvasOccupancy(canvasID)
	if occ.Empty {
		return 0, 0
	}
	return occ.MinX, occ.MaxY + TopGap
}

// startPoint decides where a fresh block of content begins.
//
// Below everything on the board is the safe answer and, on a large board, the
// useless one: a person working in one corner of a wall of content watched new
// cards appear thousands of pixels away, which reads as the agent ignoring
// where the work is happening.
//
// So: start where they are LOOKING, and only fall back to the bottom of the
// board when nothing is visible to anchor to. Content already inside the view
// is stepped over rather than written onto.
func startPoint(scope *BoardScope) (x, y float64) {
	fallbackX, fallbackY := 0.0, 0.0
	if !scope.Occupied.Empty {
		fallbackX = scope.Occupied.MinX
		fallbackY = scope.Occupied.MaxY + TopGap
	}

	v := scope.Viewport
	if !v.Valid() {
		return fallbackX, fallbackY
	}

	// Inset from the very edge so the first element is not half off-screen.
	x = v.X + Gap
	y = v.Y + Gap

	// Step below anything already occupying the visible band, so "where you are
	// looking" never means "on top of what you are looking at".
	lowest := y
	for _, el := range scope.Elements {
		if el.Location.ParentID != scope.Board.ID || el.IsDeleted() {
			continue
		}
		if el.Location.Section == domain.SectionUnsorted {
			continue
		}
		b := elementBox(el)
		if b.right <= v.X || b.left >= v.X+v.Width {
			continue // outside the visible band horizontally
		}
		if b.bottom <= v.Y || b.top >= v.Y+v.Height {
			continue // above or below the view
		}
		if b.bottom+TopGap > lowest {
			lowest = b.bottom + TopGap
		}
	}
	return x, lowest
}

type box struct{ left, top, right, bottom float64 }

func elementBox(el *domain.Element) box {
	w := el.Location.Width
	if w <= 0 {
		w = CardWidth
	}
	h := el.Location.Height
	if h <= 0 {
		h = minElementPx
	}
	return box{
		left: el.Location.Position.X, top: el.Location.Position.Y,
		right: el.Location.Position.X + w, bottom: el.Location.Position.Y + h,
	}
}

// rowTolerance is how far apart two columns' tops may be and still count as the
// same row. People nudge things; a row is a visual fact, not an exact one.
const rowTolerance = 120.0

// maxRowWidth bounds how far a joined row may grow. A kanban row scrolls
// horizontally and that is normal, but past roughly a dozen columns a board has
// stopped being a row and joining it stops being helpful.
const maxRowWidth = 3 * RowBudget

// columnRow finds the row of columns a newly created column should join on the
// given canvas, and where along it the next one goes.
//
// The chosen row is the one with the MOST columns in it — that is the board's
// main row, and the one a person means when they say "add a column". Ties go to
// the topmost, because that is the one they are looking at.
//
// canvasID rather than the root board, because a nested board is a kanban board
// too: a column added to one belongs at the end of the row it already has, and
// hardcoding the root meant every sub-board looked like a blank canvas with no
// row to join.
func columnRow(scope *BoardScope, canvasID string) (y, nextX float64, ok bool) {
	type band struct {
		y, right float64
		n        int
	}
	var bands []*band
	for _, el := range scope.Elements {
		if el.Type != domain.TypeColumn || el.Location.ParentID != canvasID {
			continue
		}
		if el.Location.Section == domain.SectionUnsorted || el.IsDeleted() {
			continue
		}
		top := el.Location.Position.Y
		width := el.Location.Width
		if width <= 0 {
			width = ColumnWidth
		}
		right := el.Location.Position.X + width

		var into *band
		for _, b := range bands {
			if math.Abs(b.y-top) <= rowTolerance {
				into = b
				break
			}
		}
		if into == nil {
			bands = append(bands, &band{y: top, right: right, n: 1})
			continue
		}
		into.n++
		if right > into.right {
			into.right = right
		}
		if top < into.y {
			into.y = top // the row sits at its topmost member
		}
	}

	var best *band
	for _, b := range bands {
		if best == nil || b.n > best.n || (b.n == best.n && b.y < best.y) {
			best = b
		}
	}
	if best == nil {
		return 0, 0, false
	}
	next := best.right + Gap
	if next > maxRowWidth {
		return 0, 0, false // long enough; a new band is kinder than more scrolling
	}
	return best.y, next, true
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

// placesOnCanvas reports whether an action needs a coordinate: it creates
// something, and it puts it straight onto the board rather than inside a
// container.
func placesOnCanvas(a *Action, scope *BoardScope, p *Plan) bool {
	// ActPlace already carries a computed position from ComputeArrangement.
	// Re-laying it out here would overwrite the arrangement with the default
	// shelf pack, which is the opposite of what was asked for.
	if a.Kind == ActPlace {
		return false
	}
	if !a.Kind.Creates() {
		return false
	}
	// The same reason, arriving by the other door: arrange can now reach
	// elements the run created, and it writes the computed box onto the create
	// itself. Re-packing it here would replace a flow the person asked for with
	// the default shelf rows.
	if a.Position != nil {
		return false
	}
	if a.Section == string(sectionUnsorted) {
		return false // the tray is a list, not a canvas
	}
	// ANY board is a canvas: the run's own, one this plan is creating, or one
	// that already existed and the scope can now see into.
	//
	// The last was missing, and it is the case the agent hits most: organizing a
	// board is the first thing it does well, and every run after that files into
	// the sub-boards it made. The committed op for one of those columns was
	// {parentId, section, index:0} — no position, no width — so the board
	// rendered width-0 columns scattered over the real ones.
	if a.ParentID == scope.Board.ID {
		return true
	}
	// Deliberately boards only. A COLUMN's children are ordered by index, not
	// placed, and giving one a coordinate would be a second, contradictory answer
	// to "where does this go".
	if el, ok := scope.Elements[a.ParentID]; ok && el != nil && el.Type == domain.TypeBoard {
		return true
	}
	return createsBoard(p, a.ParentID)
}

// createsBoard reports whether this plan brings the given board into being.
// A column or a note is NOT a canvas: its children are ordered, not placed.
func createsBoard(p *Plan, id string) bool {
	if p == nil || id == "" {
		return false
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ElementID == id {
			return a.Kind == ActCreateBoard
		}
	}
	return false
}

func snapTo(v, step float64) float64 { return math.Round(v/step) * step }
