package agent

import "qomranote/backend/internal/domain"

// Where a card sits INSIDE its container.
//
// The index was the action's position in the plan — a.Seq+1 — which has nothing
// to do with the container the card lands in. Two consequences, both visible on
// the first board that used it:
//
//	Intro          Shot 4 (1.5)  Shot 5 (2)  Shot 1 (3)  Shot 2 (4)  Shot 3 (5)
//	Process        Shot 4 (3)    Shot 5 (4)  … Shot 9 (8)
//	Impact         Shot 4 (9)    Shot 5 (10) Shot 6 (11)
//
// The new shots landed ABOVE the three already in Intro, because a running
// count over the whole plan produced numbers lower than what was already there.
// And the count ran ACROSS containers, so a card's position in one column was
// decided by how many cards happened to be staged for a different column first.
//
// Ordering is per container and it is relative: a card added to a column goes
// after what is already in it, in the order it was staged. That is what the
// prompt has always promised, and it was never true.

// OrderPlan assigns every action's index within its destination container.
//
// A pure pass over the plan, run alongside LayoutPlan and LabelDestinations —
// the three answer "where does this go" for the three audiences: the canvas,
// the reviewer, and the container.
func OrderPlan(p *Plan, scope *BoardScope) {
	if p == nil {
		return
	}
	// Where each container's numbering resumes. A container this plan creates
	// starts empty; one already on the board starts after its last child.
	next := map[string]float64{}

	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ParentID == "" || !ordersItsChildren(a, p, scope) {
			continue
		}
		cursor, seen := next[a.ParentID]
		if !seen {
			cursor = lastIndexIn(a.ParentID, scope) + 1
		}
		a.Index = cursor
		next[a.ParentID] = cursor + 1
	}
}

// ordersItsChildren reports whether this action needs an index — it puts
// something inside a container that ORDERS its contents, rather than onto a
// canvas that positions them.
func ordersItsChildren(a *Action, p *Plan, scope *BoardScope) bool {
	if a.Kind != ActMove && !a.Kind.Creates() {
		return false
	}
	if a.Kind == ActConnect {
		return false // a line is drawn between endpoints, not filed
	}
	// The tray is a LIST, whatever it hangs off. A board's canvas places by
	// coordinate and its Unsorted tray orders by index — the same parent id, two
	// different disciplines — so testing the parent alone sent every card filed
	// into the tray in with index 0 and let the database decide the queue's
	// order. `file_to` writes there, the ambient hint points at it, and it is the
	// one container in the product whose whole meaning is its sequence.
	if a.Section == string(domain.SectionUnsorted) {
		return true
	}
	// A canvas places by coordinate. Only containers order.
	if scope != nil && scope.Board != nil && a.ParentID == scope.Board.ID {
		return false
	}
	if createsBoard(p, a.ParentID) {
		return false
	}
	if scope != nil {
		if el, ok := scope.Elements[a.ParentID]; ok && el.Type == "BOARD" {
			return false
		}
	}
	return true
}

// lastIndexIn is the highest index already used inside a container, or 0 when
// nothing is in it. Reading the CURRENT contents is the whole point: appending
// without looking is how new cards landed on top of old ones.
func lastIndexIn(parentID string, scope *BoardScope) float64 {
	if scope == nil {
		return 0
	}
	max := 0.0
	for _, el := range scope.Elements {
		if el.Location.ParentID != parentID || el.IsDeleted() {
			continue
		}
		if el.Location.Index > max {
			max = el.Location.Index
		}
	}
	return max
}
