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
	// Relationships and grids. A connection expresses something no arrangement
	// of columns can — "this blocks that" — and a table is the honest answer
	// whenever items share repeating attributes.
	ActConnect     ActionKind = "connect"
	ActCreateTable ActionKind = "create_table"
	// A clone shows one card in two places and stays in sync. Only reachable
	// now that the clone read path authorizes its sources (GAPS_AUDIT §0).
	ActCloneHere ActionKind = "clone_here"
	// A note to the reader, left where the work is. Everything the agent knows
	// otherwise dies with the run panel: a month later the board cannot say why
	// it is shaped the way it is.
	ActComment ActionKind = "comment"
	// ActPlace moves an existing element to a computed position on the canvas.
	// NOT a create, so it never goes through the create-time layout pass; its
	// Position comes from ComputeArrangement instead.
	ActPlace ActionKind = "place"
	// Attribute edits that were unreachable: who owns a task, when it is due,
	// and how much space something takes. All content fields, so all of them
	// ride the generic update op.
	ActSetAssignee ActionKind = "set_assignee"
	ActSetReminder ActionKind = "set_reminder"
	ActResize      ActionKind = "resize"
	// ActAddTasks appends items to a to-do list that already exists.
	//
	// The agent could CREATE a checklist and never add to one. Asked to put
	// three more items on an existing list its only route was a second list
	// beside the first, which is not what anybody means.
	ActAddTasks ActionKind = "add_tasks"
	// ActPlaceFile puts a file the person ATTACHED TO THE REQUEST onto the
	// board — as an image card if it is one, a file card otherwise.
	//
	// The agent could look at an attachment and had no way to place it. Asked
	// "put this image in the first scene" it answered "I am unable to directly
	// add an image without its content or a URL", which was true and reads as
	// broken: the file was right there, uploaded, and it had already read it.
	ActPlaceFile ActionKind = "place_file"
	// A heading is a CARD with a variant, not a new type — it is a landmark on
	// the canvas that names a region without boxing it.
	ActCreateHeading ActionKind = "create_heading"

	// The rest of this block closes the gap between what a person can do on a
	// board and what the agent can do. Three of the app's own toolbar buttons
	// had no counterpart in the plan vocabulary, and the whole editing half of
	// the surface was missing: the agent could CREATE a table, a link, an image
	// card, and then never change one.
	//
	// Asked to add a line to a budget table it had exactly one route — build a
	// second table beside the first. That is the same failure as the to-do list
	// before ActAddTasks, repeated across every type.

	// ActWriteDocument makes a DOCUMENT: long-form prose that belongs in a page,
	// not a sticky note. A treatment, a brief, an outline. The agent used to
	// answer these with a card, and a card is a two-line object.
	ActWriteDocument ActionKind = "write_document"
	// ActAddColor makes a COLOR_SWATCH. On a workspace built for film and
	// design work, a palette is a first-class artefact, and every mood board
	// the agent built came out with the colours described in words.
	ActAddColor ActionKind = "add_color"
	// ActLinkBoard makes an ALIAS: a shortcut to a board that lives elsewhere.
	// This is how a hub board points at the real work without moving it.
	ActLinkBoard ActionKind = "link_board"

	// ActEditTable rewrites the cells of a table that already exists —
	// replacing the grid, or appending rows to it.
	ActEditTable ActionKind = "edit_table"
	// ActSetURL repoints an existing link. A dead URL was permanent.
	ActSetURL ActionKind = "set_url"
	// ActSetCaption labels an image or file card. An uncaptioned picture is one
	// nothing else on the board can refer to.
	ActSetCaption ActionKind = "set_caption"
	// ActCollapse folds a column shut, or opens one. This is how a board that
	// has grown past a screen gets readable again.
	ActCollapse ActionKind = "collapse"

	// ActDuplicate copies an element and everything inside it, INDEPENDENTLY —
	// unlike ActCloneHere, which makes an instance that stays in sync. Episode
	// two starts as a copy of episode one and then diverges; a synced clone is
	// the wrong tool for that and there was no right one.
	ActDuplicate ActionKind = "duplicate"
	// ActStyleBoard sets a nested board's tile colour and icon.
	//
	// A board tile takes a GRADIENT and the agent's only colour vocabulary was
	// eight note-paper pastels, so serving it "the way cardSwatches already is"
	// would have produced a tile that looks broken beside its siblings. Two
	// separately named vocabularies, and a digest that reads what the siblings
	// already carry — without which "make these distinct" is a coin flip against
	// a hash-picked default.
	ActStyleBoard ActionKind = "style_board"
	// ActRestore brings something back from the trash, with everything the same
	// delete removed.
	//
	// The op, the capability mapping and the batch semantics all shipped; only
	// the verb was missing, so "put back the casting column" was a thing the
	// product could do, the person could do by hand, and the agent could not
	// name. The batch grouping is what makes it read as magic and what makes a
	// naive per-element restore wrong: restoring a column brings back its cards
	// as one unit, because that is what the delete took.
	ActRestore ActionKind = "restore"
	// ActSetDirection pins a card's text direction — auto, rtl or ltr.
	//
	// A first-class product control on thirteen element types that the agent
	// could neither set nor SEE. On a bilingual product it is not cosmetic:
	// content.textDirection also governs Arabic-Indic numeral substitution, so it
	// decides whether the numbers in an agent-authored Arabic card render as ٥ or
	// as 5. And because the agent could not read a pin, a rewrite through
	// set_note_text silently inherited whatever auto-detection made of the new
	// first word — quietly undoing a decision a person made on purpose.
	ActSetDirection ActionKind = "set_direction"
	// ActResolveThread closes out a discussion, or reopens one.
	//
	// The compiler wrote content.resolved = false on every thread it created and
	// nothing anywhere read it back or ever set it true. `resolved` is the
	// difference between a live objection and a settled one — exactly what a
	// reporting run should weigh when asked what is still contested — and it was
	// a field the agent could only ever initialise.
	ActResolveThread ActionKind = "resolve_thread"
	// ActConvert changes what an element IS while keeping what it says: a note
	// that outgrew itself becomes a document, a note listing steps becomes a
	// checklist. Delete-and-recreate loses the id, and with it every connector,
	// comment and clone pointing at it.
	ActConvert ActionKind = "convert"
)

// Creates reports whether the action brings a new element into being.
// Answered by the registry, so a kind cannot claim one thing here and another
// where it is compiled.
func (k ActionKind) Creates() bool {
	s, ok := SpecFor(k)
	return ok && s.Creates
}

