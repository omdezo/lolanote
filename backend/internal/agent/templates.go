package agent

import (
	"fmt"
	"strings"

	"qomranote/backend/internal/domain"
)

// Templates: a stencil is not content.
//
// The product has a template flag — the element shell toggles
// `content.isTemplate` — and nothing in the agent had ever heard of it. A
// template board nested in the workspace was admitted as ordinary content
// (it is a BOARD, therefore organizable), so "organise this board" would
// cheerfully reorganise a blank sprint template, rename its columns, or fill it
// in. Writing into a stencil is destructive in a way nothing prevented, and it
// is destructive twice over: the template is gone AND every future instantiation
// of it is wrong.
//
// Reads stay open on purpose. Reading a template is how the agent learns your
// conventions — the column names, the card shapes, the way you title things —
// and that is the entire value of having templates visible at all. So the rule
// is asymmetric and stated as such: read freely, never write.

// templateKey is the content flag the element shell toggles.
const templateKey = "isTemplate"

// archivedKey is JN17's status flag, written by the boards drawer.
const archivedKey = "archived"

// isTemplateElement reports whether this element is a stencil.
func isTemplateElement(el *domain.Element) bool {
	if el == nil {
		return false
	}
	v, _ := el.Content[templateKey].(bool)
	return v
}

// isArchivedElement reports whether this board has been put to bed.
//
// The same asymmetry as a template, reached from the opposite direction. A
// template is a stencil that must not become work; an archive is work that has
// stopped being work. Both are readable — reading a wrapped production is how
// the agent learns how the last one was run, which is most of the value of
// keeping it — and neither may be written into by a run.
//
// JN17 argues this is more urgent than it looks: the planner's own prompt uses
// "archive the stale stuff" as a worked example of a good request, so the model
// arrives already primed to treat these boards as fair game, and a wrapped
// episode reorganised by a well-meaning run is the exact loss archiving exists
// to prevent.
func isArchivedElement(el *domain.Element) bool {
	if el == nil {
		return false
	}
	v, _ := el.Content[archivedKey].(bool)
	return v
}

// markTemplates computes the protected set: every template in scope, plus
// everything inside one.
//
// Inheritance is the point. A template board's columns and cards carry no flag
// of their own — the person marked the board — so a guard that checked only the
// flagged element would refuse a rename of the template and permit a rewrite of
// every card in it. Run once over the compiled scope rather than inside the
// walk, because the walk admits a child before it has necessarily decided
// anything about the parent, and a rule about what may be WRITTEN has to be a
// property of the finished set.
func (s *BoardScope) markTemplates() {
	s.Templates = s.closeOverChildren(isTemplateElement)
	s.Archived = s.closeOverChildren(isArchivedElement)
}

// closeOverChildren computes a protected set: every element in scope the seed
// predicate names, plus everything beneath one.
//
// Shared by templates and archives because the inheritance argument is
// identical for both and getting it subtly different in two places is how one
// of them ends up protecting the container and permitting a rewrite of every
// card inside it.
func (s *BoardScope) closeOverChildren(seed func(*domain.Element) bool) map[string]bool {
	seeds := map[string]bool{}
	for id, el := range s.Elements {
		if seed(el) {
			seeds[id] = true
		}
	}
	// The run's OWN root is never protected, even when it is a template.
	//
	// The failure this guard exists for is the agent wandering into a stencil
	// nested in a workspace it was asked to organise. Somebody who opened their
	// sprint template and typed "add a Retro column" aimed at it deliberately,
	// and refusing that is the product overriding a direct instruction — which
	// would turn a safety rail into "the agent will not edit my templates at
	// all", the exact complaint that gets a guard deleted.
	//
	// The same reasoning carries to an archive: somebody who opened a wrapped
	// episode and asked for a change is asking on purpose, and "unarchive it
	// first" is a rule that teaches people to keep boards out of the archive.
	delete(seeds, s.Board.ID)
	if len(seeds) == 0 {
		return nil
	}
	// Closure over parent links, iterated to a fixed point: the scope map is not
	// in tree order, so one pass would miss a grandchild whose parent had not
	// been marked yet.
	for changed := true; changed; {
		changed = false
		for id, el := range s.Elements {
			if seeds[id] {
				continue
			}
			if seeds[el.Location.ParentID] {
				seeds[id] = true
				changed = true
			}
		}
	}
	return seeds
}

// IsTemplate reports whether writing to this id would write into a stencil.
func (s *BoardScope) IsTemplate(id string) bool {
	return id != "" && s.Templates != nil && s.Templates[id]
}

// IsArchived reports whether writing to this id would write into finished work.
func (s *BoardScope) IsArchived(id string) bool {
	return id != "" && s.Archived != nil && s.Archived[id]
}

