package agent

// Geometry for the two shapes a board cannot express by packing: a hierarchy
// and a process.
//
// Both are pure functions over (nodes, edges) → positions. That is not a style
// preference: the whole preview architecture rests on the server computing
// geometry once, deterministically, so the ghosts a person approves are placed
// identically to the transaction that commits. It is also why neither of these
// is force-directed — an iterative relaxation gives a different answer each time
// it runs, which would make the preview a rumour again.
//
// Sizes come in from the caller because a card's height depends on its text and
// only the caller knows whether it is measuring a staged action or an element
// already on the board.

// node is one box to place.
type node struct {
	id   string
	w, h float64
}

// edge is a directed relationship. Undirected callers pass either orientation.
type edge struct{ from, to string }

// Diagram spacing. Wider than the arrange gutters: a diagram is read by
// following lines between boxes, and lines need room to be followed.
const (
	tierGap    = 96 // between ranks / tree levels
	siblingGap = 48 // between boxes within a rank / among siblings
)

// ---------------------------------------------------------------------------
// Hierarchy
// ---------------------------------------------------------------------------

// layoutTree places a forest top-down: parents centred over their children,
// siblings left to right, no two boxes overlapping.
//
// Each subtree is allocated a horizontal slot as wide as it needs and drawn
// inside it. That is not the most compact packing — Reingold–Tilford threads
// contours together and squeezes adjacent subtrees closer — but it is O(n),
// obviously correct, and with cards this wide the difference is a gap nobody
// notices. Correct and legible beats clever and subtly wrong.
//
// Nodes unreachable from any root still get placed, in a row beneath the tree,
// because silently dropping something the person asked to arrange is worse than
// an imperfect arrangement.
func layoutTree(nodes []node, edges []edge, originX, originY float64) map[string]ColumnBox {
	out := make(map[string]ColumnBox, len(nodes))
	if len(nodes) == 0 {
		return out
	}
	byID := make(map[string]node, len(nodes))
	for _, n := range nodes {
		byID[n.id] = n
	}

	children, roots := rootedForest(nodes, edges)

	// Bottom-up: how much horizontal room each subtree needs.
	width := make(map[string]float64, len(nodes))
	var measure func(id string, depth int) float64
	measure = func(id string, depth int) float64 {
		if w, done := width[id]; done {
			return w
		}
		width[id] = byID[id].w // guards a cycle the forest builder missed
		own := byID[id].w
		if depth >= maxDiagramDepth {
			return own
		}
		total := 0.0
		for i, kid := range children[id] {
			if i > 0 {
				total += siblingGap
			}
			total += measure(kid, depth+1)
		}
		if total > own {
			width[id] = total
		} else {
			width[id] = own
		}
		return width[id]
	}
	for _, r := range roots {
		measure(r, 0)
	}

	// Top-down: hand each subtree its slot and centre the parent in it.
	rowBottom := map[int]float64{}
	var place func(id string, left, top float64, depth int)
	place = func(id string, left, top float64, depth int) {
		n := byID[id]
		slot := width[id]
		x := left + (slot-n.w)/2
		out[id] = ColumnBox{X: x, Y: top, Width: n.w}
		if bottom := top + n.h; bottom > rowBottom[depth] {
			rowBottom[depth] = bottom
		}
		if depth >= maxDiagramDepth {
			return
		}
		// Children sit one level down, sharing the parent's slot left to right.
		childTop := top + n.h + tierGap
		cursor := left
		for _, kid := range children[id] {
			place(kid, cursor, childTop, depth+1)
			cursor += width[kid] + siblingGap
		}
	}

	cursor := originX
	for _, r := range roots {
		place(r, cursor, originY, 0)
		cursor += width[r] + siblingGap*2 // separate trees read as separate
	}

	// Anything the forest could not reach.
	placeOrphans(nodes, out, originX, lowest(rowBottom)+tierGap)
	return out
}

