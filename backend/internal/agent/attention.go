package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"qomranote/backend/internal/domain"
)

// Where the budget gets spent, and what the edge of it says.
//
// Two findings, one policy, deliberately in one file because they are the same
// decision asked on two axes and implementing them apart means writing the
// admission rule twice.
//
//   - The elision was scrupulously honest and completely unaimed. `admitChildren`
//     spent its allowance in CHILD ORDER and the frontier walked breadth-first by
//     level, so the 400 elements the model saw were the ones the tree happened to
//     reach first. Asked about the budget, a run read 25 casting cards in full and
//     the words "and 40 more inside" from the budget column.
//   - Fidelity followed TREE POSITION rather than ATTENTION. The person's
//     selection and the region they are looking at are the strongest statement
//     they can make about what matters, and neither reached the admission
//     decision at all.
//
// Same total budget, aimed. No extra reads, no model calls: every input is
// already in hand by the time the decision is made.

// Attention weights. Ordered by how strongly the signal states "this one".
//
// A selection is a person pointing. A viewport is a person looking. A lexical
// hit is the harness guessing. They are worth exactly that much, in that order,
// and the guess must never be able to outrank the pointing.
const (
	attnSelected = 1000
	attnViewport = 200
	attnLexical  = 10
)

// attentionOrder sorts a frontier level so the containers that matter to THIS
// request are served before the ones that merely came first in the tree.
//
// It permutes kidsOf alongside frontier, because the two are index-linked and a
// sort that moved one without the other would hand every container somebody
// else's children — which is the sort of bug that produces a plausible-looking
// digest describing a board that does not exist.
//
// Reading order remains the tie-break, so a board with no viewport, no selection
// and an intent that matches nothing compiles byte-identically to what it
// compiled to before this existed. That property is what keeps the prompt cache
// hitting and what makes this safe to land.
func attentionOrder(frontier []*domain.Element, kidsOf [][]*domain.Element, task TaskSpec, selected map[string]bool) {
	if len(frontier) < 2 {
		return
	}
	terms := intentTerms(task.Intent)
	score := make(map[string]int, len(frontier))
	rank := make(map[string]int, len(frontier))
	for i, c := range frontier {
		score[c.ID] = attentionScore(c, kidsOf[i], terms, task.Viewport, selected)
		rank[c.ID] = i
	}
	idx := make([]int, len(frontier))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ca, cb := frontier[idx[a]], frontier[idx[b]]
		if score[ca.ID] != score[cb.ID] {
			return score[ca.ID] > score[cb.ID]
		}
		return rank[ca.ID] < rank[cb.ID]
	})
	outF := make([]*domain.Element, len(frontier))
	outK := make([][]*domain.Element, len(kidsOf))
	for newPos, oldPos := range idx {
		outF[newPos] = frontier[oldPos]
		outK[newPos] = kidsOf[oldPos]
	}
	copy(frontier, outF)
	copy(kidsOf, outK)
}

// attentionScore is what one container is worth to this request.
//
// The subtree contributes as well as the title, because "the budget" names a
// column called "Finance" whose cards all say budget — a title-only score misses
// exactly the container the question is about.
func attentionScore(c *domain.Element, kids []*domain.Element, terms map[string]bool, vp *Viewport, selected map[string]bool) int {
	score := 0
	if selected[c.ID] {
		score += attnSelected
	}
	for _, k := range kids {
		if selected[k.ID] {
			// One selected card inside a column makes that column the subject, but
			// it is a weaker statement than selecting the column itself.
			score += attnSelected / 4
			break
		}
	}
	if vp.Valid() && c.Location.Section == domain.SectionCanvas && withinViewport(c, vp) {
		score += attnViewport
	}
	if len(terms) > 0 {
		score += attnLexical * overlapCount(terms, c.Title())
		hits := 0
		for _, k := range kids {
			if overlapCount(terms, textOf(k)) > 0 {
				hits++
			}
			if hits == 5 {
				break // enough to rank; counting the rest buys nothing
			}
		}
		score += attnLexical * hits
	}
	return score
}

// withinViewport reports whether an element's origin lies in the region the
// person is looking at. Origin rather than a full rectangle intersection: a
// container's stored width is frequently absent and a false precision here would
// rank on a number the board does not actually carry.
func withinViewport(el *domain.Element, vp *Viewport) bool {
	p := el.Location.Position
	return p.X >= vp.X && p.X <= vp.X+vp.Width && p.Y >= vp.Y && p.Y <= vp.Y+vp.Height
}

// intentTerms reduces the request to the words worth matching on.
//
// Stopworded and length-floored, because "the", "this" and "and" match
// everything and a score that every container earns is not a ranking. Nothing
// stemmed: a stemmer that gets one word wrong reorders the whole digest, and the
// win over exact prefix matching does not pay for that risk.
func intentTerms(intent string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(intent), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(f)) < 4 || emptyWords[f] {
			continue
		}
		out[f] = true
	}
	return out
}