// LinkPreview is what a page said about itself, carried from the fetch that
// read it to the compiler that writes the card.
//
// Every one of these strings is ⟨web⟩ — somebody else's prose, arriving through
// a URL the model chose. They are sanitized on the way in and land in a LINK
// element, which the digest already labels ⟨web⟩, so the provenance survives
// into the next run's reading of the board.
type LinkPreview struct {
	Description  string `bson:"description,omitempty"  json:"description,omitempty"`
	ThumbnailURL string `bson:"thumbnailUrl,omitempty" json:"thumbnailUrl,omitempty"`
	SiteName     string `bson:"siteName,omitempty"     json:"siteName,omitempty"`
	EmbedType    string `bson:"embedType,omitempty"    json:"embedType,omitempty"`
}

// Type is what this action actually produces — the per-action override when it
// carries one, the kind's answer otherwise. Every consumer that asks "what will
// exist" must go through here rather than through Kind.ElementType(), or the
// override is written by the compiler and disbelieved by the verifier.
func (a Action) Type() domain.ElementType {
	if a.ElementType != "" {
		return a.ElementType
	}
	return a.Kind.ElementType()
}

// ElementType maps a create action to the element it produces. It is the
// DEFAULT: see Action.Type for the one kind that overrides it.
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
	case ActConnect:
		return domain.TypeLine
	case ActCreateTable:
		return domain.TypeTable
	case ActCloneHere:
		return domain.TypeClone
	case ActComment:
		return domain.TypeCommentThread
	case ActCreateHeading:
		return domain.TypeCard
	case ActPlaceFile:
		return domain.TypeImage
	case ActWriteDocument:
		return domain.TypeDocument
	case ActAddColor:
		return domain.TypeColorSwatch
	case ActLinkBoard:
		return domain.TypeAlias
	}
	return domain.TypeUnknown
}

// CreatedIDs is what an action actually brings into being.
//
// For nearly every kind that is ElementID, and the two were used
// interchangeably. A DUPLICATE breaks that: its ElementID names the SOURCE it
// copies, and the ids it writes live in Copies. Every check that reached for
// ElementID on a create was therefore wrong about it in a way that only shows
// up on a board with a duplicate in the plan:
//
//   - dropping the duplicate from the review list registered the ORIGINAL as a
//     dead parent, cascading the drop to unrelated work on the original;
//   - "what can I parent to?" answered with the source id, so a later action
//     aimed at the copy landed in the thing being copied;
//   - the id list offered to the model after a mistake named the source as
//     something the run had created.
//
// Ask this instead of ElementID wherever the question is "what does this plan
// create".
func (a Action) CreatedIDs() []string {
	if a.Kind == ActDuplicate {
		out := make([]string, 0, len(a.Copies))
		for _, c := range a.Copies {
			out = append(out, c.NewID)
		}
		return out
	}
	if a.Kind.Creates() && a.ElementID != "" {
		return []string{a.ElementID}
	}
	return nil
}

// Container reports whether the created element can hold children — which is
// what makes it a legal parent for a later action in the same plan.
func (k ActionKind) Container() bool {
	s, ok := SpecFor(k)
	return ok && s.Container
}

