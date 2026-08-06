package agent

import (
	"fmt"
	"sort"
	"strings"

	"qomranote/backend/internal/domain"
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
	LabelDestinations(p, scope)
	OrderPlan(p, scope)

	kids := childIndex(p)
	var items []placedItem
	var canvases []string
	byCanvas := map[string][]placedItem{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Position == nil {
			continue
		}
		it := placedItem{a: a, children: len(kids[a.ElementID])}
		items = append(items, it)
		if _, seen := byCanvas[a.ParentID]; !seen {
			canvases = append(canvases, a.ParentID)
		}
		byCanvas[a.ParentID] = append(byCanvas[a.ParentID], it)
	}
	if len(items) == 0 {
		return ""
	}

	// One block per CANVAS, because a coordinate only compares with others from
	// the same one. Layout now places elements on nested boards as well as on the
	// root, and measuring across all of them subtracts two numbers from different
	// coordinate systems: a plan whose canvases are each one screen wide would
	// describe itself as five screens wide, and its rows would pair a column on
	// the root with one inside a sub-board because both happen to sit at y=200.
	var b strings.Builder
	widest, totalRows := 0, 0
	for i, canvas := range canvases {
		if i > 0 {
			b.WriteString("\n")
		}
		width, rows := renderCanvas(&b, byCanvas[canvas], len(canvases) > 1)
		if width > widest {
			widest = width
		}
		totalRows += rows
	}

	if obs := observations(items, widest, totalRows); len(obs) > 0 {
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

// RenderContainmentTree describes a plan by WHERE things go rather than by
// where they sit, and returns "" only for a plan that files nothing anywhere.
//
// The review turn used to be gated on RenderSelfView, and RenderSelfView
// describes geometry. A plan that files everything into columns and sub-boards
// has no geometry the root canvas can show, so it rendered as the empty string —
// and the entire quality loop behind that gate, the STOP nudge included, was
// skipped for exactly the runs that had built the most out of sight. The guard
// written for the eighteen-empty-columns plan could not see the plan it was
// written for.
//
// Containment is the axis that is always there: everything a plan stages goes
// somewhere, whether or not that somewhere has coordinates.
//
//	▣ Pre-Production › Casting (3 item(s))
//	▣ Pre-Production › Locations (empty)
func RenderContainmentTree(p *Plan, scope *BoardScope) string {
	if p == nil || scope == nil {
		return ""
	}
	rootID := ""
	if scope.Board != nil {
		rootID = scope.Board.ID
	}
	// Titles of containers this plan creates, so a card filed into a column
	// staged three steps ago still names it. Same resolution LabelDestinations
	// does; the difference is that this one walks the whole chain up, because a
	// single name ("Casting") does not say which board it is inside.
	staged := map[string]*Action{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind.Creates() {
			staged[a.ElementID] = a
		}
	}

	holds := map[string]int{}
	var order []string
	see := func(id string) {
		if _, ok := holds[id]; !ok {
			holds[id] = 0
			order = append(order, id)
		}
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		// A container this plan creates is a node even when nothing lands in it.
		// Those are the rows worth reading.
		if a.Kind.Creates() && a.Kind.Container() {
			see(a.ElementID)
		}
		if a.ParentID == "" {
			continue
		}
		see(a.ParentID)
		holds[a.ParentID]++
	}
	if len(order) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("CONTAINMENT — where this plan puts everything:\n")
	for _, id := range order {
		path := containmentPath(id, rootID, staged, scope)
		if path == "" {
			continue
		}
		// "(empty)" rather than "(0 item(s))". The whole point of listing a
		// container nothing points at is that a reader should trip over it.
		if n := holds[id]; n == 0 {
			fmt.Fprintf(&b, "  ▣ %s (empty)\n", path)
		} else {
			fmt.Fprintf(&b, "  ▣ %s (%d item(s))\n", path, n)
		}
	}
	return b.String()
}

// containmentPath names an element by its whole chain from the run's board down,
// resolving through containers this plan is creating and containers already on
// the board alike.
func containmentPath(id, rootID string, staged map[string]*Action, scope *BoardScope) string {
	if id == rootID {
		return "this board"
	}
	var parts []string
	// Bounded because a plan can parent a container to a container it also
	// parents back — a cycle the compiler rejects later, and an infinite loop
	// here if this walked freely.
	for cur, depth := id, 0; cur != "" && cur != rootID && depth < maxContainmentDepth; depth++ {
		name, parent := "", ""
		if a, ok := staged[cur]; ok {
			name, parent = a.Title, a.ParentID
			if name == "" {
				name = truncate(a.Text, 24)
			}
		} else if el, ok := scope.Elements[cur]; ok && el != nil {
			name, parent = el.Title(), el.Location.ParentID
		}
		if name == "" {
			name = "(untitled)"
		}
		parts = append([]string{name}, parts...)
		cur = parent
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " › ")
}

// maxContainmentDepth bounds the walk up. Deeper than the scope's own depth cap,
// so it never truncates a real path — it only stops a cycle.
const maxContainmentDepth = 8

// placedItem is one element with a coordinate, plus what the plan puts in it.
type placedItem struct {
	a        *Action
	children int
}

// renderCanvas writes one canvas's arrangement and reports how wide it got and
// how many rows it took.
//
// named is set only when the plan writes to more than one canvas: on the common
// single-canvas plan, saying WHICH board is noise about the only board there is.
func renderCanvas(b *strings.Builder, items []placedItem, named bool) (width, rowCount int) {
	minX, maxX := 0.0, 0.0
	rows := map[float64]int{}
	for i, it := range items {
		if i == 0 || it.a.Position.X < minX {
			minX = it.a.Position.X
		}
		if i == 0 || it.a.Position.X+it.a.Position.Width > maxX {
			maxX = it.a.Position.X + it.a.Position.Width
		}
		rows[it.a.Position.Y]++
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

	where := ""
	if named {
		// Destination is already the container's name, resolved for elements the
		// board holds and for ones this plan is creating alike. Empty means the
		// run's own board, which LabelDestinations deliberately leaves unnamed.
		name := items[0].a.Destination
		if name == "" {
			name = "this board"
		}
		where = " on " + name
	}
	width = int(maxX - minX)
	fmt.Fprintf(b, "ARRANGEMENT%s — %d px wide · %d row(s)\n", where, width, len(ys))

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
		fmt.Fprintf(b, "  row %d  %s\n", rowOf[y], strings.Join(line, "  "))
	}
	return width, len(ys)
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
// So it fires at most once, on ANY non-empty plan, and it asks a SPECIFIC
// question. "Is this good?" reliably produces agreement with whatever was just
// built; "name the weakest grouping" produces information, because naming
// nothing is a visibly different answer from naming something.
//
// It used to fire only when there was an ARRANGEMENT to see, and that gate was
// the whole quality loop's off switch. RenderSelfView returns "" when nothing
// lands on the root canvas, so a run that filed everything into sub-boards and
// columns — which is every run after the first organizing pass — skipped
// MEASURED, the critique, the register mismatch and the STOP nudge together. It
// also burned its one allowed review on the empty string, so nothing could have
// fired later either. Judgement is now decoupled from geometry: the VIEW
// degrades, the check does not.
func (s *staging) reviewTurn(stepsLeft int) string {
	if len(s.plan.Actions) == 0 {
		return ""
	}
	// Once. A second forced look costs a whole model turn on every run that
	// triggers it, and the plan that most needs fixing is the one with the
	// least budget left. A shell is refused at FINISH instead — in-turn, where
	// the model can act without paying for another round trip.
	if s.reviews >= 1 {
		return ""
	}
	// Never spend the last step on a review. A run that reviews and then has no
	// turn left to act on what it saw has paid for the insight and thrown it
	// away — and would report a finished plan as incomplete.
	if stepsLeft < 2 {
		return ""
	}
	s.reviews++

	// Three renders, in descending order of what they can show. Geometry when
	// there is geometry; containment when the plan files rather than places;
	// the bare list of decisions when it does neither, which is a plan of pure
	// renames and edits.
	view := RenderSelfView(s.plan, s.scope)
	if view == "" {
		view = RenderContainmentTree(s.plan, s.scope)
	}
	if view == "" {
		view = "WHAT YOU HAVE STAGED\n" + describePlan(s.plan)
	}
	// The review is a step of the run and costs a model turn, so it belongs in
	// the journal like every other step. Reusing step.finished rather than
	// minting an event type keeps the client's renderer untouched — a new type
	// would show up as an unhandled row in the events stream.
	if s.emit != nil {
		s.emit(EvStepFinished, "reviewing the plan before it reaches the person",
			map[string]any{"review": true, "actions": len(s.plan.Actions)})
	}

	const preamble = "Before this reaches the user, here is what you have actually built:\n\n"

	// Sufficiency is asked FIRST, and with NUMBERS.
	//
	// The review used to ask the model to "count what you staged" and judge
	// whether that was a complete answer — the weakest possible check, because
	// the run that produced six empty column headings was perfectly satisfied
	// with six empty column headings. Something asked to audit itself in prose
	// agrees with itself. Shown "12 of 60 changes · 0 new cards" it has a fact
	// to argue with.
	quality := MeasurePlan(s.plan, s.scope, s.task.Budget)
	// What KIND of answer the wording asked for. The size floor is nonsense for
	// a question — one note is a complete answer to "what is missing?" — and a
	// check that scolds a good answer is one people learn to ignore.
	want := expectationOf(s.task.Intent)
	measured := "\n" + quality.Report() + "\n"
	// The measurements know nothing about WHERE things go — PlanQuality counts a
	// plan, not a destination — so the shape critiques that need the scope are
	// appended here rather than forced into CritiqueFor.
	// Conformance rides the SAME MEASURED block, which is the point: the prompt
	// half of house style has failed three times in this codebase's history, and
	// a measured line in a section the model already answers is the only channel
	// that has moved it.
	weak := append(quality.CritiqueFor(want), shellCritique(s.plan, s.scope)...)
	weak = append(weak, ConformanceCritique(s.plan, s.scope)...)
	if len(weak) > 0 {
		measured += "\nWHAT IS WEAK ABOUT THAT:\n"
		for _, line := range weak {
			measured += "- " + line + "\n"
		}
	}
	// And against the REQUEST, not only against itself. A plan can be well
	// measured and still answer a different question than the one asked —
	// which is how "what is missing from this plan?" came back as fourteen
	// notes plus two moves and a rename.
	if mismatch := want.Mismatch(s.plan, quality); mismatch != "" {
		measured += "\nTHIS MAY NOT BE THE QUESTION THAT WAS ASKED:\n" + mismatch + "\n"
	}

	// A structure with nothing in it takes the LAST word, because last is what
	// gets acted on, and it is a bigger problem than any arrangement: an empty
	// column is not a weak grouping, it is a label. It replaces the closing
	// questions rather than joining them — "name the weakest grouping" invites a
	// run whose real problem is emptiness to go and adjust a heading.
	//
	// The measurements still ride above it. They were previously dropped on
	// exactly these plans, which meant the run with the worst numbers was the one
	// never shown any.
	if names := hollowContainers(s.plan); len(names) > 0 {
		return preamble + view + measured + "\n" + hollowNudge(names)
	}
	// Scope creep on a follow-up earns the last word for the same reason: the
	// authoring close says "is this a sketch? put the real work in", which is
	// the OPPOSITE instruction — and the live model obeys whichever comes last.
	// The directive was first placed mid-message, above the closing questions,
	// and went 1 for 3; this is the D5 lesson again, found in a third home.
	if creep := followUpOverreach(s.task.Intent, s.scope, s.plan); creep != "" {
		return preamble + view + measured + "\n" + creep
	}
	return preamble + view + measured + closingQuestions(want)
}

// shellCritique names the boards this plan is about to fill with a single
// column carrying the board's own name.
//
// Observed: a board `Pre-Production` holding a column `Pre-Production`, twice
// over. Every individual step was right — the board was the right home for that
// material, the column was already called that — and the result is a door into a
// room containing one box labelled with the room's name. The person opens the
// board and has to open one more thing before seeing anything.
//
// A critique and not a refusal, because the same shape is occasionally correct:
// a board can hold "Casting" beside "Locations" and one of them may share the
// board's name for a real reason. Naming it is enough; the model can act or
// explain.
func shellCritique(p *Plan, scope *BoardScope) []string {
	if p == nil || scope == nil {
		return nil
	}
	// Boards this plan is creating, so a column filed into one staged three
	// steps ago is judged by the name the plan gives it.
	staged := map[string]string{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind == ActCreateBoard {
			staged[a.ElementID] = a.Title
		}
	}

	var out []string
	said := map[string]bool{}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind != ActMove || a.ParentID == "" || said[a.ParentID] {
			continue
		}
		el, ok := scope.Elements[a.ElementID]
		if !ok || el == nil || el.Type != domain.TypeColumn {
			continue
		}
		boardName, isBoard := staged[a.ParentID]
		if !isBoard {
			board, ok := scope.Elements[a.ParentID]
			if !ok || board == nil || board.Type != domain.TypeBoard {
				continue
			}
			boardName = board.Title()
		}
		// Normalized, and equality rather than the looser sibling test: "Casting"
		// inside "Pre-Production Casting" is a proper column, and only an exact
		// echo of the board's own name is the redundant shell.
		if !sameNameExactly(boardName, el.Title()) {
			continue
		}
		said[a.ParentID] = true
		out = append(out, fmt.Sprintf(
			"Board %q will contain a single column with its own name — move the CARDS into "+
				"the board and leave the redundant shell behind, or rename the column to what "+
				"distinguishes it from the board.", boardName))
	}
	return out
}

// closingQuestions is what the review turn asks LAST, and last is what gets
// acted on.
//
// They used to be unconditional, and they are written for authoring: "is this a
// complete answer or a sketch — go back and put the real work in". Appended to
// a REPORTING run that had just been told "a question was asked, withdraw those
// edits", they said the opposite, and the opposite is what the model did.
//
// Asked "what is missing from this plan?", a run left the right comment, read
// the mismatch, then created a column and moved two cards into it — and
// explained itself in `unmet` with "I previously only left a comment about
// this, rather than acting on it". It had been told to act, two paragraphs
// after being told not to.
func closingQuestions(want expectation) string {
	if want.Reporting {
		return "\nTwo questions, in this order.\n" +
			"1. Does the answer actually say the thing? A comment that names the gap is " +
			"the answer; a comment that says \"there are some gaps\" is not.\n" +
			"2. Is anything in this plan a CHANGE to the board rather than the answer? " +
			"A question was asked. Moves, new columns and renames are a second answer " +
			"nobody asked for — withdraw them with undo_staged.\n\n" +
			"Do not add structure to make the plan look more substantial. For a question, " +
			"one note or one comment carrying the answer IS the complete plan."
	}
	return "\nTwo questions, in this order.\n" +
		"1. Is this a complete answer, or a sketch of one? If the person asked you to " +
		"design or set something up and you produced mostly headings, the arrangement is " +
		"not the problem — go back and put the real work in them.\n" +
		"2. Name the weakest grouping in one clause.\n\n" +
		"If either is worth changing, change it with more tool calls now. If the plan is " +
		"right as it stands, call finish again and say in your summary why this shape " +
		"suits the material."
}
