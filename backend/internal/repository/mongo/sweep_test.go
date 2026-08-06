package mongo

import (
	"testing"

	"qomranote/backend/internal/domain"
)

// The sweep decides which connectors are past saving. The decision is the part
// worth testing, and it is pure — the queries around it only assemble the
// elements it reads.
//
// The board this was written from: one organizing run filed four pairs of
// connected columns into different nested boards, and left four LINE elements
// on the root canvas joining columns that no longer share a canvas. Nothing was
// deleted, so the old sweep — which only looked for dead endpoints — counted
// zero, and four invisible arrows stayed in the graph.
func sweepGraph() map[string]*domain.Element {
	el := func(id, typ, parent string) *domain.Element {
		return &domain.Element{ID: id, Type: domain.ElementType(typ),
			Location: domain.Location{ParentID: parent, Section: domain.SectionCanvas}}
	}
	g := map[string]*domain.Element{}
	add := func(e *domain.Element) { g[e.ID] = e }
	add(el("root", "BOARD", ""))
	add(el("pre", "BOARD", "root"))
	add(el("post", "BOARD", "root"))
	add(el("scripting", "COLUMN", "pre"))
	add(el("casting", "COLUMN", "pre"))
	add(el("editing", "COLUMN", "post"))
	add(el("beat", "CARD", "scripting"))
	add(el("cut", "CARD", "editing"))
	add(el("loose-a", "CARD", "root"))
	add(el("loose-b", "CARD", "root"))
	return g
}

func line(id, parent, from, to string) *domain.Element {
	return &domain.Element{ID: id, Type: domain.TypeLine,
		Location: domain.Location{ParentID: parent},
		Content:  domain.Content{"fromId": from, "toId": to}}
}

func TestSweep_KeepsLinesThatStillDraw(t *testing.T) {
	graph := sweepGraph()
	lines := []*domain.Element{
		// Two loose cards on the root canvas: the ordinary connector.
		line("l1", "root", "loose-a", "loose-b"),
		// Two columns inside the same nested board, joined on that board.
		line("l2", "pre", "scripting", "casting"),
		// A card inside a column: it resolves on the board the column is on.
		line("l3", "pre", "beat", "casting"),
		// An arrow to a sub-board TILE, which sits on the canvas above it.
		line("l4", "root", "loose-a", "pre"),
	}
	if got := strandedConnectors(lines, graph); len(got) != 0 {
		t.Errorf("swept lines that render perfectly well: %v", got)
	}
}

func TestSweep_CatchesLinesAcrossTwoCanvases(t *testing.T) {
	graph := sweepGraph()
	lines := []*domain.Element{
		// The prod strand: two columns that ended up in different sub-boards,
		// with the line left behind on the root canvas.
		line("strand-1", "root", "scripting", "editing"),
		// Same split, one level down: cards inside those columns.
		line("strand-2", "root", "beat", "cut"),
		// Both endpoints share a canvas, but the line is parented elsewhere —
		// equally invisible, and what a half-done re-home would leave.
		line("strand-3", "root", "scripting", "casting"),
		// The old rule's cases still count.
		line("strand-4", "root", "loose-a", "gone"),
		line("strand-5", "root", "loose-a", ""),
	}
	got := strandedConnectors(lines, graph)
	if len(got) != len(lines) {
		t.Fatalf("swept %v, want all %d", got, len(lines))
	}
}

// A cycle in parent ids is bad data, not a reason to hang a maintenance job.
func TestSweep_BoundsABrokenAncestorChain(t *testing.T) {
	graph := map[string]*domain.Element{
		"a": {ID: "a", Type: domain.TypeCard, Location: domain.Location{ParentID: "b"}},
		"b": {ID: "b", Type: domain.TypeColumn, Location: domain.Location{ParentID: "a"}},
	}
	got := strandedConnectors([]*domain.Element{line("l", "root", "a", "b")}, graph)
	if len(got) != 1 {
		t.Errorf("a line hanging off a cycle should be swept, got %v", got)
	}
}