// Destructive reports whether the action loses information. Destructive plans
// cannot run unattended: they force a human decision (see Autonomy).
func (k ActionKind) Destructive() bool {
	s, ok := SpecFor(k)
	return ok && s.Destructive
}

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
	// FromID / ToID carry a connection's endpoints; Rows carries a table.
	FromID string `bson:"fromId,omitempty" json:"fromId,omitempty"`
	ToID   string `bson:"toId,omitempty"   json:"toId,omitempty"`
	// Relation is what the connector MEANS. The server turns it into colour,
	// weight, curve and arrowheads, so a diagram can be read at a glance
	// instead of label by label.
	Relation Relation   `bson:"relation,omitempty" json:"relation,omitempty"`
	Rows     [][]string `bson:"rows,omitempty"   json:"rows,omitempty"`
	// Collapsed folds a column shut. A pointer so "leave it alone" and "open
	// it" are different states — a plain bool would make every action that
	// never mentions collapse look like a request to expand.
	Collapsed *bool `bson:"collapsed,omitempty" json:"collapsed,omitempty"`
	// Becomes is the type ActConvert turns an element into.
	Becomes string `bson:"becomes,omitempty" json:"becomes,omitempty"`
	// ElementType overrides what this create produces, for the one kind whose
	// output is not decided by the kind alone.
	//
	// place_file's own description promises "an image card if it is one, a file
	// card otherwise", and the type was derived from the ActionKind — a pure
	// function that cannot see a mime type — so an attached PDF was compiled as
	// an IMAGE and rendered as <img src={pdf}>: a broken picture with no
	// filename and no way to download it. Empty means "ask the kind", which is
	// still the answer for every other create.
	ElementType domain.ElementType `bson:"elementType,omitempty" json:"elementType,omitempty"`
	// MimeType / Size describe a placed FILE. Named fields rather than a
	// generic map for the same reason as the attribute edits above: the review
	// list and the ghost layer read them without inspecting untyped content.
	MimeType string `bson:"mimeType,omitempty" json:"mimeType,omitempty"`
	Size     int64  `bson:"size,omitempty"     json:"size,omitempty"`
	// Preview is what a fetched page said about itself. Nil when the fetch was
	// skipped or failed, which compiles to the plain link the board used to get.
	Preview *LinkPreview `bson:"preview,omitempty" json:"preview,omitempty"`
	// Copies holds the ids ActDuplicate will write, in the order the subtree
	// was walked: Copies[0] is the copy of the element itself. Assigned at
	// staging time from the LIVE subtree, so the preview shows exactly the
	// elements the commit creates.
	Copies []CopySpec `bson:"copies,omitempty" json:"copies,omitempty"`
	// AssigneeID / RemindAt carry the people and time edits.
	AssigneeID string `bson:"assigneeId,omitempty" json:"assigneeId,omitempty"`
	RemindAt   string `bson:"remindAt,omitempty"   json:"remindAt,omitempty"`
	Section    string `bson:"section,omitempty" json:"section,omitempty"`
	// Index is where this sits INSIDE its container, assigned by OrderPlan
	// relative to what is already there. It used to be the action's position in
	// the whole plan, which put new cards above existing ones and let one
	// column's contents decide another's ordering.
	Index float64 `bson:"index,omitempty" json:"index,omitempty"`
	// Position is assigned by the server's layout pass for elements that land
	// directly on a canvas, so preview and commit cannot disagree.
	Position *ColumnBox `bson:"position,omitempty" json:"position,omitempty"`
	// Destination is where this action puts the element, by name — "Pricing",
	// "Unsorted". Empty when it lands on the board itself, where the ghost
	// already shows exactly where.
	//
	// Computed by LabelDestinations, not by whoever staged the action: only a
	// pass over the finished plan can name a container the same plan created.
	Destination string `bson:"destination,omitempty" json:"destination,omitempty"`
	// Risk is how much of a reviewer's attention this row deserves:
	// "touches-existing", "structural" or "additive".
	//
	// Computed here rather than in the browser because only the server holds the
	// whole subtree. The client's derivation is correct as far as it can see and
	// fails SAFE past that — an element on a nested board it has not loaded is
	// banded as existing material, never as an addition — so a run that filed
	// into four sub-boards showed every row at full weight and the collapse that
	// makes a 41-row list reviewable did nothing.
	//
	// Never taken from the model. A band a model can argue for is one more thing
	// to argue with, and this one decides what a person is allowed not to read.
	Risk string `bson:"risk,omitempty" json:"risk,omitempty"`
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
	// RunID is the run that authored this plan, carried so the compiler can
	// stamp provenance onto every element it creates.
	//
	// Without it the agent cannot tell its own prior output from the person's.
	// The ORGANISING register's whole premise — the material is already here and
	// it is theirs, so restraint IS the job — was being applied to cards a run
	// wrote ninety seconds earlier, which makes "clean up what you did last
	// time" structurally indistinguishable from "clean up my board".
	RunID string `bson:"runId,omitempty" json:"-"`
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
	// Destinations are boards outside this run's root that the plan files into.
	// Recorded here so APPLY can validate each against the human's own edit
	// rights before widening the grant — the plan states an intention, the
	// service decides whether it is permitted.
	Destinations []string `bson:"destinations,omitempty" json:"destinations,omitempty"`
	// NewComments are the bodies for any comment threads this plan creates.
	// Like labels, they are written at APPLY time only, so a discarded preview
	// leaves no orphan threads behind.
	NewComments []*domain.Comment `bson:"newComments,omitempty" json:"newComments,omitempty"`
	// Quarantined marks a plan produced by a run that board content tried to
	// steer. It can still be applied — by a person, deliberately — but never
	// unattended, whatever the autonomy setting says.
	Quarantined bool `bson:"quarantined,omitempty" json:"quarantined,omitempty"`
	// Question is set when the run stopped to ask instead of guessing.
	Question *Question `bson:"question,omitempty" json:"question,omitempty"`
	// Shape is how the elements this plan CREATES should be laid out on the
	// canvas: "" packs them in rows, "tree" draws a hierarchy, "flow" draws a
	// process left to right.
	//
	// Declared by the plan rather than inferred, because only the run knows
	// whether five connected cards are a process it just designed or five cards
	// that happen to have lines between them.
	Shape Layout `bson:"shape,omitempty" json:"shape,omitempty"`
	// ProposedRule is one sentence the agent suggests adding to this board's
	// standing rules, offered only when the person had to correct it.
	//
	// Refinements used to die with the run. Say "never split the Research
	// column" across five runs and the sixth still splits it — the only durable
	// channel was the rules field, which the person had to think to write by
	// hand and which the agent never mentioned.
	//
	// Proposed, never written: a rule the agent set for itself would be
	// invisible, would compound silently, and could not be argued with.
	ProposedRule string `bson:"proposedRule,omitempty" json:"proposedRule,omitempty"`
	// AppliedMemoryIDs are the standing rules the run says it followed,
	// resolved against the ids the digest actually rendered.
	//
	// A rules list is the one part of the system that only ever grows, and
	// nothing recorded which rule had ever fired — so a rule written for a board
	// that has since changed shape is indistinguishable from one that fires every
	// run, the list becomes noise, the model starts skimming it, and the rules
	// that matter stop being obeyed. Self-reported and cheap: it does not need to
	// be accurate to be useful, because the question is "is this rule ever
	// relevant", not "was it obeyed".
	AppliedMemoryIDs []string `bson:"appliedMemoryIds,omitempty" json:"appliedMemoryIds,omitempty"`
	// Variants are the alternative SHAPES this run built, when the structural
	// decision had more than one defensible answer. Variants[0] is Actions.
	//
	// The hardest structural decisions were committed to blind: the run picked
	// one shape before it had seen either, and the person reviewed a finished
	// forty-action structure whose only alternative was "discard and retype". A
	// whiteboard is the one medium where two alternatives can be judged in two
	// seconds, and nothing offered it.
	//
	// Nil on nearly every run: gated to structural plans and capped, so the
	// common case carries no extra bytes and no extra decision.
	Variants []Variant `bson:"variants,omitempty" json:"variants,omitempty"`
	// ChosenVariant is the index of the shape the person actually applied.
	//
	// Zero on every run that offered one shape, which is the same thing it means
	// when K were offered and the first was taken — the two are worth
	// distinguishing only alongside Variants, which is where anything reading
	// this already is. Its value is the correction record: the shapes NOT chosen
	// are the clearest preference signal the product ever collects.
	ChosenVariant int `bson:"chosenVariant,omitempty" json:"chosenVariant,omitempty"`
	// Confidence is how sure the run is that this is what was meant: "sure",
	// "reading" or "guess". Empty on plans written before the field existed.
	//
	// The person's scarce resource in reviewing forty actions is attention, and
	// nothing directed it. Worse, the terse-intent path is a guess-management
	// mechanism whose output was free prose — so the one place the system KNOWS
	// it interpreted rather than understood reported in the same register as
	// everything else. Applying a plan built on a wrong reading costs a review
	// and an undo, and there was no signal to look harder.
	Confidence string `bson:"confidence,omitempty" json:"confidence,omitempty"`
	// Reading is the interpretation the run acted on, quoted. Required whenever
	// Confidence is not "sure" — the field exists to make the interpretation
	// QUOTABLE, and an unquoted one is worth nothing. That is the same lesson as
	// the refusals that quote the person's own words back: the model argues with
	// an assertion and complies with a quote.
	Reading string `bson:"reading,omitempty" json:"reading,omitempty"`
	// Unmet is the model's own account of parts of the request this plan does
	// NOT carry out, each with its reason.
	//
	// Notes above is the harness speaking — budgets, dropped references, step
	// limits. This is the agent speaking, and it is the more important of the
	// two: a plan that silently does four of the five things asked reads as a
	// bad agent, when the honest failure is that nobody was told about the
	// fifth. Separate fields because the provenance differs and a person should
	// be able to tell "the system stopped me" from "I decided not to".
	Unmet []Unmet `bson:"unmet,omitempty" json:"unmet,omitempty"`
}