// templateBlock tells the model what it may read and may not touch.
//
// Named explicitly rather than left implicit in a refusal, because a capability
// discovered only by being refused costs a turn every time — and because the
// useful half is the read: "these are your conventions, learn from them" is a
// better instruction than "these are off limits".
func (s *BoardScope) templateBlock() string {
	if len(s.Templates) == 0 {
		return ""
	}
	var names []string
	for id := range s.Templates {
		el := s.Elements[id]
		if el == nil && s.Board != nil && s.Board.ID == id {
			el = s.Board
		}
		// Only the stencils themselves are listed; their contents are implied
		// and listing them would spend the digest on a wall of ids.
		if el == nil || !isTemplateElement(el) {
			continue
		}
		title, _ := el.Content["title"].(string)
		names = append(names, fmt.Sprintf("TEMPLATE %s %q", id, title))
	}
	if len(names) == 0 {
		return ""
	}
	sortStrings(names)
	return "\nTEMPLATES — stencils, not work. READ them to learn this workspace's " +
		"conventions; you may not change anything in one, or put anything in one: " +
		strings.Join(names, ", ") + "\n"
}

// archivedBlock names the finished work, for the same reason templateBlock
// names the stencils: a boundary the model discovers only by being refused
// costs a turn every time it meets it.
//
// The read half is stated first and deliberately. A wrapped production is the
// best record the workspace has of how the last one was run, and "you may look
// at this to see how they did it before" is a more useful sentence than "this
// is off limits" — it is also what stops the model concluding the board is
// missing and building a worse copy of it.
func (s *BoardScope) archivedBlock() string {
	if len(s.Archived) == 0 {
		return ""
	}
	var names []string
	for id := range s.Archived {
		el := s.Elements[id]
		if el == nil && s.Board != nil && s.Board.ID == id {
			el = s.Board
		}
		// Only the archived boards themselves; their contents are implied.
		if el == nil || !isArchivedElement(el) {
			continue
		}
		title, _ := el.Content["title"].(string)
		names = append(names, fmt.Sprintf("ARCHIVED %s %q", id, title))
	}
	if len(names) == 0 {
		return ""
	}
	sortStrings(names)
	return "\nARCHIVED — finished productions. READ them for how past work was " +
		"organised; you may not change anything in one, or put anything in one. " +
		"If the person wants one worked on again, say it needs unarchiving first: " +
		strings.Join(names, ", ") + "\n"
}

// archivedRefusal is what a write into finished work is told.
//
// It names the EXIT as well as the reason. "You may not" with no next step is
// what makes a model try the same write through a different verb; "this needs
// unarchiving first, and that is the person's call" is a sentence it can put in
// its summary and the person can act on.
func archivedRefusal(kind ActionKind, id string) string {
	return fmt.Sprintf(
		"%s would write into %s, which is ARCHIVED — finished work the person "+
			"deliberately put to bed. Changing it rewrites the record of how a "+
			"completed production was actually run. Read it if you need its "+
			"conventions, then do the work somewhere else and say in your summary "+
			"that this board would have to be unarchived first.", verbFor(kind), id)
}

// templateTargetOf names the stencil an action would write into, or "".
//
// Both ends are checked: the PARENT (putting something into a template) and the
// TARGET (changing something that is already in one). A create's own id cannot
// be in a template because it does not exist yet — its parent decides.
func templateTargetOf(a Action, scope *BoardScope) string {
	if scope == nil {
		return ""
	}
	if scope.IsTemplate(a.ParentID) {
		return a.ParentID
	}
	if !a.Kind.Creates() && scope.IsTemplate(a.ElementID) {
		return a.ElementID
	}
	return ""
}

// archivedTargetOf names the finished board an action would write into, or "".
//
// A separate function from templateTargetOf rather than a merged "protected
// target", because the precondition that reads it reports a NAMED criterion and
// the person reading a failed verdict needs to know which rule stopped the run.
func archivedTargetOf(a Action, scope *BoardScope) string {
	if scope == nil {
		return ""
	}
	if scope.IsArchived(a.ParentID) {
		return a.ParentID
	}
	if !a.Kind.Creates() && scope.IsArchived(a.ElementID) {
		return a.ElementID
	}
	return ""
}

// templateRefusal is what a write into a stencil is told.
//
// It names WHY rather than reporting a rule, because the model's next move on a
// bare refusal is to try the same write through a different verb.
func templateRefusal(kind ActionKind, id string) string {
	return fmt.Sprintf(
		"%s would write into %s, which is a TEMPLATE — a stencil the person "+
			"instantiates, not work in progress. Changing it silently changes every "+
			"copy they make from it later. Read it if you need its conventions, then "+
			"do the work somewhere else and say in your summary that you left the "+
			"template alone.", verbFor(kind), id)
}
