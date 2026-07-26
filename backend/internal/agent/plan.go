package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"qomranote/backend/internal/domain"
)

// A Plan is what a run proposes: an ordered list of typed actions over the
// element graph. It replaced a clustering-shaped proposal because the agent
// needs to express anything a person can do on a board — make a board, put
// columns in it, fill those with notes and to-dos, connect two cards — not just
// group what already exists.
//
// Two properties make it safe to hand a model this much reach:
//
//   - Actions are STAGED, never executed as they are proposed. The whole plan
//     compiles to one domain.Transaction, so it lands atomically, broadcasts
//     once, undoes with one Ctrl+Z, and reverts as a unit.
//   - Created elements get their real ids at STAGING time, derived from the run
//     and the action's position. That is what lets action 4 put a column inside
//     the board that action 1 created, and it makes a retried apply produce
//     byte-identical ops instead of duplicates.

// ActionKind is the closed set of things a plan may contain.
type ActionKind string

const (
	ActCreateBoard  ActionKind = "create_board"
	ActCreateColumn ActionKind = "create_column"
	ActCreateNote   ActionKind = "create_note"
	ActCreateTodo   ActionKind = "create_todo"
	ActCreateLink   ActionKind = "create_link"
	ActMove         ActionKind = "move_element"
	ActRename       ActionKind = "rename"
	ActSetText      ActionKind = "set_note_text"
	ActDelete       ActionKind = "delete_element"
	// Attribute edits. These change how an element is FILED without moving it,
	// which is often the honest answer to "organise this": a card can be both
	// urgent and about pricing, and no arrangement of columns expresses that.
	ActApplyLabel ActionKind = "apply_label"
	ActSetColor   ActionKind = "set_color"
	ActSetTask    ActionKind = "set_task_done"
)

// Creates reports whether the action brings a new element into being.
func (k ActionKind) Creates() bool {
	switch k {
	case ActCreateBoard, ActCreateColumn, ActCreateNote, ActCreateTodo, ActCreateLink:
		return true
	}
	return false
}

// ElementType maps a create action to the element it produces.
func (k ActionKind) ElementType() domain.ElementType {
	switch k {
	case ActCreateBoard:
		return domain.TypeBoard
	case ActCreateColumn:
		return domain.TypeColumn
	case ActCreateNote:
		return domain.TypeCard
	case ActCreateTodo:
		return domain.TypeTaskList
	case ActCreateLink:
		return domain.TypeLink
	}
	return domain.TypeUnknown
}

// Container reports whether the created element can hold children — which is
// what makes it a legal parent for a later action in the same plan.
func (k ActionKind) Container() bool {
	return k == ActCreateBoard || k == ActCreateColumn || k == ActCreateTodo
}

// Destructive reports whether the action loses information. Destructive plans
// cannot run unattended: they force a human decision (see Autonomy).
func (k ActionKind) Destructive() bool { return k == ActDelete }

// Action is one staged step.
type Action struct {
	Seq  int        `bson:"seq"  json:"seq"`
	Kind ActionKind `bson:"kind" json:"kind"`
	// ElementID is the element created or edited. For creates it is assigned
	// at staging time and is final.
	ElementID string `bson:"elementId" json:"elementId"`
	ParentID  string `bson:"parentId,omitempty" json:"parentId,omitempty"`
	// Title / Text / URL carry the payload for the kinds that use them.
	Title string   `bson:"title,omitempty"   json:"title,omitempty"`
	Text  string   `bson:"text,omitempty"    json:"text,omitempty"`
	URL   string   `bson:"url,omitempty"     json:"url,omitempty"`
	Tasks []string `bson:"tasks,omitempty"   json:"tasks,omitempty"`
	// LabelID / Color / Done carry the attribute edits. They are separate
	// fields rather than a generic map so the review list, the ghost layer and
	// the inverse can all be built without inspecting untyped content.
	LabelID string `bson:"labelId,omitempty" json:"labelId,omitempty"`
	Color   string `bson:"color,omitempty"   json:"color,omitempty"`
	Done    bool   `bson:"done,omitempty"    json:"done,omitempty"`
	Section string `bson:"section,omitempty" json:"section,omitempty"`
	// Position is assigned by the server's layout pass for elements that land
	// directly on a canvas, so preview and commit cannot disagree.
	Position *ColumnBox `bson:"position,omitempty" json:"position,omitempty"`
	// Summary is the one-line human sentence shown in the review list.
	Summary string `bson:"summary" json:"summary"`
	// Because is the agent's one-clause reason, when it offered one. Reviewing
	// forty actions by reconstructing the logic yourself is slower than doing
	// the work; a reason per grouping makes a plan scannable.
	Because string `bson:"because,omitempty" json:"because,omitempty"`
}

// ColumnBox is a rectangle on the canvas.
type ColumnBox struct {
	X     float64 `bson:"x"     json:"x"`
	Y     float64 `bson:"y"     json:"y"`
	Width float64 `bson:"width" json:"width"`
}