// Unmet is one part of the request the plan does not deliver.
type Unmet struct {
	Request string `bson:"request" json:"request"`
	Why     string `bson:"why,omitempty" json:"why,omitempty"`
}

// EnsureShape guarantees a plan serializes into the shape clients expect.
//
// Go marshals a nil slice as `null`, not `[]`. A plan with no actions is a
// legitimate outcome — an answer, or a question — and every client that
// iterates plan.actions crashes on null. The empty case is the one worth being
// careful about precisely because it is the rare one: it ships untested and
// takes the whole surface down when it finally happens.
func (p *Plan) EnsureShape() *Plan {
	if p == nil {
		return nil
	}
	if p.Actions == nil {
		p.Actions = []Action{}
	}
	return p
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
	collect := func(actions []Action) {
		for _, a := range actions {
			if a.Kind.Creates() || a.ElementID == "" || seen[a.ElementID] {
				continue
			}
			seen[a.ElementID] = true
			ids = append(ids, a.ElementID)
		}
	}
	collect(p.Actions)
	// The UNION across alternative shapes, because the person may apply any of
	// them. A fingerprint taken over the shape that happened to be first would
	// let the second one commit against an element that moved under it — the
	// staleness check silently narrower than the thing it guards.
	for _, v := range p.Variants {
		collect(v.Actions)
	}
	sortStrings(ids)
	return ids
}

// DestinationParentIDs names every container whose CONTENTS this plan changes,
// which is the grain the membership fingerprint is taken at.
//
// Only creates, moves and deletes qualify. Membership exists to catch "a card
// appeared in the container I was building and now sits outside the grouping",
// and a rename or a recolour cannot cause that — the per-element timestamp
// already covers those, so pinning their container as well would re-import the
// whole-workspace over-invalidation one action kind at a time.
//
// boardID stands in for an empty ParentID: an action that lands directly on the
// canvas is still an insertion into a container, and dropping those would leave
// the commonest destination of all unpinned.
func (p *Plan) DestinationParentIDs(boardID string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(parent string) {
		if parent == "" || seen[parent] {
			return
		}
		seen[parent] = true
		out = append(out, parent)
	}
	// Same union as TargetIDs, for the same reason: whichever shape the person
	// picks, its destinations are the ones membership must have been pinned over.
	// Copied rather than appended in place — p.Actions may have spare capacity,
	// and growing it here would write another shape's actions into the plan.
	actions := append([]Action(nil), p.Actions...)
	for _, v := range p.Variants {
		actions = append(actions, v.Actions...)
	}
	for _, a := range actions {
		switch {
		case a.Kind.Creates(), a.Kind == ActMove:
			parent := a.ParentID
			if parent == "" {
				parent = boardID
			}
			add(parent)
		case a.Kind == ActDelete:
			// The container at risk is the one being REMOVED, not the one it
			// sits in: a card dropped into a column between the review and the
			// apply is swept away by the cascade, and nothing else would notice.
			add(a.ElementID)
		}
	}
	sortStrings(out)
	return out
}

// ActionID derives an element id deterministically from the run and the
// action's position, so a retried apply collides on the existing id instead of
// creating a duplicate.
func ActionID(runID string, seq int) string {
	sum := sha256.Sum256([]byte(runID + ":action:" + fmt.Sprint(seq)))
	return hex.EncodeToString(sum[:12]) // 24 hex chars — the element id shape
}

// childID derives a stable id for a generated child (a to-do's tasks).
// CopySpec is one element a duplicate will create: a new id, the source it is
// copied from, and the new id of its parent.
//
// Resolved when the action is staged rather than when it is compiled, because
// the whole subtree has to be readable to answer "how many elements is this"
// — which is what the review list and the cost estimate show the person BEFORE
// they approve it.
type CopySpec struct {
	NewID    string  `bson:"newId"    json:"newId"`
	SourceID string  `bson:"sourceId" json:"sourceId"`
	ParentID string  `bson:"parentId" json:"parentId"`
	Index    float64 `bson:"index,omitempty" json:"index,omitempty"`
}

