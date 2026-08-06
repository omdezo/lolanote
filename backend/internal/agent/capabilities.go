package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// The handlers for the capability parity wave.
//
// Every one of these existed as a button in the app's own toolbar, or as an
// edit a person makes without thinking about it, and had no counterpart the
// agent could reach. The pattern was the same each time: it could CREATE a
// thing and never REVISE one, so the only way to change a table was to build a
// second table beside the first.
//
// Four things make a capability real, and missing any one of them produces a
// tool that passes every test and does nothing on the board:
//
//	1. a schema the model can call,
//	2. a handler that resolves ids and enforces containment,
//	3. a compiler branch writing the keys the RENDERER reads,
//	4. a digest that shows the current state, so the model can revise
//	   rather than guess.
//
// Point 4 is the one that keeps getting missed. An edit tool over a value the
// model cannot see is a tool it will only ever use to overwrite.

// runWriteDocument stages a DOCUMENT — prose that belongs on a page.
//
// The agent answered "write the treatment" with a note, because a note was all
// it had. A note is a sticky: three paragraphs on one is how a board becomes
// unreadable.
func (s *staging) runWriteDocument(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	parentID, section, err := s.resolveParentFor(in.ParentID, domain.TypeDocument)
	if err != nil {
		return r.fail("%v", err)
	}
	title := sanitizeName(in.Title)
	body := sanitizeBody(firstNonEmpty(in.Body, in.Text))
	if title == "" {
		return r.fail("a document needs a title")
	}
	if body == "" {
		return r.fail("a document needs a body — write the substance, an empty page helps nobody")
	}
	if _, err := s.add(Action{
		Kind: ActWriteDocument, ParentID: parentID, Section: section,
		Title: truncate(title, 120), Text: truncate(body, 20000),
		Summary: fmt.Sprintf("document %q", truncate(title, 40)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged the document %q.", truncate(title, 40)))
}

// runAddColor stages a COLOR_SWATCH.
func (s *staging) runAddColor(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	parentID, section, err := s.resolveParentFor(in.ParentID, domain.TypeColorSwatch)
	if err != nil {
		return r.fail("%v", err)
	}
	hexColor := normalizeHex(in.Color)
	if !isHexColor(hexColor) {
		// Naming the accepted shape turns a wrong value into a correction
		// rather than a retry of the same thing.
		return r.fail("%q is not a colour I can put on the board — use six hex digits, like #1b2a4a", in.Color)
	}
	title := sanitizeName(firstNonEmpty(in.Title, in.Name))
	if _, err := s.add(Action{
		Kind: ActAddColor, ParentID: parentID, Section: section,
		Color: hexColor, Title: truncate(title, 60),
		Summary: strings.TrimSpace(fmt.Sprintf("swatch %s %s", hexColor, truncate(title, 30))),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged the swatch %s.", hexColor))
}

// runLinkBoard stages an ALIAS pointing at a board that lives elsewhere.
func (s *staging) runLinkBoard(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	parentID, section, err := s.resolveParentFor(in.ParentID, domain.TypeAlias)
	if err != nil {
		return r.fail("%v", err)
	}
	target := firstNonEmpty(in.BoardID, in.SourceID, in.FromID)
	el, err := s.resolveExisting(target)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeBoard {
		return r.fail("%s is a %s — a shortcut can only point at a board", el.ID, el.Type)
	}
	title := sanitizeName(in.Title)
	if title == "" {
		title = titleOf(el)
	}
	if title == "" {
		title = "Board"
	}
	if _, err := s.add(Action{
		Kind: ActLinkBoard, ParentID: parentID, Section: section,
		FromID: el.ID, Title: truncate(title, 80),
		Summary: fmt.Sprintf("shortcut to %s", truncate(title, 40)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged a shortcut to %q.", truncate(title, 40)))
}

// runEditTable rewrites an existing table's grid.
//
// Whole-grid replacement rather than per-cell edits: cells is a nested array
// that MergePatch replaces wholesale, so a partial write would produce a table
// in a state it was never in. Asking for the finished grid also means the model
// has to have READ the table, which is the point — the digest renders its cells
// for exactly this reason.
func (s *staging) runEditTable(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeTable {
		return r.fail("%s is a %s, not a table", el.ID, el.Type)
	}
	rows, err := cleanGrid(in.Rows)
	if err != nil {
		return r.fail("%v", err)
	}
	before := len(toRows(el.Content["cells"]))
	if _, err := s.add(Action{
		Kind: ActEditTable, ElementID: el.ID, Rows: rows,
		Summary: fmt.Sprintf("table: %d rows (was %d)", len(rows), before),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged the table as %d rows by %d columns.", len(rows), len(rows[0])))
}

// runSetURL repoints an existing link.
func (s *staging) runSetURL(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeLink {
		return r.fail("%s is a %s, not a link", el.ID, el.Type)
	}
	url := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return r.fail("a link needs a full address starting http:// or https://")
	}
	if _, err := s.add(Action{
		Kind: ActSetURL, ElementID: el.ID, URL: truncate(url, 2000),
		Summary: fmt.Sprintf("link: %s", truncate(url, 48)),
	}); err != nil {
		return r.fail("%v", err)
	}
	// A new label is a separate action on the existing rename path, rather than
	// being smuggled into this one — so the review list shows both changes.
	if title := sanitizeName(in.Title); title != "" {
		if _, err := s.add(Action{
			Kind: ActRename, ElementID: el.ID, Title: truncate(title, 120),
			Summary: fmt.Sprintf("rename: %s", truncate(title, 40)),
		}); err != nil {
			return r.fail("%v", err)
		}
	}
	return r.out("Staged the new address.")
}

// runSetCaption labels a picture or file already on the board.
func (s *staging) runSetCaption(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeImage && el.Type != domain.TypeFile {
		return r.fail("%s is a %s — captions belong on pictures and files. "+
			"To retitle something else, use rename", el.ID, el.Type)
	}
	title := sanitizeName(in.Title)
	if title == "" {
		return r.fail("that needs a caption")
	}
	if _, err := s.add(Action{
		Kind: ActSetCaption, ElementID: el.ID, Title: truncate(title, 200),
		Summary: fmt.Sprintf("caption: %s", truncate(title, 40)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged the caption.")
}

// runStyleBoard gives a nested board's tile an identity.
func (s *staging) runStyleBoard(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeBoard {
		return r.fail("%s is a %s — only a board has a tile", el.ID, el.Type)
	}
	colour := strings.ToLower(strings.TrimSpace(in.Color))
	icon := strings.TrimSpace(firstNonEmpty(in.Name, in.Title))
	if colour == "" && icon == "" {
		return r.fail("say a colour, an icon, or both")
	}
	if colour != "" {
		if _, ok := boardColors[colour]; !ok {
			// Naming the vocabulary turns a wrong value into a correction rather
			// than a retry of the same thing — and the failure this guards is the
			// one that matters here: a card swatch on a board tile is a value the
			// popover would never write and the tile renders as a flat block
			// beside its gradient siblings.
			return r.fail("%q is not a board tile colour. A tile takes one of: %s",
				in.Color, strings.Join(boardColorNames(), ", "))
		}
	}
	if !boardIconAllowed(icon) {
		return r.fail("%q is not an icon this product has. Use one of: %s — or a single "+
			"capital letter or digit", icon, strings.Join(boardIcons, ", "))
	}
	what := colour
	if icon != "" {
		what = strings.TrimSpace(what + " " + icon)
	}
	if _, err := s.add(Action{
		Kind: ActStyleBoard, ElementID: el.ID, Color: colour, Text: icon,
		Summary: fmt.Sprintf("tile: %s", what),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged the tile style.")
}

// maxTrashShown bounds the trash listing. The panel a person uses is scrollable;
// a model context is not, and a hundred deleted cards would crowd out the board
// the run is actually about.
const maxTrashShown = 20

// runListTrash shows what this person deleted and could put back.
//
// Two rules make it safe, and both are about WHOSE clean-up it is.
//
// `Trashed` scopes to "deleted by you OR inside a board you own", which is right
// for the panel and wrong here: an agent listing across both buckets can propose
// resurrecting a colleague's deliberate tidy-up, and the person approving it has
// no way to tell that is what they are approving. So this filters to entries the
// delegating human deleted THEMSELVES.
//
// And every entry states its BATCH SIZE. A delete removed a container and
// everything in it as one unit, so "restore 1 thing" that returns thirteen is
// exactly the unreviewable surprise the clone fan-out is — the number is what
// makes the verb honest.
func (s *staging) runListTrash(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.elements == nil {
		return r.fail("the trash is not readable on this deployment")
	}
	// Preview-gated regardless of autonomy. An unattended run that quietly
	// un-deletes things is the one shape of this feature nobody asked for.
	if s.task.Autonomy != AutonomyPreview {
		return r.fail("restoring things needs somebody to look at it first, so it is not " +
			"available to a run that applies itself")
	}
	trashed, err := s.elements.Trashed(ctx, s.task.Owner)
	if err != nil {
		return r.fail("could not read the trash")
	}
	batch := map[string]int{}
	for _, el := range trashed {
		if el.TrashBatchID != "" {
			batch[el.TrashBatchID]++
		}
	}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	var lines []string
	for _, el := range trashed {
		if el.DeletedBy != s.task.Owner {
			// Somebody else's deliberate cleanup. Not listed at all, rather than
			// listed and refused: an id the model can see is an id it will try.
			continue
		}
		text := truncate(sanitizeText(textOf(el)), 80)
		if q != "" && !strings.Contains(strings.ToLower(text), q) {
			continue
		}
		line := fmt.Sprintf("%s · %s · %s", el.ID, el.Type, text)
		if n := batch[el.TrashBatchID]; n > 1 {
			line += fmt.Sprintf(" — restoring this brings back %d items", n)
		}
		if el.DeletedAt != nil {
			line += " — deleted " + humanAge(*el.DeletedAt)
		}
		lines = append(lines, line)
		if len(lines) == maxTrashShown {
			break
		}
	}
	if len(lines) == 0 {
		return r.out("Nothing in the trash that this person deleted themselves.")
	}
	return r.out("IN THE TRASH (things they deleted; restore(id) puts one back):\n" +
		strings.Join(lines, "\n"))
}

// runRestore stages a return from the trash.
func (s *staging) runRestore(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.elements == nil {
		return r.fail("the trash is not readable on this deployment")
	}
	if s.task.Autonomy != AutonomyPreview {
		return r.fail("restoring things needs somebody to look at it first, so it is not " +
			"available to a run that applies itself")
	}
	id := strings.TrimSpace(in.ElementID)
	if id == "" {
		return r.fail("restore needs an id from list_trash")
	}
	// Resolved against the TRASH rather than against the scope: a deleted element
	// is deliberately absent from Elements, so resolveExisting would reject every
	// legal call and count it as an out-of-scope reference.
	trashed, err := s.elements.Trashed(ctx, s.task.Owner)
	if err != nil {
		return r.fail("could not read the trash")
	}
	var target *domain.Element
	siblings := 0
	for _, el := range trashed {
		if el.ID == id {
			target = el
		}
	}
	if target == nil {
		return r.fail("%s is not in the trash — call list_trash and use an id from it", id)
	}
	if target.DeletedBy != s.task.Owner {
		return r.fail("%s was deleted by somebody else. Their clean-up is theirs to undo, "+
			"not yours to reverse on their behalf", id)
	}
	for _, el := range trashed {
		if el.TrashBatchID != "" && el.TrashBatchID == target.TrashBatchID {
			siblings++
		}
	}
	summary := fmt.Sprintf("restore %s", truncate(sanitizeText(textOf(target)), 40))
	if siblings > 1 {
		// The review row must state the count, because "restore 1 thing" that
		// returns thirteen is the same unreviewable surprise as an edit that
		// silently fans out to four boards.
		summary += fmt.Sprintf(" (and %d more from the same delete)", siblings-1)
	}
	if _, err := s.add(Action{Kind: ActRestore, ElementID: target.ID, Summary: summary}); err != nil {
		return r.fail("%v", err)
	}
	if siblings > 1 {
		return r.out(fmt.Sprintf("Staged. This brings back %d items — everything that delete "+
			"removed. Say so in your summary.", siblings))
	}
	return r.out("Staged.")
}

// directionTypes mirrors TEXT_TYPES in frontend/src/lib/direction.ts — the
// thirteen types whose context menu offers auto/rtl/ltr.
//
// Kept as a set here rather than as a "does it have text" guess, because the
// product's own control is what defines the answer: offering the agent a
// direction on a type the person cannot set one on would write a key nothing
// reads, which is precisely how set_color spent its whole shipped life.
var directionTypes = map[domain.ElementType]bool{
	domain.TypeCard: true, domain.TypeDocument: true, domain.TypeTaskList: true,
	domain.TypeColumn: true, domain.TypeCommentThread: true, domain.TypeTable: true,
	domain.TypeImage: true, domain.TypeLink: true, domain.TypeFile: true,
	domain.TypeBoard: true, domain.TypeAlias: true, domain.TypeClone: true,
}

// runSetDirection pins a card's text direction.
func (s *staging) runSetDirection(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if !directionTypes[el.Type] {
		return r.fail("%s is a %s and carries no text direction", el.ID, el.Type)
	}
	dir := strings.ToLower(strings.TrimSpace(in.Direction))
	switch dir {
	case "auto", "rtl", "ltr":
	case "":
		return r.fail("say which direction: auto, rtl or ltr")
	default:
		return r.fail("%q is not a direction — use auto, rtl or ltr", in.Direction)
	}
	// A CLONE shares its SOURCE's content, so a direction written onto the
	// instance would live on an element nothing renders from. The product's own
	// menu does exactly this redirect; doing anything else here would produce a
	// change that appears to succeed and never shows up.
	target := el
	if el.Type == domain.TypeClone {
		src, _ := el.Content["cloneSourceId"].(string)
		if src == "" {
			return r.fail("%s is a synced copy with no source recorded", el.ID)
		}
		origin, err := s.resolveExisting(src)
		if err != nil {
			return r.fail("%s is a synced copy of %s, which is not on this board — "+
				"pin the direction on the original instead", el.ID, src)
		}
		target = origin
	}
	if _, err := s.add(Action{
		Kind: ActSetDirection, ElementID: target.ID, Text: dir,
		Summary: fmt.Sprintf("direction: %s", dir),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged the direction.")
}

// runResolveThread settles a conversation, or reopens one.
func (s *staging) runResolveThread(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeCommentThread {
		return r.fail("%s is a %s — only a conversation can be resolved", el.ID, el.Type)
	}
	// The flag is REOPEN rather than DONE, so an omitted field means "resolve" —
	// the thing the tool exists for. A `done` boolean defaulting to Go's zero
	// would have made every unqualified call reopen a settled thread, which is
	// the opposite of what was asked and would look like the model arguing.
	done := !in.Reopen
	if was, _ := el.Content["resolved"].(bool); was == done {
		state := "open"
		if was {
			state = "already resolved"
		}
		return r.fail("%s is %s — nothing to change", el.ID, state)
	}
	verb := "resolve"
	if !done {
		verb = "reopen"
	}
	if _, err := s.add(Action{
		Kind: ActResolveThread, ElementID: el.ID, Done: done,
		Summary: verb + " the conversation",
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged.")
}

// runCollapse folds a column shut or opens one.
func (s *staging) runCollapse(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeColumn {
		return r.fail("%s is a %s — only columns fold", el.ID, el.Type)
	}
	if in.Collapsed == nil {
		return r.fail("say whether to fold it shut (true) or open it (false)")
	}
	want := *in.Collapsed
	if now, _ := el.Content["collapsed"].(bool); now == want {
		// Not an error worth retrying — the board is already how it wants it,
		// and a no-op in the review list is noise the person has to read past.
		state := "open"
		if want {
			state = "folded shut"
		}
		return r.out(fmt.Sprintf("That column is already %s; nothing staged.", state))
	}
	verb := "opened"
	if want {
		verb = "folded shut"
	}
	if _, err := s.add(Action{
		Kind: ActCollapse, ElementID: el.ID, Collapsed: &want,
		Summary: fmt.Sprintf("%s %s", verb, truncate(titleOf(el), 30)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged.")
}

// maxCopyElements bounds one duplicate. A copy is one op per element, and a
// person approving "duplicate this" is picturing a thing, not a hundred things.
const maxCopyElements = 60

// runDuplicate copies an element and its whole subtree as an INDEPENDENT copy.
//
// The subtree is walked HERE, at staging time, not at compile time: the review
// list and the cost estimate both have to say how many elements this actually
// creates before the person approves it. Resolving it later would mean showing
// "1 change" for something that writes forty.
func (s *staging) runDuplicate(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	src, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if src.Type == domain.TypeLine {
		return r.fail("a connector is defined by what it joins, so copying one on its own " +
			"would draw a second line between the same two things. Use connect instead")
	}
	parentID, section, err := s.resolveParentFor(firstNonEmpty(in.ParentID, src.Location.ParentID), src.Type)
	if err != nil {
		return r.fail("%v", err)
	}
	// A container cannot be copied into itself: the copy would be its own
	// descendant, and the subtree walk would never reach the bottom.
	if parentID == src.ID || s.descendsFrom(parentID, src.ID) {
		return r.fail("%s cannot be copied into itself", src.ID)
	}

	copies := s.planCopies(src, parentID)
	if len(copies) > maxCopyElements {
		return r.fail("%s holds %d items — more than one copy should carry. "+
			"Copy something smaller, or build what you need directly", src.ID, len(copies))
	}
	title := sanitizeName(in.Title)
	if _, err := s.add(Action{
		Kind: ActDuplicate, ElementID: src.ID, ParentID: parentID, Section: section,
		Title: truncate(title, 120), Copies: copies,
		Summary: fmt.Sprintf("copy of %s (%d items)", truncate(titleOf(src), 30), len(copies)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged a copy: %d element(s).", len(copies)))
}

// planCopies walks the live subtree breadth-first and assigns each element the
// id its copy will have. Parents come before children, which is the order the
// write path needs when a child parents to something created in the same
// transaction.
func (s *staging) planCopies(root *domain.Element, newParentID string) []CopySpec {
	seq := len(s.plan.Actions)
	specs := []CopySpec{{
		NewID: copyID(s.runID, seq, root.ID), SourceID: root.ID,
		ParentID: newParentID, Index: root.Location.Index,
	}}
	newIDOf := map[string]string{root.ID: specs[0].NewID}

	for i := 0; i < len(specs) && len(specs) <= maxCopyElements; i++ {
		for _, child := range s.childrenOf(specs[i].SourceID) {
			id := copyID(s.runID, seq, child.ID)
			newIDOf[child.ID] = id
			specs = append(specs, CopySpec{
				NewID: id, SourceID: child.ID,
				ParentID: newIDOf[specs[i].SourceID], Index: child.Location.Index,
			})
		}
	}
	return specs
}

// copyID derives a duplicate's id from the run, the action and the source.
// Deterministic, so a retried apply writes the same ops instead of a second
// copy of everything.
func copyID(runID string, seq int, sourceID string) string {
	sum := sha256.Sum256([]byte(runID + ":copy:" + fmt.Sprint(seq) + ":" + sourceID))
	return hex.EncodeToString(sum[:12])
}

// childrenOf lists an element's live children from scope, in index order.
//
// Connectors are skipped: a line's endpoints are ids, and a copied line would
// still point at the ORIGINAL two elements. Redrawing relationships inside a
// copy is a separate judgement, not something to do silently.
func (s *staging) childrenOf(parentID string) []*domain.Element {
	var out []*domain.Element
	for _, el := range s.scope.Elements {
		if el.Location.ParentID == parentID && !el.IsDeleted() && el.Type != domain.TypeLine {
			out = append(out, el)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Location.Index != out[j].Location.Index {
			return out[i].Location.Index < out[j].Location.Index
		}
		return out[i].ID < out[j].ID // stable, so the ids a preview shows are the ids committed
	})
	return out
}

// descendsFrom reports whether id sits anywhere under ancestor.
func (s *staging) descendsFrom(id, ancestor string) bool {
	for i := 0; i < 12 && id != ""; i++ {
		el, ok := s.scope.Elements[id]
		if !ok {
			return false
		}
		if el.Location.ParentID == ancestor {
			return true
		}
		id = el.Location.ParentID
	}
	return false
}

// runConvert changes what an element is while keeping what it says.
func (s *staging) runConvert(_ context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	target := domain.ElementType(strings.ToUpper(strings.TrimSpace(in.Becomes)))
	switch target {
	case domain.TypeDocument, domain.TypeCard, domain.TypeTaskList:
	default:
		return r.fail("I can convert something into a DOCUMENT, a CARD or a TASK_LIST, not a %q", in.Becomes)
	}
	if el.Type == target {
		return r.out(fmt.Sprintf("%s is already a %s; nothing staged.", el.ID, target))
	}
	// Converting the same element twice in one run.
	//
	// Scope is a snapshot: after the first conversion is STAGED the element is
	// still a CARD as far as the check above can see, so a second call sails
	// through. A live run did exactly that — two converts and two add_tasks on
	// one note, which would have put every item on the list twice.
	for i := range s.plan.Actions {
		if a := &s.plan.Actions[i]; a.Kind == ActConvert && a.ElementID == el.ID {
			return r.out(fmt.Sprintf(
				"%s is already staged to become a %s in this run — that is done, move on.",
				el.ID, a.Becomes))
		}
	}
	if el.Type != domain.TypeCard && el.Type != domain.TypeDocument {
		return r.fail("%s is a %s — only notes and documents convert. "+
			"Anything else would lose what it is", el.ID, el.Type)
	}
	text := sanitizeBody(firstNonEmpty(in.Text, in.Body, textOf(el)))
	if strings.TrimSpace(text) == "" {
		return r.fail("%s has nothing in it to carry across", el.ID)
	}

	if _, err := s.add(Action{
		Kind: ActConvert, ElementID: el.ID, Becomes: string(target), Text: text,
		Summary: fmt.Sprintf("%s becomes a %s", el.Type, target),
	}); err != nil {
		return r.fail("%v", err)
	}
	// A checklist's ITEMS are separate elements, which the conversion op cannot
	// create. Without this the person gets an empty list where their note was —
	// technically a conversion, and a total loss of the content.
	if target == domain.TypeTaskList {
		// The heading is the LIST's name, not its first item. convertOps takes
		// the first line as the title, and feeding the same text to
		// checklistLines put it on the list as well — so a note reading
		// "Delivery checklist / - Picture lock / …" became a list called
		// "Delivery checklist" whose first thing to tick off was
		// "Delivery checklist".
		_, body := splitHeading(text)
		items := checklistLines(body)
		if len(items) == 0 {
			return r.fail("%s does not read as a list of steps — there is nothing to tick off", el.ID)
		}
		if _, err := s.add(Action{
			Kind: ActAddTasks, ElementID: el.ID, Tasks: items, Index: 1,
			Summary: fmt.Sprintf("%d item(s) from the note", len(items)),
		}); err != nil {
			return r.fail("%v", err)
		}
		return r.out(fmt.Sprintf("Staged: converted to a checklist of %d item(s).", len(items)))
	}
	return r.out(fmt.Sprintf("Staged the conversion to a %s.", target))
}

// checklistLines turns a note's body into to-do items — one per non-empty line,
// with any bullet or numbering the person typed stripped off.
func checklistLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*•–— \t")
		// Strip a leading "1." or "2)" so the list is not numbered twice.
		if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 && allDigits(line[:i]) {
			line = strings.TrimSpace(line[i+1:])
		}
		if line = sanitizeName(line); line != "" {
			out = append(out, truncate(line, 200))
		}
		if len(out) >= 40 {
			break
		}
	}
	return out
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// cleanGrid validates and squares off a table the model supplied.
func cleanGrid(rows [][]string) ([][]string, error) {
	var out [][]string
	width := 0
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, c := range row {
			cells = append(cells, truncate(sanitizeName(c), 200))
		}
		out = append(out, cells)
		if len(cells) > width {
			width = len(cells)
		}
	}
	if len(out) == 0 || width == 0 {
		return nil, fmt.Errorf("a table needs at least one row of cells")
	}
	if len(out) > 60 || width > 12 {
		return nil, fmt.Errorf("that table is %d by %d — too big to read on a board", len(out), width)
	}
	// Pad short rows rather than refusing: a ragged grid renders with cells
	// missing, and rejecting costs a turn over something trivially fixable.
	for i := range out {
		for len(out[i]) < width {
			out[i] = append(out[i], "")
		}
	}
	return out, nil
}

// normalizeHex coerces what a model tends to write into what the swatch card
// can read. The card slices fixed offsets out of the string, so "1b2a4a" or
// "#abc" render as NaN rather than failing loudly.
func normalizeHex(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s != "" && !strings.HasPrefix(s, "#") {
		s = "#" + s
	}
	if len(s) == 4 {
		s = "#" + string([]byte{s[1], s[1], s[2], s[2], s[3], s[3]})
	}
	return s
}

// titleOf is an element's own name, for summaries. Falls back to its text so a
// note without a title still says something in the review list.
func titleOf(el *domain.Element) string {
	if el == nil {
		return ""
	}
	if t, _ := el.Content["title"].(string); strings.TrimSpace(t) != "" {
		return t
	}
	if t, _ := el.Content["caption"].(string); strings.TrimSpace(t) != "" {
		return t
	}
	return sanitizeText(textOf(el))
}

// firstNonEmpty is the first value that carries something. Whitespace does not
// count: a model that sends " " for a field it has nothing for should fall
// through to the next candidate rather than write a blank.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// splitHeading separates a title line from the list beneath it.
//
// Only when the first line reads as a heading: it carries no bullet and at
// least one line below it does. "Buy milk / Buy bread" is two items, not a
// heading and one item — guessing wrong there silently drops a task.
func splitHeading(text string) (string, string) {
	head, rest := splitFirstLine(text)
	if rest == "" || isBulleted(head) || !anyBulleted(rest) {
		return "", text
	}
	return head, rest
}

func isBulleted(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.ContainsRune("-*•–—", rune(line[0])) {
		return true
	}
	i := strings.IndexAny(line, ".)")
	return i > 0 && i <= 3 && allDigits(line[:i])
}

func anyBulleted(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if isBulleted(line) {
			return true
		}
	}
	return false
}