// Plan is the full proposal awaiting a human decision.
type Plan struct {
	Actions []Action `bson:"actions" json:"actions"`
	// Summary is the agent's own account of what it is proposing.
	Summary string `bson:"summary,omitempty" json:"summary,omitempty"`
	// Notes records what the agent deliberately did NOT do, and what the
	// harness dropped. Silence about a refusal reads as success.
	Notes []string `bson:"notes,omitempty" json:"notes,omitempty"`
	// Fingerprint binds the plan to the exact versions of the elements it
	// touches, so an apply can tell that the board moved underneath it.
	Fingerprint map[string]string `bson:"fingerprint,omitempty" json:"-"`
	// NewLabels are labels the run coined. They are inserted at APPLY time with
	// the ops, never while planning: a preview that is discarded must leave the
	// user's taxonomy exactly as it found it.
	NewLabels []*domain.Label `bson:"newLabels,omitempty" json:"newLabels,omitempty"`
	// Quarantined marks a plan produced by a run that board content tried to
	// steer. It can still be applied — by a person, deliberately — but never
	// unattended, whatever the autonomy setting says.
	Quarantined bool `bson:"quarantined,omitempty" json:"quarantined,omitempty"`
	// Question is set when the run stopped to ask instead of guessing.
	Question *Question `bson:"question,omitempty" json:"question,omitempty"`
}

// Destructive reports whether any action loses information.
func (p *Plan) Destructive() bool {
	for _, a := range p.Actions {
		if a.Kind.Destructive() {
			return true
		}
	}
	return false
}

// TargetIDs lists every EXISTING element the plan touches — the exact set the
// fingerprint covers. Created elements are excluded: they cannot go stale
// because they do not exist yet.
func (p *Plan) TargetIDs() []string {
	seen := map[string]bool{}
	var ids []string
	for _, a := range p.Actions {
		if a.Kind.Creates() || a.ElementID == "" || seen[a.ElementID] {
			continue
		}
		seen[a.ElementID] = true
		ids = append(ids, a.ElementID)
	}
	sortStrings(ids)
	return ids
}

// ActionID derives an element id deterministically from the run and the
// action's position, so a retried apply collides on the existing id instead of
// creating a duplicate.
func ActionID(runID string, seq int) string {
	sum := sha256.Sum256([]byte(runID + ":action:" + fmt.Sprint(seq)))
	return hex.EncodeToString(sum[:12]) // 24 hex chars — the element id shape
}

// childID derives a stable id for a generated child (a to-do's tasks).
func childID(parentID string, index int) string {
	sum := sha256.Sum256([]byte(parentID + ":child:" + fmt.Sprint(index)))
	return hex.EncodeToString(sum[:12])
}

// CompileOps turns a plan into the ops of ONE transaction.
//
// Order matters and is preserved: a create precedes any action that parents to
// it, which is what the write path's scope check relies on when it validates a
// child against a parent created in the same transaction.
func CompileOps(p *Plan, scope *BoardScope) ([]domain.Op, error) {
	if len(p.Actions) == 0 {
		return nil, ErrEmptyPlan
	}
	ops := make([]domain.Op, 0, len(p.Actions)*2)

	for _, a := range p.Actions {
		switch a.Kind {
		case ActCreateBoard, ActCreateColumn, ActCreateNote, ActCreateTodo, ActCreateLink:
			ops = append(ops, createOp(a))
			if a.Kind == ActCreateTodo {
				// A to-do list is a container; its items are TASK children,
				// each an element in its own right.
				for i, text := range a.Tasks {
					ops = append(ops, domain.Op{
						ElementID: childID(a.ElementID, i),
						Action:    domain.ActionCreate,
						Changes: domain.Content{
							"type": string(domain.TypeTask),
							"location": map[string]any{
								"parentId": a.ElementID,
								"section":  string(domain.SectionCanvas),
								"index":    float64(i + 1),
							},
							"content": map[string]any{"text": text, "done": false},
						},
						UndoChanges: domain.Content{},
					})
				}
			}

		case ActMove:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan moves %s, which is outside the compiled scope", a.ElementID)
			}
			section := a.Section
			if section == "" {
				section = string(domain.SectionCanvas)
			}
			changes := map[string]any{"parentId": a.ParentID, "section": section}
			if a.Position != nil {
				changes["position"] = map[string]any{"x": a.Position.X, "y": a.Position.Y}
			} else {
				changes["index"] = float64(a.Seq + 1)
			}
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionMove,
				Changes:     domain.Content{"location": changes},
				UndoChanges: domain.Content{"location": locationOf(el)},
			})

		case ActApplyLabel:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan labels %s, which is outside the compiled scope", a.ElementID)
			}
			// UndoChanges carries the WHOLE prior set, not the delta: labelIds
			// is replaced wholesale by MergePatch, so a revert has to restore
			// the list as it stood, including labels the agent never touched.
			prev := append([]string{}, el.LabelIDs...)
			next := prev
			if !containsStr(prev, a.LabelID) {
				next = append(append([]string{}, prev...), a.LabelID)
			}
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"labelIds": next},
				UndoChanges: domain.Content{"labelIds": prev},
			})

		case ActSetColor:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan colours %s, which is outside the compiled scope", a.ElementID)
			}
			prevColor, _ := el.Content["color"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"color": a.Color}},
				UndoChanges: domain.Content{"content": map[string]any{"color": prevColor}},
			})

		case ActSetTask:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan ticks %s, which is outside the compiled scope", a.ElementID)
			}
			prevDone, _ := el.Content["done"].(bool)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"done": a.Done}},
				UndoChanges: domain.Content{"content": map[string]any{"done": prevDone}},
			})

		case ActRename:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan renames %s, which is outside the compiled scope", a.ElementID)
			}
			prev, _ := el.Content["title"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"title": a.Title}},
				UndoChanges: domain.Content{"content": map[string]any{"title": prev}},
			})

		case ActSetText:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan edits %s, which is outside the compiled scope", a.ElementID)
			}
			prevText, _ := el.Content["textPreview"].(string)
			prevDoc := el.Content["doc"]
			ops = append(ops, domain.Op{
				ElementID: a.ElementID,
				Action:    domain.ActionUpdate,
				Changes: domain.Content{"content": map[string]any{
					"textPreview": a.Text, "doc": tiptapDoc(a.Text),
				}},
				UndoChanges: domain.Content{"content": map[string]any{
					"textPreview": prevText, "doc": prevDoc,
				}},
			})

		case ActDelete:
			if _, ok := scope.Elements[a.ElementID]; !ok {
				return nil, fmt.Errorf("agent: plan deletes %s, which is outside the compiled scope", a.ElementID)
			}
			ops = append(ops, domain.Op{ElementID: a.ElementID, Action: domain.ActionDelete})

		default:
			return nil, fmt.Errorf("agent: unknown action %q", a.Kind)
		}
	}
	return ops, nil
}