// targetFor resolves an action's target for compilation: an element already on
// the board, or one THIS SAME PLAN creates.
//
// Every revise branch below used to read scope.Elements alone and hard-error on
// a miss, which made the staged plan write-once — the compiler half of the same
// wall resolveExisting put in front of the tools. A run could create a card and
// never colour it, label it, or fix a cell in a table it had just built.
//
// A staged target has NO PRIOR VALUE, and the synthesised element is
// deliberately empty rather than a guess at one. That is what makes the inverse
// honest: reverting the CREATE deletes the element outright, so an inverse that
// claimed to restore some earlier colour would be describing a state that never
// existed. Empty in, empty out.
func targetFor(p *Plan, scope *BoardScope, id string) (*domain.Element, bool) {
	if el, ok := scope.Elements[id]; ok {
		return el, false
	}
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.ElementID == id && a.Kind.Creates() {
			return &domain.Element{ID: id, Type: a.Kind.ElementType(), Content: domain.Content{}}, true
		}
	}
	return nil, false
}

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
		// Where each action's ops begin, so the kind can be stamped onto all of
		// them below without every branch remembering to. A to-do compiles to a
		// list plus one op per task and a duplicate to one per copied element,
		// so this is a range rather than a single index.
		from := len(ops)
		switch a.Kind {
		case ActAddTasks:
			// Items land after whatever the list already holds. Their ids are
			// derived from the list and the index, so a retried apply produces
			// the same ops rather than duplicates.
			for i, text := range a.Tasks {
				ops = append(ops, domain.Op{
					ElementID: childID(a.ElementID, int(a.Index)+i),
					Action:    domain.ActionCreate,
					Changes: domain.Content{
						"type": string(domain.TypeTask),
						"location": map[string]any{
							"parentId": a.ElementID,
							"section":  string(domain.SectionCanvas),
							"index":    a.Index + float64(i),
						},
						"content": map[string]any{"text": text, "done": false},
					},
				})
			}
		case ActCreateBoard, ActCreateColumn, ActCreateNote, ActCreateTodo, ActCreateLink,
			ActCreateTable, ActConnect, ActCloneHere, ActComment, ActCreateHeading,
			ActPlaceFile, ActWriteDocument, ActAddColor, ActLinkBoard:
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
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
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
				changes["index"] = a.Index
			}
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionMove,
				Changes:     domain.Content{"location": changes},
				UndoChanges: domain.Content{"location": locationOf(el)},
			})

		case ActApplyLabel:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
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

		case ActEditTable:
			el, ok := scope.Elements[a.ElementID]
			if !ok {
				return nil, fmt.Errorf("agent: plan edits table %s, which is outside the compiled scope", a.ElementID)
			}
			// cells is replaced wholesale by MergePatch, so the inverse carries
			// the WHOLE prior grid. Restoring a delta would leave the table in
			// a state it was never in.
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"cells": a.Rows}},
				UndoChanges: domain.Content{"content": map[string]any{"cells": el.Content["cells"]}},
			})

		case ActSetURL:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan repoints %s, which is outside the compiled scope", a.ElementID)
			}
			prev, _ := el.Content["url"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"url": a.URL}},
				UndoChanges: domain.Content{"content": map[string]any{"url": prev}},
			})

		case ActSetCaption:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan captions %s, which is outside the compiled scope", a.ElementID)
			}
			prev, _ := el.Content["caption"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"caption": a.Title}},
				UndoChanges: domain.Content{"content": map[string]any{"caption": prev}},
			})

		case ActCollapse:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan collapses %s, which is outside the compiled scope", a.ElementID)
			}
			if a.Collapsed == nil {
				return nil, fmt.Errorf("agent: collapse action %d says neither open nor shut", a.Seq)
			}
			prev, _ := el.Content["collapsed"].(bool)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"collapsed": *a.Collapsed}},
				UndoChanges: domain.Content{"content": map[string]any{"collapsed": prev}},
			})

		case ActConvert:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan converts %s, which is outside the compiled scope", a.ElementID)
			}
			changes, undo, err := convertOps(el, a)
			if err != nil {
				return nil, err
			}
			ops = append(ops, domain.Op{
				ElementID: a.ElementID, Action: domain.ActionUpdate,
				Changes: changes, UndoChanges: undo,
			})

		case ActDuplicate:
			// One create per element in the subtree, parents before children —
			// which is the order Copies was built in, and the order the write
			// path's scope check needs when a child parents to something
			// created in the same transaction.
			for _, c := range a.Copies {
				src, ok := scope.Elements[c.SourceID]
				if !ok {
					return nil, fmt.Errorf("agent: duplicate reads %s, which is outside the compiled scope", c.SourceID)
				}
				ops = append(ops, copyOp(src, c, a))
			}

		case ActSetAssignee:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan assigns %s, which is outside the compiled scope", a.ElementID)
			}
			prev, _ := el.Content["assigneeId"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"assigneeId": a.AssigneeID}},
				UndoChanges: domain.Content{"content": map[string]any{"assigneeId": prev}},
			})

		case ActSetReminder:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan schedules %s, which is outside the compiled scope", a.ElementID)
			}
			prev, _ := el.Content["reminderAt"].(string)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"reminderAt": a.RemindAt}},
				UndoChanges: domain.Content{"content": map[string]any{"reminderAt": prev}},
			})

		case ActResize:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan resizes %s, which is outside the compiled scope", a.ElementID)
			}
			if a.Position == nil {
				return nil, fmt.Errorf("agent: resize action %d has no size", a.Seq)
			}
			// A move op carrying only width: size lives on location, and the
			// inverse restores the whole prior location so nothing else drifts.
			ops = append(ops, domain.Op{
				ElementID: a.ElementID,
				Action:    domain.ActionMove,
				Changes: domain.Content{"location": map[string]any{
					"parentId": el.Location.ParentID,
					"section":  string(el.Location.Section),
					"width":    a.Position.Width,
				}},
				UndoChanges: domain.Content{"location": locationOf(el)},
			})

		case ActSetColor:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan colours %s, which is outside the compiled scope", a.ElementID)
			}
			// backgroundColor, not color. NoteCard reads
			// `element.content?.backgroundColor` and the human colour picker
			// writes the same key; this compiler wrote "color", which no
			// component reads. Nine cards were coloured, the review row said
			// "→ amber", the transaction committed, and the board looked
			// identical — and because colorOf read the same wrong key back, the
			// next run saw its own invisible taxonomy and believed it existed.
			// The prior value is read through the same fallback colorOf uses, so
			// undo restores a card that already carries the old junk key rather
			// than blanking it.
			prevColor := swatchHexOf(el)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"backgroundColor": a.Color}},
				UndoChanges: domain.Content{"content": map[string]any{"backgroundColor": prevColor}},
			})

		case ActSetDirection:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan pins the direction of %s, which is outside the compiled scope", a.ElementID)
			}
			// nil for auto, exactly as the card's own control writes it: the
			// renderer reads content.textDirection and treats anything that is not
			// "rtl"/"ltr" as auto, so storing the literal string "auto" would work
			// by accident and diverge the moment somebody tightened the read.
			var next any
			if a.Text != "auto" {
				next = a.Text
			}
			prev := el.Content["textDirection"]
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"textDirection": next}},
				UndoChanges: domain.Content{"content": map[string]any{"textDirection": prev}},
			})

		case ActResolveThread:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan resolves %s, which is outside the compiled scope", a.ElementID)
			}
			prevResolved, _ := el.Content["resolved"].(bool)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"resolved": a.Done}},
				UndoChanges: domain.Content{"content": map[string]any{"resolved": prevResolved}},
			})

		case ActSetTask:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan ticks %s, which is outside the compiled scope", a.ElementID)
			}
			prevDone, _ := el.Content["done"].(bool)
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": map[string]any{"done": a.Done}},
				UndoChanges: domain.Content{"content": map[string]any{"done": prevDone}},
			})

		case ActPlace:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan places %s, which is outside the compiled scope", a.ElementID)
			}
			if a.Position == nil {
				return nil, fmt.Errorf("agent: place action %d has no position", a.Seq)
			}
			// A move op, not an update: the element keeps its parent and only
			// its coordinate changes, and locationOf captures the whole prior
			// location so the inverse restores it exactly.
			ops = append(ops, domain.Op{
				ElementID: a.ElementID,
				Action:    domain.ActionMove,
				Changes: domain.Content{"location": map[string]any{
					"parentId": el.Location.ParentID,
					"section":  string(domain.SectionCanvas),
					"position": map[string]any{"x": a.Position.X, "y": a.Position.Y},
				}},
				UndoChanges: domain.Content{"location": locationOf(el)},
			})

		case ActRename:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
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
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan edits %s, which is outside the compiled scope", a.ElementID)
			}
			prevText, _ := el.Content["textPreview"].(string)
			prevDoc := el.Content["doc"]
			ops = append(ops, domain.Op{
				ElementID: a.ElementID,
				Action:    domain.ActionUpdate,
				Changes: domain.Content{"content": map[string]any{
					"textPreview": domain.TextPreview(a.Text), "doc": tiptapDoc(a.Text),
					"searchText": a.Text,
				}},
				UndoChanges: domain.Content{"content": map[string]any{
					"textPreview": prevText, "doc": prevDoc,
				}},
			})

		case ActStyleBoard:
			el, _ := targetFor(p, scope, a.ElementID)
			if el == nil {
				return nil, fmt.Errorf("agent: plan styles %s, which is outside the compiled scope", a.ElementID)
			}
			// Exactly what the popover writes, including nulling iconUrl: the three
			// icon forms are mutually exclusive, and leaving an uploaded image in
			// place beside a glyph name means the tile keeps showing the image and
			// the change is invisible.
			changes := map[string]any{}
			undo := map[string]any{}
			if a.Color != "" {
				changes["color"], undo["color"] = boardColors[a.Color], el.Content["color"]
			}
			if a.Text != "" {
				changes["icon"], undo["icon"] = a.Text, el.Content["icon"]
				changes["iconUrl"], undo["iconUrl"] = nil, el.Content["iconUrl"]
			}
			ops = append(ops, domain.Op{
				ElementID:   a.ElementID,
				Action:      domain.ActionUpdate,
				Changes:     domain.Content{"content": changes},
				UndoChanges: domain.Content{"content": undo},
			})

		case ActRestore:
			// The write path restores the WHOLE trashBatchId, which is the semantic
			// that makes the feature read as magic: bringing back a column brings
			// back the cards that delete took with it. The inverse is a delete of
			// the same element, which cascades the same way.
			ops = append(ops, domain.Op{ElementID: a.ElementID, Action: domain.ActionRestore})

		case ActDelete:
			if _, ok := scope.Elements[a.ElementID]; !ok {
				return nil, fmt.Errorf("agent: plan deletes %s, which is outside the compiled scope", a.ElementID)
			}
			ops = append(ops, domain.Op{ElementID: a.ElementID, Action: domain.ActionDelete})

		default:
			return nil, fmt.Errorf("agent: unknown action %q", a.Kind)
		}
		// Stamped once, at the seam every branch passes through. The write path
		// reads it to decide which content keys this op is allowed to carry, so
		// a branch that forgot to set it would be refused rather than trusted —
		// but "a branch forgot" is the failure that produced the denylist in the
		// first place, and one assignment outside the switch cannot forget.
		for i := from; i < len(ops); i++ {
			ops[i].Kind = string(a.Kind)
		}
	}
	stampAuthorship(ops, p.RunID)
	return ops, nil
}