// overlapCount counts how many of the request's terms appear in a piece of text.
func overlapCount(terms map[string]bool, text string) int {
	if text == "" || len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	n := 0
	for t := range terms {
		if strings.Contains(lower, t) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Summarised elision
// ---------------------------------------------------------------------------

// Elision is what a container held that the page could not print, summarised
// from the children the walk had ALREADY LOADED before the allowance ran out.
//
// The budget edge used to be a hole: "… and 40 more inside" is a fact about the
// harness, not about the board, and a model reading it knows only that it cannot
// see. The same forty elements are sitting in memory when the decision is taken,
// so describing them costs one pass and turns "I cannot see" into "I know
// roughly what is there" — which is the difference between a run that reads a
// board and one that reads around it.
type Elision struct {
	// Count is how many children were left off the page.
	Count int
	// Types is the breakdown by element type, most structural first.
	Types map[domain.ElementType]int
	// Earliest / Latest bound the dates found on the elided material.
	Earliest, Latest string
	// Unassigned and Overdue count the tasks nobody owns and the ones past due.
	Unassigned, Overdue int
	// Done counts ticked tasks, so "40 more" on a checklist can say how much of
	// it is already finished rather than making the run open it to find out.
	Done int
	// Unsorted counts how many of the cut children were in the tray rather than
	// on the canvas. A queue's LENGTH is the fact that decides whether to act on
	// it, so a tray that overflowed the budget must still be able to state how
	// long it is.
	Unsorted int
}

// noteElided folds one skipped child into a container's rollup.
func (s *BoardScope) noteElided(containerID string, k *domain.Element, now time.Time) {
	s.Elided[containerID]++
	if s.ElidedFacts == nil {
		s.ElidedFacts = map[string]*Elision{}
	}
	e := s.ElidedFacts[containerID]
	if e == nil {
		e = &Elision{Types: map[domain.ElementType]int{}}
		s.ElidedFacts[containerID] = e
	}
	e.Count++
	e.Types[k.Type]++
	if k.Location.Section == domain.SectionUnsorted {
		e.Unsorted++
	}
	if k.Type == domain.TypeTask {
		if done, _ := k.Content["done"].(bool); done {
			e.Done++
		}
		if who, _ := k.Content["assigneeId"].(string); who == "" {
			e.Unassigned++
		}
	}
	due, _ := k.Content["reminderAt"].(string)
	if due == "" {
		due, _ = k.Content["dueAt"].(string)
	}
	if due == "" {
		return
	}
	if overdueOn(due, now) {
		e.Overdue++
	}
	if day := dayOf(due); day != "" {
		if e.Earliest == "" || day < e.Earliest {
			e.Earliest = day
		}
		if e.Latest == "" || day > e.Latest {
			e.Latest = day
		}
	}
}

// dayOf takes the calendar day off an RFC3339-ish timestamp without parsing it.
// A date range is a rollup, not a schedule: the failure mode of a strict parse
// here is a dropped fact, and the failure mode of a prefix is a slightly wrong
// one — and the sentence says "dates" either way.
func dayOf(ts string) string {
	if len(ts) >= 10 && ts[4] == '-' && ts[7] == '-' {
		return ts[:10]
	}
	return ""
}

// Summary renders the rollup as the clause that follows the count.
func (e *Elision) Summary() string {
	if e == nil || e.Count == 0 {
		return ""
	}
	types := make([]domain.ElementType, 0, len(e.Types))
	for t := range e.Types {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		if e.Types[types[i]] != e.Types[types[j]] {
			return e.Types[types[i]] > e.Types[types[j]]
		}
		return types[i] < types[j]
	})
	// The type breakdown uses the SAME vocabulary as elidedBreakdown — "7 cards,
	// 3 boards", not "mostly CARD". Two phrasings for one fact is how a reader
	// learns that one of them means something different, and the plural form is
	// the one the human's own board tiles already use.
	var parts []string
	for _, t := range types {
		if len(parts) == maxHoldingKinds {
			break
		}
		parts = append(parts, plural(t, e.Types[t]))
	}
	switch {
	case e.Earliest != "" && e.Latest != "" && e.Earliest != e.Latest:
		parts = append(parts, "dates "+e.Earliest+" to "+e.Latest)
	case e.Earliest != "":
		parts = append(parts, "dated "+e.Earliest)
	}
	if e.Done > 0 {
		parts = append(parts, fmt.Sprintf("%d ticked", e.Done))
	}
	if e.Unassigned > 0 {
		parts = append(parts, fmt.Sprintf("%d unassigned", e.Unassigned))
	}
	if e.Overdue > 0 {
		parts = append(parts, fmt.Sprintf("%d overdue", e.Overdue))
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Lints
// ---------------------------------------------------------------------------

// maxLints bounds the standing defect list. Past a handful it stops being a list
// of problems and becomes a wall the model skims — the same reason the unmet
// list and the elision breakdown are both capped.
const maxLints = 6

// Lints are the board's own defects, computed server-side and stated before
// anybody asks.
//
// The fourth context type tldraw ships and this harness had no equivalent of.
// Everything here is knowable from the compiled scope and none of it was said,
// so the agent met a board with an empty column, an arrow pointing at a card
// somebody deleted and three overlapping stacks — and had to be TOLD there was a
// problem before it could see one. A collaborator notices.
//
// Stated as observations, never as a task list: a run asked to add one card must
// not go and repair the board because the digest mentioned a defect. That is the
// same restraint rule the REPORTING register is built on.
func (s *BoardScope) Lints() []string {
	if s == nil {
		return nil
	}
	var out []string
	// Containers holding nothing. The commonest real defect on a board an agent
	// has touched before, and the one the hollow check already treats as serious
	// when a plan CREATES it — so not seeing an existing one was inconsistent.
	var empty []string
	byParent := map[string]int{}
	for _, it := range s.Items {
		if it.ParentID != "" {
			byParent[it.ParentID]++
		}
	}
	for _, it := range s.Items {
		if it.Type != domain.TypeColumn && it.Type != domain.TypeTaskList {
			continue
		}
		if byParent[it.ID] > 0 || s.Elided[it.ID] > 0 {
			continue
		}
		// The database's own count wins over what the page shows: a container the
		// walk never opened is not an empty one.
		if live := s.ChildCounts[it.ID]; len(live) > 0 {
			total := int64(0)
			for _, n := range live {
				total += n
			}
			if total > 0 {
				continue
			}
		}
		empty = append(empty, fmt.Sprintf("%q", truncate(it.Text, 30)))
	}
	if len(empty) > 0 {
		sort.Strings(empty)
		if len(empty) > 4 {
			empty = empty[:4]
		}
		out = append(out, fmt.Sprintf("%s %s nothing in %s",
			strings.Join(empty, ", "), plural(domain.TypeColumn, len(empty)), pick(len(empty) == 1, "it", "them")))
	}
	// Cards sitting exactly on top of each other. A person reading the board sees
	// one card; the agent reading the digest sees two, and "why is this duplicated"
	// is the wrong question to reach for when the real answer is a dragging
	// accident.
	if n := overlapCount2(s); n > 0 {
		out = append(out, fmt.Sprintf("%d element(s) sit almost exactly on top of another one — "+
			"a dragging accident, not a duplicate", n))
	}
	// Off-palette colours the person themselves introduced. Named because it is
	// the read half of the style rule the plan is measured against: a board with
	// eleven one-off colours has no palette and the agent should not invent one.
	inUse := map[string]bool{}
	for _, it := range s.Items {
		if it.Color != "" {
			inUse[it.Color] = true
		}
	}
	if len(inUse) > 6 {
		out = append(out, fmt.Sprintf("%d different card colours are in use — there is no palette "+
			"here to match, so leave colour alone unless asked", len(inUse)))
	}
	if len(out) > maxLints {
		out = out[:maxLints]
	}
	return out
}

// overlapCount2 counts elements whose canvas origins are within a few pixels of
// another's. Named for what it counts rather than for what it is not; the coarse
// grid the digest already uses is too coarse for this — two cards in the same
// cell are neighbours, two cards at the same coordinate are a mistake.
func overlapCount2(s *BoardScope) int {
	const tolerance = 8.0
	type pt struct{ x, y float64 }
	seen := map[string][]pt{}
	n := 0
	for _, el := range s.Elements {
		if el == nil || el.Location.Section != domain.SectionCanvas || el.Type == domain.TypeLine {
			continue
		}
		p := pt{el.Location.Position.X, el.Location.Position.Y}
		// The origin is (0,0) for everything that has never been placed, which is
		// most of what sits inside a column. Counting those as overlapping would
		// report every tidy board as broken.
		if p.x == 0 && p.y == 0 {
			continue
		}
		parent := el.Location.ParentID
		for _, q := range seen[parent] {
			if abs(q.x-p.x) <= tolerance && abs(q.y-p.y) <= tolerance {
				n++
				break
			}
		}
		seen[parent] = append(seen[parent], p)
	}
	return n
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// lintBlock renders the standing defect list, or "" on a clean board.
func (s *BoardScope) lintBlock() string {
	lints := s.Lints()
	if len(lints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nWHAT IS ALREADY WRONG HERE (observations about the board as it stands — " +
		"NOT a task list; fix these only if the request is about them):\n")
	for _, l := range lints {
		b.WriteString("- " + l + "\n")
	}
	return b.String()
}