// maxDiagramDepth stops a pathological chain from recursing without bound.
// Deeper than any hierarchy a person reads on a canvas. Distinct from
// maxTreeDepth in tools.go, which bounds a TEXT outline, not geometry.
const maxDiagramDepth = 12

// rootedForest turns edges into a parent→children map and finds the roots.
//
// A node keeps the FIRST parent claiming it, so a graph that is nearly a tree
// but has one extra edge still draws as a tree instead of failing. Cycles are
// broken the same way: whoever gets there first wins.
func rootedForest(nodes []node, edges []edge) (children map[string][]string, roots []string) {
	present := make(map[string]bool, len(nodes))
	order := make([]string, 0, len(nodes))
	for _, n := range nodes {
		present[n.id] = true
		order = append(order, n.id)
	}
	children = map[string][]string{}
	parent := map[string]string{}
	for _, e := range edges {
		if !present[e.from] || !present[e.to] || e.from == e.to {
			continue
		}
		if _, taken := parent[e.to]; taken {
			continue
		}
		if wouldCycle(parent, e.from, e.to) {
			continue
		}
		parent[e.to] = e.from
		children[e.from] = append(children[e.from], e.to)
	}
	// Insertion order, so the same input always draws the same tree.
	for _, id := range order {
		if _, hasParent := parent[id]; !hasParent {
			roots = append(roots, id)
		}
	}
	return children, roots
}

// wouldCycle reports whether making `from` the parent of `to` closes a loop.
func wouldCycle(parent map[string]string, from, to string) bool {
	for cur, hops := from, 0; cur != "" && hops < maxDiagramDepth*2; hops++ {
		if cur == to {
			return true
		}
		cur = parent[cur]
	}
	return false
}

// ---------------------------------------------------------------------------
// Process
// ---------------------------------------------------------------------------

// layoutFlow places a directed graph left to right in ranks: everything that
// can happen first in the leftmost column, what depends on it next, and so on.
//
// Sugiyama's three phases, reduced to what a board actually needs:
//
//  1. RANK by longest path from a source, so an edge never points backwards
//     unless the graph genuinely has a cycle.
//  2. ORDER within each rank by the median position of what feeds it, which is
//     the standard crossing-reduction heuristic and cheap.
//  3. POSITION by stacking each rank vertically, centred on the tallest.
//
// Cycles are tolerated: a back edge is simply not counted when ranking, so a
// loop draws as a line returning leftwards rather than failing to draw at all.
func layoutFlow(nodes []node, edges []edge, originX, originY float64) map[string]ColumnBox {
	out := make(map[string]ColumnBox, len(nodes))
	if len(nodes) == 0 {
		return out
	}
	byID := make(map[string]node, len(nodes))
	order := make([]string, 0, len(nodes))
	for _, n := range nodes {
		byID[n.id] = n
		order = append(order, n.id)
	}

	forward := forwardEdges(order, byID, edges)

	// 1. Rank: longest path, so a node sits to the right of everything it
	//    depends on rather than merely to the right of one of them.
	rank := map[string]int{}
	incoming := map[string][]string{}
	for _, e := range forward {
		incoming[e.to] = append(incoming[e.to], e.from)
	}
	var rankOf func(id string, depth int) int
	rankOf = func(id string, depth int) int {
		if r, done := rank[id]; done {
			return r
		}
		if depth > maxDiagramDepth*2 {
			return 0
		}
		best := 0
		for _, from := range incoming[id] {
			if r := rankOf(from, depth+1) + 1; r > best {
				best = r
			}
		}
		rank[id] = best
		return best
	}
	maxRank := 0
	for _, id := range order {
		if r := rankOf(id, 0); r > maxRank {
			maxRank = r
		}
	}

	// 2. Order within each rank by the median rank-position of its inputs.
	ranks := make([][]string, maxRank+1)
	for _, id := range order { // insertion order is the deterministic tiebreak
		ranks[rank[id]] = append(ranks[rank[id]], id)
	}
	pos := map[string]int{}
	for _, ids := range ranks {
		for i, id := range ids {
			pos[id] = i
		}
	}
	for sweep := 0; sweep < crossingSweeps; sweep++ {
		for r := 1; r <= maxRank; r++ {
			ids := ranks[r]
			key := make(map[string]float64, len(ids))
			for _, id := range ids {
				key[id] = medianOf(incoming[id], pos, float64(pos[id]))
			}
			stableSortBy(ids, func(a, b string) bool {
				if key[a] != key[b] {
					return key[a] < key[b]
				}
				return pos[a] < pos[b]
			})
			for i, id := range ids {
				pos[id] = i
			}
		}
	}

	// 3. Position: rank → x, order → y, each rank centred against the tallest.
	rankX := originX
	tallest := 0.0
	for _, ids := range ranks {
		h := 0.0
		for i, id := range ids {
			if i > 0 {
				h += siblingGap
			}
			h += byID[id].h
		}
		if h > tallest {
			tallest = h
		}
	}
	for _, ids := range ranks {
		colWidth := 0.0
		colHeight := 0.0
		for i, id := range ids {
			if i > 0 {
				colHeight += siblingGap
			}
			colHeight += byID[id].h
			if w := byID[id].w; w > colWidth {
				colWidth = w
			}
		}
		y := originY + (tallest-colHeight)/2
		for _, id := range ids {
			n := byID[id]
			out[id] = ColumnBox{X: rankX + (colWidth-n.w)/2, Y: y, Width: n.w}
			y += n.h + siblingGap
		}
		rankX += colWidth + tierGap
	}
	return out
}