// ContentKeyAllowance is the whole compiler's content contract, flattened into
// the map a delegation grant carries: ActionKind → the keys ops of that kind may
// write.
//
// Built from ContentKeysFor, which derives its answer by RUNNING the compiler
// over a probe action, so the allowance cannot drift from what is emitted. That
// is the property the denylist never had: it was a hand-kept parallel list, and
// it was already missing three keys by the time anybody checked.
func ContentKeyAllowance() map[string][]string {
	out := make(map[string][]string, len(actionSpecs)+1)
	for kind := range actionSpecs {
		keys, declared := ContentKeysFor(kind)
		if !declared {
			continue
		}
		// authoredBy is already in the declared set for creates: ContentKeysFor
		// adds it, because stampAuthorship writes it on a separate pass that no
		// compiler branch mentions.
		out[string(kind)] = keys
	}
	// The one kind whose key set is genuinely unknowable: a copy carries its
	// source's content verbatim, deliberately, because a copy that quietly
	// dropped fields would be a worse lie than refusing. Named explicitly so it
	// is a decision rather than a hole in the allowlist — the write path still
	// runs its own privileged-key check over a verbatim copy.
	out[string(ActDuplicate)] = []string{domain.ContentKeysVerbatim}
	return out
}

// authoredByKey marks an element as this run's own work, on the element itself.
//
// Origin and AgentRunID live on the TRANSACTION, and CreatedBy is the human's
// sub even for an agent write — so nothing on the element said who made it, and
// the digest's trustAgent label was declared and assigned nowhere. Inert on the
// client, and it survives undo, revert and the clone fan-out for free because it
// rides inside content like any other field.
const authoredByKey = "authoredBy"

// stampAuthorship labels every element this plan brings into being.
//
// Done as one pass over the compiled ops rather than inside each create branch,
// because there are five places that emit a create and a sixth will be added:
// the invariant is "everything the agent makes is labelled", and an invariant
// enforced in five branches is one a future branch will miss.
func stampAuthorship(ops []domain.Op, runID string) {
	author := "agent"
	if runID != "" {
		author += ":" + runID
	}
	for i := range ops {
		if ops[i].Action != domain.ActionCreate {
			continue
		}
		content, ok := ops[i].Changes["content"].(map[string]any)
		if !ok {
			content = map[string]any{}
			if ops[i].Changes == nil {
				ops[i].Changes = domain.Content{}
			}
			ops[i].Changes["content"] = content
		}
		content[authoredByKey] = author
	}
}

// AuthoredByAgent reports whether an element was written by an agent run rather
// than by a person.
func AuthoredByAgent(el *domain.Element) bool {
	if el == nil {
		return false
	}
	by, _ := el.Content[authoredByKey].(string)
	return by == "agent" || strings.HasPrefix(by, "agent:")
}

