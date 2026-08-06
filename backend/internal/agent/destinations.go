package agent

import "qomranote/backend/internal/domain"

// LabelDestinations fills in, for every action, where the thing is going — as a
// name a person recognises.
//
// This is the fix for the least reviewable thing in the product. Ask the agent
// to file thirty cards into four columns — the canonical request — and the
// review used to read:
//
//	Move   Check competitor pricing
//	Move   Book the venue
//	Move   Email finance
//
// A move's summary is the card's own text, and only actions landing directly on
// the canvas get a ghost, so anything filed INTO a container had no position to
// draw either. The single fact needed to say yes — which card went where — was
// the one fact the review did not contain.
//
// Resolved server-side because only the server knows both halves: the titles of
// elements already on the board, and the titles of containers this same plan is
// about to create. A client could resolve the first and would guess the second.
func LabelDestinations(p *Plan, scope *BoardScope) {
	if p == nil || scope == nil {
		return
	}
	// Titles of containers this plan creates, so an action parented to a column
	// staged three steps ago still names it.
	staged := make(map[string]string, len(p.Actions))
	for _, a := range p.Actions {
		if a.Kind.Creates() && a.Title != "" {
			staged[a.ElementID] = a.Title
		}
	}

	rootID := ""
	if scope.Board != nil {
		rootID = scope.Board.ID
	}

	for i := range p.Actions {
		a := &p.Actions[i]
		a.Destination = ""
		if a.ParentID == "" {
			continue
		}
		// Landing on the board itself is not a destination worth naming — the
		// ghost already shows exactly where, and "→ this board" is noise on
		// every row.
		if a.ParentID == rootID {
			if a.Section == string(domain.SectionUnsorted) {
				a.Destination = unsortedName
			}
			continue
		}
		if title, ok := staged[a.ParentID]; ok {
			a.Destination = title
			continue
		}
		if el, ok := scope.Elements[a.ParentID]; ok {
			if title := el.Title(); title != "" {
				a.Destination = title
			}
		}
	}
}

// unsortedName is what the Unsorted tray is called in the product. Filing to it
// IS a destination — it is the one place on the board that is not the canvas.
const unsortedName = "Unsorted"

// Risk bands, the vocabulary the review list collapses on.
const (
	// RiskTouchesExisting is anything aimed at material that predates this run.
	// Never collapsible: approving it approves a change to something somebody
	// already made a decision about.
	RiskTouchesExisting = "touches-existing"
	// RiskStructural is a new container that will hold existing material.
	// Approving it approves a re-filing, not merely an addition.
	RiskStructural = "structural"
	// RiskAdditive is a new element made of nothing that was there before.
	RiskAdditive = "additive"
)

// BandPlan decides how much of a reviewer's attention each row deserves.
//
// Both prior architecture documents lean on human review as the load-bearing
// safety guarantee, and human-factors work says that guarantee decays exactly as
// the system gets good: reviewers anchor on the first rows, scrutiny falls with
// list length, approval becomes the default. A 41-row flat list with one Apply
// button is unreviewable by construction. Collapsing the additive rows is what
// keeps the guarantee real, so what counts as additive has to be decided from
// committed facts.
//
// Server-side because only here is the whole subtree in hand. The client's own
// derivation is correct as far as it can see and deliberately fails SAFE past
// that — an element on a nested board it never loaded is treated as existing
// material — so a run that filed into four sub-boards showed every row at full
// weight and the collapse did nothing at all. It stays as the fallback for a
// plan made before this field existed.
//
// Computed from the board and the plan, never from the model: a band a model can
// argue for is one more thing to argue with, and this one decides what a person
// is allowed not to read.
func BandPlan(p *Plan, scope *BoardScope) {
	if p == nil || scope == nil {
		return
	}
	for i := range p.Actions {
		p.Actions[i].Risk = bandOf(&p.Actions[i], scope)
	}
}

func bandOf(a *Action, scope *BoardScope) string {
	// Deleting, and anything aimed at something that was here before this run,
	// is never collapsible. That is the invariant, not a heuristic.
	if a.Kind.Destructive() || !a.Kind.Creates() {
		return RiskTouchesExisting
	}
	if a.Kind.Container() {
		return RiskStructural
	}
	// A create whose id already names a live element is a repair of existing
	// material rather than an addition — which is how a retried apply's second
	// pass looked additive while overwriting what the first one wrote.
	if el, ok := scope.Elements[a.ElementID]; ok && el != nil && !el.IsDeleted() {
		return RiskTouchesExisting
	}
	// A locked element anywhere in the destination chain. The lock is a person
	// saying "settled"; putting something inside it is a change to their
	// decision even though nothing existing is edited.
	for id, depth := a.ParentID, 0; id != "" && depth < 8; depth++ {
		el, ok := scope.Elements[id]
		if !ok || el == nil {
			break
		}
		if locked, _ := el.Content["locked"].(bool); locked {
			return RiskTouchesExisting
		}
		id = el.Location.ParentID
	}
	return RiskAdditive
}