// crossingSweeps is how many median passes to run. Two gets most of the benefit
// on graphs this size; more is measurable only on graphs nobody puts on a board.
const crossingSweeps = 2

// forwardEdges drops the edges that close a cycle, so ranking terminates and a
// loop draws as a line pointing back rather than as a failure.
func forwardEdges(order []string, byID map[string]node, edges []edge) []edge {
	seen := map[string]int{} // 0 unvisited, 1 on the stack, 2 done
	adj := map[string][]string{}
	present := map[string]bool{}
	for id := range byID {
		present[id] = true
	}
	var kept []edge
	for _, e := range edges {
		if present[e.from] && present[e.to] && e.from != e.to {
			adj[e.from] = append(adj[e.from], e.to)
		}
	}
	back := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		seen[id] = 1
		for _, to := range adj[id] {
			switch seen[to] {
			case 0:
				walk(to)
			case 1:
				back[id+"\x00"+to] = true // an edge back onto the stack
			}
		}
		seen[id] = 2
	}
	for _, id := range order {
		if seen[id] == 0 {
			walk(id)
		}
	}
	for _, e := range edges {
		if !present[e.from] || !present[e.to] || e.from == e.to {
			continue
		}
		if back[e.from+"\x00"+e.to] {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

func medianOf(ids []string, pos map[string]int, fallback float64) float64 {
	if len(ids) == 0 {
		return fallback
	}
	vals := make([]int, 0, len(ids))
	for _, id := range ids {
		if p, ok := pos[id]; ok {
			vals = append(vals, p)
		}
	}
	if len(vals) == 0 {
		return fallback
	}
	stableSortInts(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return float64(vals[mid])
	}
	return float64(vals[mid-1]+vals[mid]) / 2
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

// placeOrphans lays out whatever the shape could not reach, in a row below it.
func placeOrphans(nodes []node, out map[string]ColumnBox, originX, top float64) {
	x := originX
	for _, n := range nodes {
		if _, placed := out[n.id]; placed {
			continue
		}
		out[n.id] = ColumnBox{X: x, Y: top, Width: n.w}
		x += n.w + siblingGap
	}
}

func lowest(rows map[int]float64) float64 {
	max := 0.0
	for _, v := range rows {
		if v > max {
			max = v
		}
	}
	return max
}

// stableSortBy is an insertion sort: stable, and the slices here are tiny.
// Written out rather than imported so the ordering is provably deterministic.
func stableSortBy(s []string, less func(a, b string) bool) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && less(s[j], s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func stableSortInts(s []int) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