// ContentKeysFor is the complete set of content keys one action kind may write.
//
// The delegation's content guard is a two-item DENYLIST over a schemaless map —
// `isHome` and `isTemplate`, plus `acl` — and a denylist maintained by everyone
// who adds a feature will be wrong. It already is: `content.locked` is not on
// it, `content.cloneSourceId` is not on it, and `content.agentInstructions` is
// not on it, so the guard against a run rewriting the standing rules that
// CONSTRAIN it is that no tool happens to emit that key today.
//
// The invariant is the other way round: an agent op may write only the keys its
// own action kind declares. Privileged keys then need no enumeration anywhere —
// they are excluded by not being produced. Derived from the compiler's own
// switch by running it, rather than restated beside it, because a contract kept
// in parallel with the code it describes is a contract that drifts within one
// wave.
// Returns declared=false for a kind that has no expressible key set — today only
// ActDuplicate, whose copy carries its SOURCE's content verbatim and so cannot
// be described by its kind. A guard must refuse rather than wave through
// anything it gets false for: "we could not work out what this may write" is not
// the same answer as "it may write nothing", and conflating the two is how a
// denylist gets rebuilt one special case at a time.
func ContentKeysFor(kind ActionKind) (keys []string, declared bool) {
	spec, ok := SpecFor(kind)
	if kind == ActDuplicate {
		// A duplicate does not go through createOp: copyOp carries the SOURCE's
		// content verbatim, deliberately, because a copy that quietly dropped
		// fields would be a worse lie than refusing. Its key set is the source's,
		// which the kind cannot know.
		return nil, false
	}
	if !ok || !spec.Creates {
		// Only creates go through createOp. The revise branches write a known
		// key each and are enumerated by reviseContentKeys.
		if set, known := reviseContentKeys[kind]; known && set != nil {
			return append([]string(nil), set...), true
		}
		return nil, false
	}
	// A probe action carrying every payload field, so the switch takes whichever
	// branch it takes and every key that branch can write is observed. The point
	// is the SHAPE of what the compiler emits, not the values.
	probe := Action{
		Kind: kind, ElementID: "probe", ParentID: "parent",
		Title: "t", Text: "x", URL: "https://example.invalid", Color: "#000",
		FromID: "f", ToID: "to", Rows: [][]string{{"a"}}, AssigneeID: "att",
		MimeType: "application/pdf", Size: 1, Tasks: []string{"a task"},
		Preview: &LinkPreview{Description: "d", ThumbnailURL: "u", SiteName: "s", EmbedType: "e"},
	}
	seen := map[string]bool{
		// Every create is stamped with its author on a separate pass, so it is a
		// key the kind produces even though the switch never mentions it.
		authoredByKey: true,
	}
	// EVERY op the branch emits, not just the first. A to-do compiles to the
	// list plus one TASK per item, and those children write {text, done} — keys
	// no createOp probe ever sees. Reading ops[0] alone declared a contract the
	// compiler broke on the second op, and the guard refused the agent's own
	// ordinary output.
	for _, op := range compileProbe(probe) {
		content, _ := op.Changes["content"].(map[string]any)
		for k := range content {
			seen[k] = true
		}
	}
	keys = make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys, true
}

