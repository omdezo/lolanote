package agent

import (
	"fmt"

	"qomranote/backend/internal/domain"
)

// What may hold what.
//
// "Is it a container?" was the only question asked, and three types answer yes
// — board, column, to-do list — so a column was a legal home for another
// column. Asked to improve a film workflow, the agent put "Screenplay" and
// "Pre-Production" INSIDE the existing "Development" column, did the same in two
// more, and moved six cards into them. Structurally the plan was coherent; on
// the board it was nonsense, because a column is a list of CARDS and a column
// inside one is not a thing this product draws.
//
// The rule is about the product's shape, not about safety, so it lives here
// rather than in domain: a board holds structure, a column holds content, a
// to-do list holds tasks. Being a container says what you can put things in;
// it never said what things.
var canHold = map[domain.ElementType]map[domain.ElementType]bool{
	// A board is the canvas. Everything is at home on it.
	domain.TypeBoard: nil, // nil means "anything"

	// A column is a vertical list of cards. Not a place for more structure:
	// nesting columns is how you get an arrangement the canvas cannot draw.
	domain.TypeColumn: {
		domain.TypeCard: true, domain.TypeLink: true, domain.TypeImage: true,
		domain.TypeFile: true, domain.TypeTaskList: true, domain.TypeTable: true,
		domain.TypeCommentThread: true, domain.TypeDocument: true,
		domain.TypeSketch: true, domain.TypeColorSwatch: true, domain.TypeClone: true,
	},

	// A to-do list holds its tasks and nothing else.
	domain.TypeTaskList: {domain.TypeTask: true},
}

// CanHold reports whether a parent of one type may contain a child of another.
func CanHold(parent, child domain.ElementType) bool {
	// A TASK is a ROW of a checklist, not an element that lives anywhere else.
	// Stated before the table because a board holds "anything", and admitting
	// tasks to the scope so the agent can finally READ a checklist would
	// otherwise have made "move this task onto the canvas" a legal plan — a task
	// on a board canvas is drawn by nothing.
	if child == domain.TypeTask {
		return parent == domain.TypeTaskList
	}
	allowed, known := canHold[parent]
	if !known {
		return false // not a container at all
	}
	if allowed == nil {
		return true // a board takes anything
	}
	return allowed[child]
}

// containmentError explains a refusal in terms the model can act on. A bare
// "not allowed" gets retried; naming the right home moves the run forward.
func containmentError(parentType, childType domain.ElementType, parentID string) error {
	switch {
	case parentType == domain.TypeColumn && childType == domain.TypeColumn:
		return fmt.Errorf("a column cannot hold another column — columns are lists of cards, "+
			"and they live side by side on a board. Create it on the board instead of inside %s, "+
			"or if you want sub-stages, make a nested board", parentID)
	case parentType == domain.TypeColumn && childType == domain.TypeBoard:
		return fmt.Errorf("a column cannot hold a board — put the board on the canvas, "+
			"not inside %s", parentID)
	case parentType == domain.TypeTaskList:
		return fmt.Errorf("%s is a to-do list; it holds tasks, not %s", parentID, childType)
	default:
		return fmt.Errorf("a %s cannot hold a %s", parentType, childType)
	}
}

// childTypeOf is the element type an action puts somewhere, or TypeUnknown when
// the action does not place anything (a rename, a colour change).
func childTypeOf(a Action, scope *BoardScope) domain.ElementType {
	// A duplicate's type is whatever it copies, so it cannot come from the
	// kind. Without this a copied column reads as TypeUnknown and the
	// containment check waves it through into another column.
	if a.Kind == ActDuplicate {
		if el, ok := scope.Elements[a.ElementID]; ok {
			return el.Type
		}
		return domain.TypeUnknown
	}
	if a.Kind.Creates() {
		// Action.Type, not Kind.ElementType: place_file decides between IMAGE
		// and FILE from the attachment's mime type, and containment must judge
		// the element that will actually exist.
		return a.Type()
	}
	if a.Kind == ActMove {
		if el, ok := scope.Elements[a.ElementID]; ok {
			return el.Type
		}
	}
	return domain.TypeUnknown
}

// parentTypeOf is the type of the container an action files into — resolved
// against the plan's own creates first, since the parent may not exist yet.
func parentTypeOf(a Action, created map[string]ActionKind, scope *BoardScope) domain.ElementType {
	if a.ParentID == "" || (scope.Board != nil && a.ParentID == scope.Board.ID) {
		return domain.TypeBoard
	}
	if kind, staged := created[a.ParentID]; staged {
		return kind.ElementType()
	}
	if el, ok := scope.Elements[a.ParentID]; ok {
		return el.Type
	}
	return domain.TypeBoard // unresolvable parents are caught by parents.resolve
}

// rejectID records an id the model named that it was never shown, and reports
// whether the id was LIFTED FROM THE BOARD or merely invented.
//
// Only the first is evidence of an injection: content on the board offering the
// agent a target it should not have. The second is the model mis-remembering an
// id — worth reporting to the person, never worth accusing their board over.
func (s *staging) rejectID(id string) {
	s.quotas.outOfScope++
	if id != "" && s.scope != nil && s.scope.mentionsInContent(id) {
		s.quotas.lifted++
	}
}