func createOp(a Action) domain.Op {
	loc := map[string]any{
		"parentId": a.ParentID,
		"section":  a.Section,
		"index":    float64(a.Seq + 1),
	}
	if a.Section == "" {
		loc["section"] = string(domain.SectionCanvas)
	}
	if a.Position != nil {
		loc["position"] = map[string]any{"x": a.Position.X, "y": a.Position.Y}
		loc["width"] = a.Position.Width
	}

	content := map[string]any{}
	switch a.Kind {
	case ActCreateBoard:
		content["title"] = a.Title
	case ActCreateColumn:
		content["title"] = a.Title
		content["collapsed"] = false
	case ActCreateNote:
		content["textPreview"] = a.Text
		content["doc"] = tiptapDoc(a.Text)
	case ActCreateTodo:
		content["title"] = a.Title
	case ActCreateLink:
		content["url"] = a.URL
		content["title"] = a.Title
		content["showPreview"] = false
		content["showDescription"] = false
	}

	return domain.Op{
		ElementID: a.ElementID,
		Action:    domain.ActionCreate,
		Changes: domain.Content{
			"type": string(a.Kind.ElementType()), "location": loc, "content": content,
		},
		// A create inverts to a delete; the client derives that from the
		// action, so no explicit inverse payload is needed.
		UndoChanges: domain.Content{},
	}
}

// tiptapDoc wraps plain text in the rich-text document shape notes store
// alongside their plain-text preview.
func tiptapDoc(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	paragraphs := strings.Split(text, "\n")
	content := make([]any, 0, len(paragraphs))
	for _, p := range paragraphs {
		if strings.TrimSpace(p) == "" {
			content = append(content, map[string]any{"type": "paragraph"})
			continue
		}
		content = append(content, map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": p}},
		})
	}
	return map[string]any{"type": "doc", "content": content}
}

func locationOf(el *domain.Element) map[string]any {
	return map[string]any{
		"parentId": el.Location.ParentID,
		"section":  string(el.Location.Section),
		"index":    el.Location.Index,
		"position": map[string]any{"x": el.Location.Position.X, "y": el.Location.Position.Y},
	}
}

// InvertOps builds the compensating ops for a committed transaction: creates
// become deletes, and patches swap with their precomputed inverses.
//
// Reverse order matters: children are removed before the containers that hold
// them, so a container delete never cascades over something the same revert was
// about to restore.
func InvertOps(ops []domain.Op) []domain.Op {
	out := make([]domain.Op, 0, len(ops))
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.Action {
		case domain.ActionCreate:
			out = append(out, domain.Op{ElementID: op.ElementID, Action: domain.ActionDelete})
		case domain.ActionDelete:
			out = append(out, domain.Op{ElementID: op.ElementID, Action: domain.ActionRestore})
		case domain.ActionRestore:
			out = append(out, domain.Op{ElementID: op.ElementID, Action: domain.ActionDelete})
		default:
			out = append(out, domain.Op{
				ElementID: op.ElementID, Action: op.Action,
				Changes: op.UndoChanges, UndoChanges: op.Changes,
			})
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Question is the one clarification a run may ask for, with concrete options so
// answering is a click rather than an essay.
type Question struct {
	Text    string   `bson:"text"              json:"text"`
	Options []string `bson:"options,omitempty" json:"options,omitempty"`
}