// compileProbe runs one probe action through the real compiler and hands back
// every op it produced, falling back to the create branch alone for the kinds
// whose compile needs live board state.
//
// The fallback is why the paired test in contentkeys_test.go matters: a kind
// that silently lands here declares only its first op's keys, and the test is
// what proves no shipped kind does.
func compileProbe(a Action) []domain.Op {
	scope := &BoardScope{
		Board:    &domain.Element{ID: "parent", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
	}
	if ops, err := CompileOps(&Plan{Actions: []Action{a}}, scope); err == nil && len(ops) > 0 {
		return ops
	}
	return []domain.Op{createOp(a)}
}

// reviseContentKeys is the declared key set for the kinds that EDIT rather than
// create. These branches are hand-written in CompileOps, one key each, and a new
// one that forgets to appear here is refused rather than silently trusted.
var reviseContentKeys = map[ActionKind][]string{
	ActRename:        {"title"},
	ActSetText:       {"doc", "searchText", "textPreview"},
	ActSetColor:      {"backgroundColor"},
	ActSetTask:       {"done"},
	ActEditTable:     {"cells"},
	ActSetURL:        {"url"},
	ActSetCaption:    {"caption"},
	ActCollapse:      {"collapsed"},
	ActSetAssignee:   {"assigneeId"},
	ActSetReminder:   {"reminderAt"},
	ActSetDirection:  {"textDirection"},
	ActResolveThread: {"resolved"},
	// Conversion rewrites what an element IS, so it touches the three keys that
	// carry a body between shapes — and nothing else.
	ActConvert: {"doc", "textPreview", "title"},
	// Structural kinds write no content at all. Declared as an EMPTY set rather
	// than left out: "this writes nothing" and "nobody has said" are different
	// answers, and only the first is safe to let through a guard.
	ActMove: {}, ActPlace: {}, ActResize: {}, ActDelete: {}, ActApplyLabel: {},
	ActRestore:    {},
	ActStyleBoard: {"color", "icon", "iconUrl"},
	// add_tasks creates TASK children rather than patching its target.
	ActAddTasks: {"done", "text"},
	// A duplicate copies its source verbatim: its key set is the source's, which
	// is not knowable from the kind. Declaring nothing here means the guard has
	// to special-case it rather than silently allow everything.
	ActDuplicate: nil,
}

func createOp(a Action) domain.Op {
	loc := map[string]any{
		"parentId": a.ParentID,
		"section":  a.Section,
		"index":    a.Index,
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
		content["textPreview"] = domain.TextPreview(a.Text)
		content["doc"] = tiptapDoc(a.Text)
		content["searchText"] = a.Text
	case ActCreateTodo:
		content["title"] = a.Title
	case ActCreateLink:
		// The compiler used to write showPreview:false and showDescription:false
		// unconditionally — not an omission but a decision, and it made an
		// agent-made link WORSE than a default one: a grey card that had opted
		// out of being rich, on a product whose own drop handler resolves the
		// page. When the fetch succeeded, the card is what a person would have
		// got; when it did not, this is exactly the old bare link.
		content["url"] = a.URL
		content["title"] = a.Title
		if a.Preview != nil {
			content["description"] = a.Preview.Description
			content["thumbnailUrl"] = a.Preview.ThumbnailURL
			content["siteName"] = a.Preview.SiteName
			content["embedType"] = a.Preview.EmbedType
			content["showPreview"] = true
			content["showDescription"] = a.Preview.Description != ""
		} else {
			content["showPreview"] = false
			content["showDescription"] = false
		}
	case ActConnect:
		// Matches what the app's own line tool writes, so an agent-drawn
		// connector is indistinguishable from a hand-drawn one. The STYLE comes
		// from what the connector means: every arrow used to be the same grey
		// arrow, which made a diagram something you had to read label by label.
		style := styleFor(a.Relation)
		content["fromId"] = a.FromID
		content["toId"] = a.ToID
		content["label"] = a.Title
		content["color"] = style.Color
		content["weight"] = style.Weight
		content["curve"] = style.Curve
		content["endArrow"] = style.EndArrow
		content["startArrow"] = style.StartArrow
	case ActPlaceFile:
		// Exactly what the app's own drag-and-drop writes (BoardCanvas.tsx
		// onDrop), so an agent-placed file is indistinguishable from one
		// somebody dropped on the board — including the branch. A PDF written
		// into the IMAGE shape rendered as a broken picture with no filename
		// and no download, which is what the two shapes exist to prevent.
		content["url"] = a.URL
		content["attachmentId"] = a.AssigneeID // carries the attachment id
		if a.Type() == domain.TypeImage {
			content["caption"] = a.Title
		} else {
			content["filename"] = a.Title
			content["mimeType"] = a.MimeType
			content["size"] = a.Size
		}
	case ActCreateTable:
		content["title"] = a.Title
		// The renderer reads content.cells (TableCard.tsx). Writing anything
		// else produces a table that exists and shows nothing.
		content["cells"] = a.Rows
	case ActCloneHere:
		content["cloneSourceId"] = a.FromID
	case ActComment:
		// The thread element carries no body; the comment itself lives in the
		// comment collection, keyed by this element's id.
		content["resolved"] = false
	case ActCreateHeading:
		content["textPreview"] = domain.TextPreview(a.Text)
		content["doc"] = tiptapDoc(a.Text)
		content["searchText"] = a.Text
		content["variant"] = headingVariant
	case ActWriteDocument:
		// Exactly what the app's own Document button writes: a title, the
		// rich-text body, and the plain-text preview.
		//
		// textPreview is CAPPED at the same 500 the editor caps it at. Writing
		// 20,000 characters into a field the human path truncates to 500 meant
		// the first keystroke somebody typed into the agent's document silently
		// destroyed 97% of what search, export and the next run could see.
		// searchText carries the whole body for the consumers that need it.
		content["title"] = a.Title
		content["textPreview"] = domain.TextPreview(a.Text)
		content["doc"] = tiptapDoc(a.Text)
		content["searchText"] = a.Text
	case ActAddColor:
		// displayFormat is what the card cycles through when tapped; starting
		// on HEX matches the toolbar. The name rides along so a palette entry
		// says what it is FOR, not just what it is.
		content["hex"] = a.Color
		content["displayFormat"] = "HEX"
		if a.Title != "" {
			content["title"] = a.Title
		}
	case ActLinkBoard:
		content["targetBoardId"] = a.FromID
		content["title"] = a.Title
	}

	return domain.Op{
		ElementID: a.ElementID,
		Action:    domain.ActionCreate,
		Changes: domain.Content{
			"type": string(a.Type()), "location": loc, "content": content,
		},
		// A create inverts to a delete; the client derives that from the
		// action, so no explicit inverse payload is needed.
		UndoChanges: domain.Content{},
	}
}

// tiptapDoc turns an authored body into the rich-text document shape notes store
// alongside their plain-text preview.
//
// It used to split on "\n" and emit paragraphs, full stop — so the agent could
// only ever write a wall, on a product whose editor ships headings, both kinds
// of list, blockquote, code and links, and whose format bar puts all of them one
// click away. MarkdownToTiptap is a strict superset: a body with no markdown in
// it produces exactly the paragraphs this always produced.
func tiptapDoc(text string) map[string]any { return MarkdownToTiptap(text) }

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

// convertOps changes what an element IS while keeping what it says.
//
// An update, not a delete-and-recreate: the id survives, and with it every
// connector drawn to it, every comment on it, and every clone of it. Recreating
// would silently sever all three.
func convertOps(el *domain.Element, a Action) (domain.Content, domain.Content, error) {
	target := domain.ElementType(a.Becomes)
	if el.Type == target {
		return nil, nil, fmt.Errorf("agent: convert action %d turns a %s into a %s", a.Seq, el.Type, target)
	}
	text := strings.TrimSpace(a.Text)
	if text == "" {
		text, _ = el.Content["textPreview"].(string)
	}
	// The inverse restores the type AND the content keys the conversion
	// overwrites. Type alone would leave a CARD holding a document's title.
	undo := domain.Content{"type": string(el.Type), "content": map[string]any{
		"textPreview": el.Content["textPreview"],
		"doc":         el.Content["doc"],
		"title":       el.Content["title"],
	}}

	switch target {
	case domain.TypeDocument:
		// A note that outgrew a sticky. The first line becomes the page title,
		// the rest is the body — which is how somebody would do it by hand.
		title, body := splitFirstLine(text)
		return domain.Content{"type": string(target), "content": map[string]any{
			"title": title, "textPreview": body, "doc": tiptapDoc(body),
		}}, undo, nil

	case domain.TypeCard:
		title, _ := el.Content["title"].(string)
		body := joinTitleAndBody(title, text)
		return domain.Content{"type": string(target), "content": map[string]any{
			"title": "", "textPreview": body, "doc": tiptapDoc(body),
		}}, undo, nil

	case domain.TypeTaskList:
		// The list's ITEMS are separate elements, which this op cannot create —
		// so the handler stages an add_tasks alongside the conversion rather
		// than producing an empty checklist. See runConvert.
		title, _ := el.Content["title"].(string)
		if title == "" {
			title, _ = splitFirstLine(text)
		}
		return domain.Content{"type": string(target), "content": map[string]any{
			"title": title, "textPreview": "", "doc": nil,
		}}, undo, nil
	}
	return nil, nil, fmt.Errorf("agent: convert action %d asks for a %s, which is not a conversion target", a.Seq, target)
}

// splitFirstLine separates a heading line from the body beneath it.
func splitFirstLine(text string) (string, string) {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+1:])
	}
	return text, ""
}

// copyOp writes one element of a duplicated subtree.
//
// The copy carries the source's content verbatim — a duplicate that quietly
// dropped fields would be a worse lie than refusing. Only the identity keys
// change: a fresh id, the new parent, and a position the layout pass fills in
// for whatever lands on a canvas.
func copyOp(src *domain.Element, c CopySpec, a Action) domain.Op {
	content := map[string]any{}
	for k, v := range src.Content {
		content[k] = v
	}
	// A CLONE copied as a CLONE would still point at the original source, which
	// is right: duplicating a synced instance gives another synced instance.
	// But the root copy takes the new title when one was asked for, so
	// "duplicate this for episode 2" produces something distinguishable.
	if c.SourceID == a.ElementID && a.Title != "" {
		if _, hasTitle := content["title"]; hasTitle || src.Type == domain.TypeColumn ||
			src.Type == domain.TypeBoard || src.Type == domain.TypeTaskList {
			content["title"] = a.Title
		}
	}
	loc := map[string]any{
		"parentId": c.ParentID,
		"section":  string(src.Location.Section),
		"index":    c.Index,
	}
	if src.Location.Width > 0 {
		loc["width"] = src.Location.Width
	}
	if c.SourceID == a.ElementID && a.Position != nil {
		loc["position"] = map[string]any{"x": a.Position.X, "y": a.Position.Y}
		loc["index"] = a.Index
	}
	return domain.Op{
		ElementID:   c.NewID,
		Action:      domain.ActionCreate,
		Changes:     domain.Content{"type": string(src.Type), "location": loc, "content": content},
		UndoChanges: domain.Content{},
	}
}
