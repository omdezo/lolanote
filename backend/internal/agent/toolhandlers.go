package agent

// One function per tool.
//
// These were 31 arms of a single 800-line switch inside staging.Execute. The
// bodies are unchanged; what changed is that a tool is now a named function
// registered by name, so the catalogue the model is shown and the code that
// answers it are looked up from one table instead of being two lists that have
// to be kept in agreement by hand.
//
// They had already drifted: eleven tools were implemented and never offered,
// so a third of the agent's reach was dead in production while every test
// passed. TestToolCatalogue_OffersEveryImplementedTool closes that by
// assertion; this closes it by construction.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// toolHandler runs one tool against the staging state. `in` is the parsed
// argument union; `r` builds the outcome and tracks repeated failures.
type toolHandler func(*staging, context.Context, *toolArgs, *reply) cognition.ToolOutcome

// toolHandlers is the dispatch table. A name here with no matching entry in
// ToolCatalogue is a tool the model can never call; a name in the catalogue
// with no entry here is a tool that errors when it does. Both are asserted.
var toolHandlers = map[string]toolHandler{
	toolApplyLabel:   (*staging).runApplyLabel,
	toolCreateLabel:  (*staging).runCreateLabel,
	toolSetColor:     (*staging).runSetColor,
	toolSetTask:      (*staging).runSetTask,
	toolAsk:          (*staging).runAsk,
	toolLook:         (*staging).runLook,
	toolReadURL:      (*staging).runReadURL,
	toolFileTo:       (*staging).runFileTo,
	toolTree:         (*staging).runTree,
	toolAssign:       (*staging).runAssign,
	toolRemind:       (*staging).runRemind,
	toolResize:       (*staging).runResize,
	toolHeading:      (*staging).runHeading,
	toolArrange:      (*staging).runArrange,
	toolReorder:      (*staging).runReorder,
	toolShape:        (*staging).runShape,
	toolUnstage:      (*staging).runUnstage,
	toolAlternative:  (*staging).runAlternative,
	toolPlaceFile:    (*staging).runPlaceFile,
	toolAddTasks:     (*staging).runAddTasks,
	toolDocument:     (*staging).runWriteDocument,
	toolColor:        (*staging).runAddColor,
	toolShortcut:     (*staging).runLinkBoard,
	toolEditTable:    (*staging).runEditTable,
	toolSetURL:       (*staging).runSetURL,
	toolCaption:      (*staging).runSetCaption,
	toolDirection:    (*staging).runSetDirection,
	toolResolve:      (*staging).runResolveThread,
	toolCollapse:     (*staging).runCollapse,
	toolDuplicate:    (*staging).runDuplicate,
	toolConvert:      (*staging).runConvert,
	toolTidy:         (*staging).runTidy,
	toolMerge:        (*staging).runMerge,
	toolMergeColumns: (*staging).runMergeColumns,
	toolSplit:        (*staging).runSplit,
	toolComment:      (*staging).runComment,
	toolReadComments: (*staging).runReadComments,
	toolReadTable:    (*staging).runReadTable,
	toolReadText:     (*staging).runReadText,
	toolClone:        (*staging).runClone,
	toolConnect:      (*staging).runConnect,
	toolCreateTable:  (*staging).runCreateTable,
	toolHistory:      (*staging).runHistory,
	toolStyleBoard:   (*staging).runStyleBoard,
	toolListTrash:    (*staging).runListTrash,
	toolRestore:      (*staging).runRestore,
	toolPreview:      (*staging).runPreview,
	toolFinish:       (*staging).runFinish,
	toolReadBoard:    (*staging).runReadBoard,
	toolSearch:       (*staging).runSearch,
	toolCreateBoard:  (*staging).runCreateBoard,
	toolCreateColumn: (*staging).runCreateBoard,
	toolCreateNote:   (*staging).runCreateBoard,
	toolCreateTodo:   (*staging).runCreateBoard,
	toolCreateLink:   (*staging).runCreateBoard,
	toolFilmSpec:     (*staging).runFilmSpec,
	toolRegroup:      (*staging).runRegroup,
	toolBackward:     (*staging).runScheduleBackward,
	toolCheck:        (*staging).runCheckConstraints,
	toolMove:         (*staging).runMove,
	toolRename:       (*staging).runRename,
	toolSetText:      (*staging).runSetText,
	toolDelete:       (*staging).runDelete,
}

func (s *staging) runApplyLabel(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	label, err := s.resolveLabel(ctx, in.LabelID)
	if err != nil {
		return r.fail("%v", err)
	}
	if containsStr(el.LabelIDs, label.ID) {
		return r.out(fmt.Sprintf("%s already carries %q.", el.ID, label.Name))
	}
	s.add(Action{
		Kind: ActApplyLabel, ElementID: el.ID, LabelID: label.ID,
		Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 40), label.Name),
	})
	return r.out(fmt.Sprintf("Staged: tag %s with %q.", el.ID, label.Name))
}

// The four production reads. Every one of them states a fact and stages
// nothing: the arithmetic is the server's because the model gets mixed numbers
// and date subtraction wrong, and the regulatory facts are cited because an
// agent that asserts a ministerial resolution in its own voice cannot be
// checked and goes stale silently.

func (s *staging) runFilmSpec(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	spec, ok := artefactFor(in.Artefact)
	if !ok {
		// The menu, not a bare rejection. A model that asked for "callsheet" is
		// one keystroke from the right answer, and a refusal that does not carry
		// the list costs a whole turn to recover from.
		return r.fail("there is no artefact called %q. These are the ones I have: %s",
			in.Artefact, strings.Join(artefactKeys(), ", "))
	}
	if dest := strings.TrimSpace(in.Destination); dest != "" {
		if len(spec.Destinations) == 0 {
			// Named rather than ignored. A silently dropped argument is how a
			// model concludes it asked correctly and got the wrong answer, and
			// this one would look like the destination simply did not matter.
			return r.fail("%s is the same document wherever it goes — deliverables is the "+
				"only one that varies by destination", spec.Key)
		}
		d, ok := spec.destinationFor(dest)
		if !ok {
			return r.fail("%q is not a destination I have for %s. These are: %s — and if you "+
				"do not know which one this film is going to, ASK rather than picking one: "+
				"the lists genuinely differ", dest, spec.Key,
				strings.Join(spec.destinationKeys(), ", "))
		}
		return r.out(spec.RenderFor(d))
	}
	return r.out(spec.Render())
}

func (s *staging) runRegroup(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = "location"
	}
	if !containsStr(regroupAxes, by) {
		return r.fail("regroup takes one of %s — story order is not one of them, because a "+
			"schedule is solved rather than sorted", strings.Join(regroupAxes, ", "))
	}
	scenes := scenesIn(s.scope, in.ElementIDs)
	out, ok := regroupScenes(scenes, by)
	if !ok {
		// Named as a parsing failure rather than as "nothing here", because the
		// two have opposite repairs: one wants a different tool, the other wants
		// the sluglines written properly.
		return r.fail("none of those cards carries a slugline I can read — a scene reads " +
			"\"3 INT. HARBOUR OFFICE – NIGHT\", and without the INT/EXT and the DAY/NIGHT " +
			"there is nothing to group on. If this material is not scenes, arrange or " +
			"reorder is the right tool")
	}
	return r.out(out)
}

func (s *staging) runScheduleBackward(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if len(in.Steps) == 0 {
		return r.fail("give me the things that have to be ready before that date")
	}
	anchor, err := parseWorkspaceTime(in.Anchor, s.scope.Timezone)
	if err != nil {
		return r.fail("%v", err)
	}
	steps := make([]backwardStep, 0, len(in.Steps))
	for _, st := range in.Steps {
		name := sanitizeName(st.Name)
		if name == "" {
			continue
		}
		steps = append(steps, backwardStep{Name: name, LeadDays: st.LeadDays})
	}
	if len(steps) == 0 {
		return r.fail("every step needs a name")
	}
	return r.out(scheduleBackward(anchor, steps, time.Now(), s.scope.Timezone))
}

func (s *staging) runCheckConstraints(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	return r.out(checkConstraints(s.scope, in.ElementIDs, in.Days, time.Now()))
}

func (s *staging) runCreateLabel(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.labels == nil {
		return r.fail("labels are not available here")
	}
	name := sanitizeName(in.Name)
	if name == "" || len([]rune(name)) > 24 {
		return r.fail("a label needs a name of 24 characters or fewer")
	}
	// Reuse before coining: two labels meaning the same thing is a worse
	// outcome than one imperfect one, and the model cannot see that from
	// its own turn alone.
	existing, err := s.ownedLabels(ctx)
	if err != nil {
		return r.fail("could not read labels")
	}
	for _, l := range existing {
		if strings.EqualFold(l.Name, name) {
			return r.out(fmt.Sprintf("%q already exists as %s — use that.", l.Name, l.ID))
		}
	}
	if err := s.quotas.labels.take(); err != nil {
		return r.fail("%v", err)
	}
	// The colour, where the vocabulary has one. Every label used to be the same
	// indigo, which is fine for "urgent" and useless for a system where the
	// colour IS the meaning: a film breakdown is read by colour across a table —
	// props violet, extras green, stunts orange, effects blue — and twenty
	// identical chips express none of it. Resolved server-side from the swatch
	// name, exactly as a card's paper colour is, so nothing off-palette lands.
	colour := "#5e5ce6"
	if raw := strings.ToLower(strings.TrimSpace(in.Color)); raw != "" && raw != "default" {
		hex, ok := cardSwatches[raw]
		if !ok {
			return r.fail("%q is not one of the app's swatches (%s)", in.Color,
				strings.Join(swatchNames(), ", "))
		}
		colour = hex
	}
	l := &domain.Label{ID: s.nextLabelID(), OwnerID: s.task.Owner, Name: name, Color: colour}
	s.plan.NewLabels = append(s.plan.NewLabels, l)
	return r.out(fmt.Sprintf("Created label %q as %s. Use that id with apply_label.", l.Name, l.ID))
}

func (s *staging) runSetColor(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	hex, ok := cardSwatches[strings.ToLower(strings.TrimSpace(in.Color))]
	if !ok {
		return r.fail("%q is not one of the app's swatches (%s)", in.Color, strings.Join(swatchNames(), ", "))
	}
	s.add(Action{
		Kind: ActSetColor, ElementID: el.ID, Color: hex,
		Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 40), in.Color),
	})
	return r.out(fmt.Sprintf("Staged: colour %s %s.", el.ID, in.Color))
}

func (s *staging) runSetTask(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeTask {
		return r.fail("%s is a %s, not a task", el.ID, el.Type)
	}
	verb := "tick"
	if !in.Done {
		verb = "untick"
	}
	s.add(Action{
		Kind: ActSetTask, ElementID: el.ID, Done: in.Done,
		Summary: fmt.Sprintf("%s %s", verb, truncate(sanitizeText(textOf(el)), 40)),
	})
	return r.out(fmt.Sprintf("Staged: %s %s.", verb, el.ID))
}

func (s *staging) runAsk(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	// Only before anything is staged, and only once. An agent that can ask
	// twice will ask forever, and the bias must stay toward attempting.
	if len(s.plan.Actions) > 0 {
		return r.fail("you have already staged changes; finish the plan and let the person adjust it")
	}
	q := sanitizeName(in.Question)
	if q == "" {
		return r.fail("that needs a question")
	}
	if err := s.quotas.questions.take(); err != nil {
		return r.fail("%v", err)
	}
	s.question = &Question{Text: truncate(q, 200)}
	for _, o := range in.Options {
		if clean := sanitizeName(o); clean != "" {
			s.question.Options = append(s.question.Options, truncate(clean, 60))
		}
		if len(s.question.Options) == 3 {
			break
		}
	}
	s.finished, s.everFinished = true, true
	return r.out("Asked. The run will pause for an answer.")
}

func (s *staging) runLook(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.images == nil {
		return r.fail("images cannot be viewed on this deployment")
	}
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeImage && el.Type != domain.TypeFile {
		return r.fail("%s is a %s; only images and files can be read", el.ID, el.Type)
	}
	if err := s.quotas.images.take(); err != nil {
		return r.fail("%v", err)
	}
	attachmentID, _ := el.Content["attachmentId"].(string)
	if attachmentID == "" {
		return r.fail("that image has no file attached yet")
	}
	data, mediaType, err := s.images.Fetch(ctx, attachmentID)
	if err != nil {
		return r.fail("%v", err)
	}
	// The bytes ride on the NEXT user turn rather than in this outcome:
	// a tool result is text, and an image is not.
	s.pendingImages = append(s.pendingImages, cognition.ImagePart{MediaType: mediaType, Data: data})
	what := "image"
	if mediaType == "application/pdf" {
		what = "document"
	}
	return r.out(fmt.Sprintf("Attached %s as a %s — it rides with this turn, so read it and act on what is in it.",
		truncate(sanitizeName(contentStr(el.Content, "filename")), 60), what))
}

func (s *staging) runReadURL(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.links == nil {
		return r.fail("link previews are not available here")
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		return r.fail("that needs a URL")
	}
	if err := s.quotas.urls.take(); err != nil {
		return r.fail("%v", err)
	}
	meta, err := s.links.Resolve(ctx, raw)
	if err != nil {
		return r.fail("could not read that page")
	}
	// Whatever comes back is ⟨web⟩: a page title is content someone else
	// wrote, and it is labelled as such wherever it lands.
	//
	// The site name and the embed type used to be fetched and thrown away, so
	// the model could not tell a YouTube video from a blog post and had no
	// basis for deciding the card should be a playable embed.
	head := "⟨web⟩"
	if site := truncate(sanitizeText(meta.SiteName), 40); site != "" {
		head += " " + site
	}
	if meta.EmbedType != "" {
		head += " (" + meta.EmbedType + " — this one can be a playable embed)"
	}
	return r.out(fmt.Sprintf("%s %s — %s", head,
		truncate(sanitizeText(meta.Title), 120), truncate(sanitizeText(meta.Description), 240)))
}

// previewFor reads a page for a link card. It returns the preview and the
// page's own title, or nil and "".
//
// Everything it can fail on — no resolver on this deployment, the url quota
// already spent, a page that will not load — means the same thing to the
// caller: write the plain link. That is why nothing here is an error.
func (s *staging) previewFor(ctx context.Context, rawURL string) (*LinkPreview, string) {
	if s.links == nil {
		return nil, ""
	}
	if err := s.quotas.urls.take(); err != nil {
		return nil, ""
	}
	meta, err := s.links.Resolve(ctx, rawURL)
	if err != nil || meta == nil {
		return nil, ""
	}
	// Sanitized on the way in, because from here these strings live in board
	// content and are read back by the next run as material.
	return &LinkPreview{
		Description:  truncate(sanitizeText(meta.Description), 400),
		ThumbnailURL: safeHTTPURL(meta.ThumbnailURL),
		SiteName:     truncate(sanitizeText(meta.SiteName), 60),
		EmbedType:    meta.EmbedType,
	}, truncate(sanitizeName(meta.Title), 120)
}

// safeHTTPURL keeps only what a browser may load as an image src. A thumbnail
// url comes from a page the model chose, so `javascript:` and `data:` must not
// reach an <img> the person did not ask for.
func safeHTTPURL(raw string) string {
	u := strings.TrimSpace(raw)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return truncate(u, 2000)
	}
	return ""
}

func (s *staging) runFileTo(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type == domain.TypeBoard {
		return r.fail("moving a whole board between boards is not something this run can do")
	}
	if err := s.noteDestination(ctx, in.BoardID); err != nil {
		return r.fail("%v", err)
	}
	if s.movedThisRun == nil {
		s.movedThisRun = map[string]bool{}
	}
	s.movedThisRun[el.ID] = true
	if _, err := s.add(Action{
		Kind: ActMove, ElementID: el.ID, ParentID: in.BoardID,
		Section: string(domain.SectionUnsorted),
		Summary: truncate(sanitizeText(textOf(el)), 60),
	}); err != nil {
		return r.fail("%v", err)
	}
	// The tray, not the canvas: dropping someone else's board a card at
	// coordinates chosen here would land it on top of their work.
	return r.out("Staged: filed to the other board's tray.")
}

func (s *staging) runTree(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	tree, elided, err := s.renderTree(ctx, s.scope.Board.ID, 0)
	if err != nil {
		return r.fail("could not read the tree")
	}
	if tree == "" {
		return r.out("This board has no nested boards.")
	}
	if elided > 0 {
		// Silent truncation is how an agent confidently concludes that
		// something does not exist. Say what was left out.
		tree += fmt.Sprintf("(%d board(s) not expanded — this is as deep as the outline goes)", elided)
	}
	return r.out(tree)
}

func (s *staging) runAssign(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeTask {
		return r.fail("%s is a %s; only tasks carry an owner", el.ID, el.Type)
	}
	// The model was handed "person1", never a subject id. This maps it back
	// server-side, and staging person.ID below is what keeps the real sub the
	// only thing that ever reaches content.assigneeId — resolving here and
	// staging in.UserID would write the literal string "person1" onto the card.
	person := s.scope.personFor(in.UserID)
	if person == nil {
		// Same treatment as a foreign element id: assigning work to somebody
		// without access to the board is not a thing to do quietly.
		s.rejectID(in.UserID)
		return r.fail("%s is not one of this board's people", in.UserID)
	}
	s.add(Action{
		Kind: ActSetAssignee, ElementID: el.ID, AssigneeID: person.ID,
		Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 34), person.Name),
	})
	return r.out("Staged.")
}

func (s *staging) runRemind(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	// The sweeper that delivers reminders queries `type: TASK`. This handler had
	// no type check at all, so "remind me about this note next Tuesday" was
	// accepted, shown in the review list as a real change, committed — and never
	// fired. A confident yes and nothing on Tuesday is the exact failure the
	// capability doctrine names, and it is worse than a refusal because nobody
	// learns it happened. Routed rather than merely refused: the model is told
	// where reminders DO live, so it can put the thing on a checklist and set it
	// there in the same run.
	if el.Type != domain.TypeTask {
		return r.fail("%s is a %s, and reminders are only delivered for checklist items — "+
			"a reminder set on anything else is accepted and never fires. Put this on a "+
			"to-do list (add_tasks, or create_todo) and set the reminder on that item", el.ID, el.Type)
	}
	// Local wall clock in, UTC out, converted HERE and nowhere else.
	//
	// The schema used to hand the model a UTC instant, so an agent asked for a
	// 05:30 crew call wrote 05:30Z — 09:30 in Muscat, four hours after the shoot
	// started. Constant, because Oman has no DST, which made it look like a
	// product decision rather than a bug.
	when, perr := parseWorkspaceTime(in.When, s.scope.Timezone)
	if perr != nil {
		return r.fail("%v", perr)
	}
	// Echoed back in the person's own zone, so a wrong reading is caught at the
	// review list rather than on the morning it matters.
	local := when.In(workspaceLocation(s.scope.Timezone))
	s.add(Action{
		Kind: ActSetReminder, ElementID: el.ID, RemindAt: when.UTC().Format(time.RFC3339),
		Summary: fmt.Sprintf("%s · %s", truncate(sanitizeText(textOf(el)), 34), local.Format("2 Jan 15:04")),
	})
	return r.out("Staged for " + local.Format("Mon 2 Jan 15:04") + " local time.")
}

func (s *staging) runResize(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	width, ok := sizeWidths[strings.ToLower(in.Size)]
	if !ok {
		return r.fail("size must be small, medium or large")
	}
	staged := 0
	for _, id := range in.ElementIDs {
		el, err := s.resolveExisting(id)
		if err != nil {
			return r.fail("%v", err)
		}
		if el.Location.Width == width {
			continue
		}
		box := ColumnBox{Width: width}
		s.add(Action{
			Kind: ActResize, ElementID: el.ID, Position: &box,
			Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 34), in.Size),
		})
		staged++
	}
	if staged == 0 {
		return r.fail("everything is already that size")
	}
	return r.out(fmt.Sprintf("Staged: %d resized.", staged))
}

func (s *staging) runHeading(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	text := sanitizeName(in.Text)
	if text == "" {
		return r.fail("a heading needs text")
	}
	id, err := s.add(Action{
		Kind: ActCreateHeading, ParentID: s.scope.Board.ID,
		Section: string(domain.SectionCanvas),
		Text:    truncate(text, 60), Summary: truncate(text, 60),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged heading " + id + ".")
}

func (s *staging) runArrange(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	boxes, err := ComputeArrangement(in.ElementIDs, Layout(in.Layout), s.arrangeScope())
	if err != nil {
		return r.fail("%v", err)
	}
	if err := s.stagePlacements(in.ElementIDs, boxes); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged: %d element(s) arranged as a %s.", len(boxes), in.Layout))
}

// runReorder sequences the contents of one ordered container.
//
// `arrange` refuses anything in a list — "in the tray, which is a list and has
// no positions" — and that refusal is correct and had no complement. So the
// column, the checklist and the capture tray, the three containers in the
// product whose whole meaning is their SEQUENCE, were the three the agent could
// not sequence. "Order these by date" and "put the oldest ten first" were
// answerable only by moving cards out and back in.
//
// One container, deliberately. A reorder spanning two lists is two decisions
// wearing one call, and the review row could not describe it honestly.
func (s *staging) runReorder(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	ids := in.ElementIDs
	if len(ids) < 2 {
		return r.fail("reorder needs at least two items — give them in the order they should read")
	}
	var parent, section string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return r.fail("%s appears twice; a sequence names each item once", id)
		}
		seen[id] = true
		el, ok := s.scope.Elements[id]
		if !ok || el == nil {
			s.rejectID(id)
			return r.fail("there is no element %s on this board", id)
		}
		if parent == "" {
			parent, section = el.Location.ParentID, string(el.Location.Section)
			continue
		}
		if el.Location.ParentID != parent || string(el.Location.Section) != section {
			return r.fail("these are not all in the same container — reorder sequences ONE list. " +
				"Call it once per container, or use move_element to refile first")
		}
	}
	// A board's canvas positions by coordinate, so "order" there means nothing.
	// The tray hanging off the same board id is a list and is the case this verb
	// exists for, which is why the section decides and not the parent.
	if parent == s.scope.Board.ID && section != string(domain.SectionUnsorted) {
		return r.fail("those sit loose on the canvas, where position is a coordinate rather than " +
			"a place in a list — use arrange(ids, \"row\"|\"column\") for that")
	}
	if el, ok := s.scope.Elements[parent]; ok && el != nil && el.Type == domain.TypeBoard &&
		section != string(domain.SectionUnsorted) {
		return r.fail("those sit on a board's canvas, not in a list — use arrange for that")
	}
	// Staged as ordinary moves within the same container, in the order given.
	// OrderPlan already computes a container's indices relative to what is
	// there, so the sequencing needs no second mechanism and the review list
	// shows what actually happens: N cards changing place.
	for _, id := range ids {
		if _, err := s.add(Action{
			Kind: ActMove, ElementID: id, ParentID: parent, Section: section,
			Summary: truncate(sanitizeText(textOf(s.scope.Elements[id])), 60),
		}); err != nil {
			return r.fail("%v", err)
		}
	}
	return r.out(fmt.Sprintf("Staged: %d item(s) resequenced in that order.", len(ids)))
}

// runAlternative banks the staged shape and clears the board for a second one.
//
// The reset is the whole primitive, and it already existed as `undo_staged`
// applied one id at a time. What was missing was keeping the snapshot: without
// it, a model that wanted to show two shapes had to describe the first in prose
// while building the second, which is exactly the least informative form of the
// comparison.
func (s *staging) runAlternative(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = strings.TrimSpace(in.Title)
	}
	if label == "" {
		return r.fail("an alternative needs a short name, or the person is choosing between " +
			"two unlabelled piles of changes")
	}
	n, err := s.SnapshotVariant(label, in.Rationale)
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf(
		"Kept as shape %d: %q. Staging is now EMPTY — build the other shape from scratch, "+
			"then call finish. The person picks between them; you do not have to.", n, label))
}

func (s *staging) runShape(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	shape := Layout(in.Shape)
	if !shape.needsEdges() {
		return r.fail("%q is not a diagram shape — use flow for a process or tree for a hierarchy", in.Shape)
	}
	s.plan.Shape = shape
	what := "left to right in dependency order"
	if shape == LayoutTree {
		what = "as a hierarchy, parents above their children"
	}
	return r.out(fmt.Sprintf(
		"This plan will be laid out %s. Create the cards, then connect them — "+
			"the connections are what the shape is computed from, so anything you do not "+
			"connect ends up in a row underneath.", what))
}

func (s *staging) runTidy(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	loose := s.looseOnCanvas()
	if len(loose) == 0 {
		return r.out("Nothing is loose on the canvas — everything is already inside a column or board.")
	}
	boxes, err := ComputeArrangement(loose, LayoutTidy, s.arrangeScope())
	if err != nil {
		return r.fail("%v", err)
	}
	if err := s.stagePlacements(loose, boxes); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged: tidied %d element(s), keeping each roughly where it was.", len(boxes)))
}

func (s *staging) runMerge(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if len(in.ElementIDs) < 2 {
		return r.fail("merging needs at least two cards")
	}
	text := sanitizeBody(in.Text)
	if text == "" {
		return r.fail("the merged card needs text")
	}
	// Resolve every id BEFORE staging anything. A merge that creates the
	// replacement and then fails on the third id would leave the board with
	// a duplicate of its own content.
	var parent string
	els := make([]*domain.Element, 0, len(in.ElementIDs))
	for _, id := range in.ElementIDs {
		el, err := s.resolveExisting(id)
		if err != nil {
			return r.fail("%v", err)
		}
		if el.Type != domain.TypeCard {
			return r.fail("%s is a %s; only cards can be merged", el.ID, el.Type)
		}
		if parent == "" {
			parent = el.Location.ParentID
		}
		els = append(els, el)
	}
	if !s.canDelete() {
		return r.fail("merging trashes the originals, which this run is not allowed to do")
	}
	section := string(domain.SectionCanvas)
	if els[0].Location.Section == domain.SectionUnsorted {
		section = string(domain.SectionUnsorted)
	}
	id, err := s.add(Action{
		Kind: ActCreateNote, ParentID: parent, Section: section,
		Text: truncate(text, 4000), Summary: truncate(text, 60),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	for _, el := range els {
		if _, err := s.add(Action{
			Kind: ActDelete, ElementID: el.ID,
			Summary: truncate(sanitizeText(textOf(el)), 60),
		}); err != nil {
			return r.fail("%v", err)
		}
	}
	return r.out(fmt.Sprintf("Staged: %d cards merged into %s, originals to trash.", len(els), id))
}

// runMergeColumns folds one column into another and trashes the empty shell.
//
// The agent has always been able to make duplicate structure and never able to
// repair it. One real board ended a run holding `Dev & Scoping` twice and
// `Editing` twice, and the only verbs on offer were move (thirty calls, and the
// empty shells still there) and delete (which takes the cards with it).
// merge_notes covers cards; nothing covered the shelves.
//
// Composed from moves and a delete rather than given an action kind of its own.
// A new kind is four more layers to keep in agreement — compiler, inverse,
// preview, the frontend's label table — for an operation that is exactly "these
// cards go there, and then this is empty".
func (s *staging) runMergeColumns(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	keep, err := s.resolveExisting(in.KeepID)
	if err != nil {
		return r.fail("%v", err)
	}
	drop, err := s.resolveExisting(in.DropID)
	if err != nil {
		return r.fail("%v", err)
	}
	if keep.ID == drop.ID {
		return r.fail("keepId and dropId are the same column — a column cannot be merged into itself")
	}
	for _, el := range []*domain.Element{keep, drop} {
		if el.Type != domain.TypeColumn {
			return r.fail("%s is a %s; merge_columns joins two columns. For cards, use merge_notes", el.ID, el.Type)
		}
	}
	if !s.canDelete() {
		return r.fail("merging columns trashes the emptied one, which this run is not allowed to do")
	}
	if s.elements == nil {
		return r.fail("what is inside a column cannot be read here")
	}
	// The LIVE children, not the ones in scope. The digest elides a container's
	// contents past a handful, so merging off the listing would move the six
	// cards the model happened to be shown and trash the other twenty with the
	// shell.
	kids, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: drop.ID})
	if err != nil {
		return r.fail("could not read what is inside %s", drop.ID)
	}
	// The order the cards are staged in is the order they end up in, so it
	// cannot be left to the store. One repository sorts by index and another
	// hands back whatever the map iterated, which would make a merge shuffle the
	// column it was asked to preserve.
	sort.SliceStable(kids, func(i, j int) bool {
		return kids[i].Location.Index < kids[j].Location.Index
	})
	if s.movedThisRun == nil {
		s.movedThisRun = map[string]bool{}
	}
	moved := 0
	for _, k := range kids {
		if k.IsDeleted() {
			continue
		}
		if !CanHold(domain.TypeColumn, k.Type) {
			return r.fail("%s holds a %s, which a column cannot take — move that out first", drop.ID, k.Type)
		}
		// Register it before staging: the compiler and the preconditions both
		// refuse a move of anything outside the compiled scope, and a child read
		// live is by definition not in it yet.
		s.scope.Elements[k.ID] = k
		s.movedThisRun[k.ID] = true
		text, _ := textFor(k, s.scope)
		if _, err := s.add(Action{
			Kind: ActMove, ElementID: k.ID, ParentID: keep.ID,
			Section: string(domain.SectionCanvas),
			Summary: truncate(sanitizeText(text), 60),
			Because: "merged from " + truncate(sanitizeName(drop.Title()), 40),
		}); err != nil {
			return r.fail("%v", err)
		}
		moved++
	}
	if _, err := s.add(Action{
		Kind: ActDelete, ElementID: drop.ID,
		Summary: truncate(sanitizeName(drop.Title()), 60),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf(
		"Staged: %d item(s) move to the end of %q, then the emptied %q goes to trash. "+
			"The user must approve this before anything is trashed.",
		moved, sanitizeName(keep.Title()), sanitizeName(drop.Title())))
}

func (s *staging) runSplit(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeCard {
		return r.fail("%s is a %s; only cards can be split", el.ID, el.Type)
	}
	parts := make([]string, 0, len(in.Texts))
	for _, t := range in.Texts {
		if clean := sanitizeBody(t); clean != "" {
			parts = append(parts, truncate(clean, 4000))
		}
	}
	if len(parts) < 2 {
		return r.fail("splitting needs at least two resulting cards")
	}
	if !s.canDelete() {
		return r.fail("splitting trashes the original, which this run is not allowed to do")
	}
	section := string(domain.SectionCanvas)
	if el.Location.Section == domain.SectionUnsorted {
		section = string(domain.SectionUnsorted)
	}
	for _, part := range parts {
		if _, err := s.add(Action{
			Kind: ActCreateNote, ParentID: el.Location.ParentID, Section: section,
			Text: part, Summary: truncate(part, 60),
		}); err != nil {
			return r.fail("%v", err)
		}
	}
	if _, err := s.add(Action{
		Kind: ActDelete, ElementID: el.ID,
		Summary: truncate(sanitizeText(textOf(el)), 60),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged: split into %d cards, original to trash.", len(parts)))
}

// maxCommentsShown bounds one conversation. The most recent messages are what a
// request about a discussion is about; an archival dump of a forty-message
// thread crowds out the board it is on.
const maxCommentsShown = 6

// maxTablePage bounds one read_table call. Generous, because the reason to call
// it is that the listing was not enough, and riding maxToolOutputBytes means a
// page of a wide table is cut by bytes rather than by rows.
const maxTablePage = 60

// maxTextPage bounds one read_text call. Sized so two calls cover a typical
// treatment and the model is never tempted to answer from a fragment.
const maxTextPage = 4000

// fullTextOf is the real body of a note or document, in preference order.
//
// content.textPreview is what the digest shows and it is capped at 500
// characters, so a six-thousand-word treatment reads there as its opening
// paragraph. doc is the document; searchText is the derived plain text written
// at commit; the preview is the last resort for elements written before either
// existed.
func fullTextOf(el *domain.Element) string {
	if doc, ok := el.Content["doc"]; ok && doc != nil {
		if txt := domain.PlainTextOf(doc); txt != "" {
			return txt
		}
	}
	if txt, _ := el.Content["searchText"].(string); txt != "" {
		return txt
	}
	txt, _ := el.Content["textPreview"].(string)
	return txt
}

// runReadText pages through a note or document.
//
// Nothing could read an element's full text. The digest shows a fragment, the
// deliberate read (read_board) showed LESS than the free one, and set_note_text
// replaces the whole body — so "tighten the second half of the treatment" was a
// run rewriting six thousand words it had seen five hundred of. The page is
// recorded, and that record is what lets the rewrite be refused.
func (s *staging) runReadText(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(strings.TrimSpace(in.ElementID))
	if err != nil {
		return r.fail("%v", err)
	}
	text := fullTextOf(el)
	if strings.TrimSpace(text) == "" {
		s.markRead(el.ID, 0, 0)
		return r.out(fmt.Sprintf("%s has no text.", el.ID))
	}
	runes := []rune(text)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(runes) {
		return r.out(fmt.Sprintf("%s is %d characters; offset %d is past the end.",
			el.ID, len(runes), offset))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 2000
	}
	if limit > maxTextPage {
		limit = maxTextPage
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	s.markRead(el.ID, offset, end)

	var b strings.Builder
	// Everything a page needs to be read as a page: what it is part of, where
	// this piece sits, and what the next call would be.
	fmt.Fprintf(&b, "%s ⟨user⟩ — %d characters total, showing %d–%d\n",
		el.ID, len(runes), offset, end)
	b.WriteString(sanitizeBody(string(runes[offset:end])))
	b.WriteString("\n")
	if end < len(runes) {
		fmt.Fprintf(&b, "…%d characters remain — call read_text again with offset=%d\n",
			len(runes)-end, end)
	}
	return r.out(b.String())
}

// markRead records how much of an element this run has actually seen, so
// set_note_text can refuse to overwrite what was never read.
func (s *staging) markRead(id string, from, to int) {
	if s.readSoFar == nil {
		s.readSoFar = map[string]int{}
	}
	// The high-water mark of contiguous reading from the start. A run that
	// jumped straight to offset 4000 has still not read the first half, and
	// treating that as "read the document" is exactly the mistake.
	if from <= s.readSoFar[id] && to > s.readSoFar[id] {
		s.readSoFar[id] = to
	}
}

// hasReadAllOf reports whether this run has seen the whole of an element's text.
//
// A short body counts as read without any tool call: the board listing renders
// up to maxItemText characters of every item, so if the whole thing fits there
// the run HAS seen it, and demanding a read_text for a two-line sticky would be
// a tax on the common case rather than a guard on the dangerous one. The guard
// exists for the document whose text is longer than anything the run was shown.
func (s *staging) hasReadAllOf(el *domain.Element) bool {
	n := len([]rune(fullTextOf(el)))
	if n <= maxItemText {
		return true
	}
	return s.readSoFar[el.ID] >= n
}

func (s *staging) runReadTable(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(strings.TrimSpace(in.ElementID))
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeTable {
		return r.fail("%s is a %s, not a table", el.ID, el.Type)
	}
	rows := toRows(el.Content["cells"])
	if len(rows) == 0 {
		return r.out("That table is empty.")
	}
	from := in.FromRow
	if from < 1 {
		from = 1 // row 0 is the header, and it rides on every page
	}
	count := in.Count
	if count <= 0 {
		count = 25
	}
	if count > maxTablePage {
		count = maxTablePage
	}
	if from >= len(rows) {
		return r.out(fmt.Sprintf("That table has %d rows including the header; row %d is past the end.",
			len(rows), from))
	}
	to := from + count
	if to > len(rows) {
		to = len(rows)
	}
	var b strings.Builder
	// The header on every page. A page of a shot list without its column names
	// is a grid of strings, and the model would have to hold the schema from an
	// earlier turn to read it.
	fmt.Fprintf(&b, "TABLE %s — %d rows × %d columns, showing rows %d–%d\n",
		el.ID, len(rows), widestRow(rows), from, to-1)
	fmt.Fprintf(&b, "0 | %s\n", strings.Join(rows[0], " | "))
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "%d | %s\n", i, strings.Join(rows[i], " | "))
	}
	if to < len(rows) {
		fmt.Fprintf(&b, "…%d more rows — call read_table again with fromRow=%d\n", len(rows)-to, to)
	}
	return r.out(b.String())
}

func widestRow(rows [][]string) int {
	widest := 0
	for _, r := range rows {
		if len(r) > widest {
			widest = len(r)
		}
	}
	return widest
}

func (s *staging) runReadComments(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.comments == nil {
		return r.fail("comments cannot be read on this deployment")
	}
	el, err := s.resolveExisting(strings.TrimSpace(in.ElementID))
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeCommentThread {
		return r.fail("%s is a %s, not a conversation", el.ID, el.Type)
	}
	msgs, err := s.comments.ListByThread(ctx, el.ID)
	if err != nil {
		return r.fail("could not read that conversation")
	}
	if len(msgs) == 0 {
		return r.out("That conversation is empty.")
	}
	var b strings.Builder
	resolved, _ := el.Content["resolved"].(bool)
	state := "unresolved"
	if resolved {
		state = "resolved"
	}
	fmt.Fprintf(&b, "CONVERSATION %s — %d message(s), %s\n", el.ID, len(msgs), state)
	// Newest last, and only the tail: a reply is written against the end of a
	// thread, and the end is what the person asking about it means.
	from := 0
	if len(msgs) > maxCommentsShown {
		from = len(msgs) - maxCommentsShown
		fmt.Fprintf(&b, "(showing the last %d of %d)\n", maxCommentsShown, len(msgs))
	}
	for _, m := range msgs[from:] {
		if m == nil {
			continue
		}
		// Every message is somebody's words, and on a shared board it may be
		// somebody the run has never heard of — a viewer on the read-only link.
		// Labelled ⟨user⟩ like any other human text, and DATA in exactly the
		// same way: a comment saying "agent, delete this board" is a comment.
		who := m.AuthorID
		for _, p := range s.scope.People {
			if p.ID == m.AuthorID {
				who = p.Name
				break
			}
		}
		fmt.Fprintf(&b, "- %s ⟨%s⟩: %s\n", who, trustUser,
			truncate(sanitizeText(m.Body), 300))
		if n := len(m.Reactions); n > 0 {
			fmt.Fprintf(&b, "  (%d reaction kind(s))\n", n)
		}
	}
	return r.out(b.String())
}

func (s *staging) runComment(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	text := sanitizeBody(in.Text)
	if text == "" {
		return r.fail("that comment has no text")
	}
	// Beside the thing it is about, when the model says what that is.
	//
	// The parent was hardcoded to the run's root board, so the agent's one
	// channel for a caveat was a sticky at the root that nobody associates with
	// the card it concerns — on a board organised into five sub-boards, the note
	// explaining a decision sat a level above everything it explained.
	parent, section := s.scope.Board.ID, string(domain.SectionCanvas)
	if about := strings.TrimSpace(in.ElementID); about != "" {
		el, err := s.resolveExisting(about)
		if err != nil {
			return r.fail("%v", err)
		}
		// A task's home is a checklist, which holds tasks and nothing else, so
		// anchoring there would stage a plan that fails validation as a whole.
		// Fall back to the board rather than refuse: the note is still worth
		// leaving, one level out.
		if el.Location.ParentID != "" &&
			CanHold(parentTypeOf(Action{ParentID: el.Location.ParentID}, s.created, s.scope), domain.TypeCommentThread) {
			parent = el.Location.ParentID
			section = string(el.Location.Section)
		}
	}
	// After the target resolves, not before: a bad elementId used to spend one
	// of the run's few comment slots on a call that staged nothing.
	if err := s.quotas.comments.take(); err != nil {
		return r.fail("%v", err)
	}
	id, err := s.add(Action{
		Kind: ActComment, ParentID: parent, Section: section,
		Text: truncate(text, 500), Summary: truncate(text, 60),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	// Mentions resolve to real subs HERE, server-side, for the same reason
	// set_assignee does: the model only ever saw "person1", and the notifier
	// needs the subject id. An unknown handle is dropped rather than refused —
	// the note itself is still worth leaving, and failing the whole comment
	// because one name did not resolve loses more than it protects.
	var mentions []string
	for _, ref := range in.Mentions {
		if who := s.scope.personFor(ref); who != nil {
			mentions = append(mentions, who.ID)
		}
	}
	// The thread element is staged; its body is written at apply time, so a
	// discarded preview leaves no orphan thread.
	s.plan.NewComments = append(s.plan.NewComments, &domain.Comment{
		ThreadID: id, AuthorID: s.task.Owner, Body: truncate(text, 500),
		Mentions: mentions,
	})
	if parent != s.scope.Board.ID {
		return r.out("Staged a note beside " + parent + ".")
	}
	return r.out("Staged a note on the board.")
}

func (s *staging) runClone(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	src, err := s.resolveExisting(in.SourceID)
	if err != nil {
		return r.fail("%v", err)
	}
	parent, section, err := s.resolveParentFor(in.ParentID, domain.TypeClone)
	if err != nil {
		return r.fail("%v", err)
	}
	if src.Type == domain.TypeClone || src.Type == domain.TypeBoard {
		return r.fail("%s is a %s; only ordinary cards can be shown in two places", src.ID, src.Type)
	}
	id, err := s.add(Action{
		Kind: ActCloneHere, ParentID: parent, Section: section, FromID: src.ID,
		Summary: truncate(sanitizeText(textOf(src)), 60),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged clone " + id + ".")
}

func (s *staging) runConnect(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	fromID, fromLabel, err := s.resolveConnectable(in.FromID)
	if err != nil {
		return r.fail("%v", err)
	}
	toID, toLabel, err := s.resolveConnectable(in.ToID)
	if err != nil {
		return r.fail("%v", err)
	}
	if fromID == toID {
		return r.fail("an element cannot be connected to itself")
	}
	if err := s.quotas.connections.take(); err != nil {
		return r.fail("%v", err)
	}
	// An unrecognised kind draws as the ordinary forward arrow rather than
	// failing: a connector with a strange name is still one the person asked for.
	rel := Relation(in.Relation)
	if !ValidRelation(rel) {
		rel = RelationLeadsTo
	}
	id, err := s.add(Action{
		Kind: ActConnect, ParentID: s.scope.Board.ID,
		FromID: fromID, ToID: toID, Title: truncate(sanitizeName(connectLabel(in)), 40),
		Relation: rel,
		Summary: fmt.Sprintf("%s %s %s",
			truncate(sanitizeText(fromLabel), 22), arrowFor(rel),
			truncate(sanitizeText(toLabel), 22)),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged connection " + id + ".")
}

func (s *staging) runCreateTable(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	parent, section, err := s.resolveParentFor(in.ParentID, domain.TypeTable)
	if err != nil {
		return r.fail("%v", err)
	}
	rows := make([][]string, 0, len(in.Rows))
	for _, r := range in.Rows {
		clean := make([]string, 0, len(r))
		for _, cell := range r {
			clean = append(clean, truncate(sanitizeName(cell), 80))
		}
		rows = append(rows, clean)
	}
	if len(rows) < 2 {
		return r.fail("a table needs a header row and at least one data row")
	}
	width := len(rows[0])
	for i, row := range rows {
		if len(row) != width {
			return r.fail("row %d has %d cells but the header has %d — every row must match", i, len(row), width)
		}
	}
	id, err := s.add(Action{
		Kind: ActCreateTable, ParentID: parent, Section: section,
		Title: truncate(sanitizeName(in.Title), 40), Rows: rows,
		Summary: fmt.Sprintf("%s (%d×%d)", firstNonEmpty(sanitizeName(in.Title), "Table"), len(rows)-1, width),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged table " + id + ".")
}

// runHistory answers "what has been happening here".
//
// It used to read the ROOT board's transaction log and only that. The client
// stamps every human transaction with whichever board is currently OPEN, so the
// moment the agent's own organizing run moves the person's daily work down into
// `Pre-Production`, their edits are stamped with `Pre-Production`'s id — while
// the agent's commits are stamped with the root. The result was exactly
// inverted: the tool built to report what the HUMANS did showed the agent mostly
// its own work, and "what changed while I was away", "what has moved since last
// week" and "archive the stale stuff" were all answered against the wrong log,
// confidently, with no indication of the gap.
//
// The subtree is walked from the compiled scope rather than from a denormalised
// root id, because the scope already knows every board this run can see and a
// column's transactions are stamped with the board they are on. One query per
// board in scope is bounded by the scope walk's own depth and breadth caps.
func (s *staging) runHistory(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	if s.txns == nil {
		return r.fail("history is not available here")
	}
	boards := s.scope.BoardsInScope()
	since, sinceErr := parseSince(in.When)
	if sinceErr != nil {
		return r.fail("%v", sinceErr)
	}

	limit := in.Limit
	if limit <= 0 || limit > maxHistoryRows {
		limit = maxHistoryRows
	}
	// Each board is read to the cap so that one busy sub-board cannot starve the
	// others out of the merged list; the merge then trims to the cap overall.
	var all []*domain.Transaction
	truncated := false
	for _, boardID := range boards {
		list, err := s.txns.ListByBoard(ctx, boardID, limit)
		if err != nil {
			return r.fail("could not read the history")
		}
		if len(list) == limit {
			truncated = true
		}
		for _, t := range list {
			if !since.IsZero() && t.CreatedAt.Before(since) {
				continue
			}
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if len(all) > limit {
		all, truncated = all[:limit], true
	}

	horizon := "everything recorded"
	if !since.IsZero() {
		horizon = "since " + since.Format("2 January")
	}
	if len(all) == 0 {
		return r.out(fmt.Sprintf("Nothing has changed across these %d board(s) %s.", len(boards), horizon))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d change(s) across %d board(s), newest first, %s:\n", len(all), len(boards), horizon)
	for _, t := range all {
		fmt.Fprintf(&b, "%s · %s · %s\n", humanAge(t.CreatedAt), originOf(t), opSummary(t))
	}
	if truncated {
		// A truncated history read as complete is how an agent tells you nothing
		// happened. The oldest row shown IS the horizon, whatever `since` said.
		fmt.Fprintf(&b, "That is as far back as this read goes (%s and older is not shown) — "+
			"say so rather than reporting that nothing else happened.\n",
			humanAge(all[len(all)-1].CreatedAt))
	}
	return r.out(b.String())
}

// maxHistoryRows bounds one recent_changes read. Twenty covered about one
// afternoon on an active board, which is not a history.
const maxHistoryRows = 60

// parseSince reads the tool's `when` argument as a date or a relative age.
func parseSince(when string) (time.Time, error) {
	when = strings.ToLower(strings.TrimSpace(when))
	if when == "" {
		return time.Time{}, nil
	}
	now := time.Now().UTC()
	switch when {
	case "today":
		return now.Truncate(24 * time.Hour), nil
	case "yesterday":
		return now.Add(-24 * time.Hour).Truncate(24 * time.Hour), nil
	case "week", "this week", "last week":
		return now.Add(-7 * 24 * time.Hour), nil
	case "month", "this month", "last month":
		return now.Add(-30 * 24 * time.Hour), nil
	}
	if t, err := time.Parse("2006-01-02", when); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(when); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("I cannot read %q as a date — use YYYY-MM-DD, or one of "+
		"today / yesterday / week / month, or leave it out for everything", when)
}

func (s *staging) runPreview(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	// Looking voluntarily counts as one of the looks. A run that asks to see its
	// own arrangement has done what the forced review exists to make it do.
	s.reviews++
	view := RenderSelfView(s.plan, s.scope)
	if view == "" {
		return r.out("Nothing is placed on the canvas yet, so there is no arrangement to review.")
	}
	return r.out(view)
}

func (s *staging) runFinish(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	summary := truncate(sanitizeBody(in.Summary), 600)

	// An answer that never reaches the board.
	//
	// Asked "what is missing from this plan?", a run named the gaps correctly
	// and staged nothing — the whole answer lived in the summary, which is run
	// panel text that disappears when the panel closes. A month later the board
	// cannot say what was missing, and the person got a paragraph they have to
	// copy somewhere by hand.
	//
	// Caught HERE rather than in the review turn, because the review turn
	// returns early on an empty plan: a run that stages nothing is never
	// reviewed, which is exactly the run that needs it.
	//
	// Narrow on purpose. It fires only when the run demonstrably HAS an answer
	// and put it in the wrong place — a substantive summary, a reporting
	// request, nothing staged. "Nothing is missing" is a short summary and
	// passes; a request the agent cannot carry out stages nothing legitimately
	// and is not a question.
	if !s.nudgedToLand && len(s.plan.Actions) == 0 && len(summary) >= 120 {
		if want := expectationOf(s.task.Intent); want.Reporting && !want.Impossible {
			s.nudgedToLand = true
			return r.fail("You have answered the question and left the answer nowhere. " +
				"That summary is run-panel text — it disappears when this panel closes, and " +
				"the board is left exactly as it was, unable to say what you found.\n\n" +
				"Put the answer where the work is: comment() on the thing it is about, or " +
				"create_note() on the board carrying it. Then call finish again.\n\n" +
				"If the honest answer really is that nothing is missing, say that in one " +
				"short line and finish — this only fires on an answer long enough to be worth keeping.")
		}
	}

	// Scope creep on a follow-up, refused ONCE, at the same gate as the
	// answer-that-never-landed. This began as review-turn prose and went one
	// for three against the live model — filling the named leftover correctly,
	// then adding columns nobody asked for anyway. A tool error is the one
	// channel the model reliably acts on: the reporting guard above holds live
	// through exactly this shape.
	if !s.nudgedOnCreep {
		if creep := followUpOverreach(s.task.Intent, s.scope, s.plan); creep != "" {
			s.nudgedOnCreep = true
			return r.fail("%s\n\nWithdraw them and call finish again — or, if the request itself "+
				"truly demands them, finish again with the reason in your summary.", creep)
		}
	}

	// Confidence, and the refusal that is the whole feature.
	//
	// A plan that says "I took a reading" and cannot say WHICH reading has told
	// the person nothing they can act on — it has moved the ambiguity from the
	// summary into a chip. The field exists to make the interpretation quotable,
	// so an unquoted one is rejected rather than stored, and the model is handed
	// the sentence shape it should have written. This is also what finally makes
	// the terse-intent nudge machine-checkable: it asks for a declared reading
	// and, until now, nothing anywhere verified it got one.
	if conf, reading, err := resolveConfidence(in.Confidence, in.Reading); err != nil {
		return r.fail("%s", err.Error())
	} else if conf != "" {
		s.plan.Confidence, s.plan.Reading = conf, reading
	}

	s.finished, s.everFinished = true, true
	s.plan.Summary = summary

	// Which standing rules this run says it followed, resolved against the ids
	// the digest actually rendered.
	//
	// Unknown ids are dropped SILENTLY. A self-report that can create a row is a
	// write primitive, and a model that cites "M9" on a board with three rules
	// would otherwise be inventing memory — the exact thing the tiering exists to
	// prevent. Dropping rather than refusing, because the signal wanted is "is
	// this rule ever relevant", and a run that mis-cites is not worth failing.
	// Same silence rule as `remember` below: the review turn is not asked which
	// rules it followed, so its saying nothing must not un-say the first answer.
	if shown, _ := s.scope.StandingRules(); len(shown) > 0 {
		if cited := ResolveMemoryRefs(shown, in.Applied); len(cited) > 0 {
			s.plan.AppliedMemoryIDs = cited
		}
	}
	// What the run did NOT do, in the run's own words. Without a field for
	// it the model buries the admission in the summary, where a person
	// scanning a change count never reads it — and a plan that quietly
	// covers four of five requests is indistinguishable from a bad agent.
	// Only when the person actually corrected this run. A rule proposed off the
	// back of a request nobody pushed back on is the agent inventing policy.
	//
	// A SECOND finish's silence is not a retraction. The loop forces a review
	// turn after every finish, and that turn is asked to look at the arrangement
	// — not asked for a rule, not asked what it left undone. It answered with a
	// bare summary, and this handler then overwrote both fields with empty:
	// `remember` and `unmet` were therefore blank on every run that took the
	// review turn, which is every run. Nothing noticed because neither field had
	// a single assertion over the whole harness, and the consequences ran
	// downstream of both — the rule card never rendered, so the one channel that
	// captures a convention was dead; and "LEFT UNDONE", which the digest's own
	// comment calls the most actionable sentence it can carry, was empty for
	// every continuation that inherited it.
	if len(s.task.Refinements) > 0 || s.steered {
		if rule := truncate(sanitizeBody(in.Remember), 200); rule != "" {
			s.plan.ProposedRule = rule
		}
	}
	var unmet []Unmet
	for _, u := range in.Unmet {
		request := truncate(sanitizeBody(u.Request), 200)
		if request == "" {
			continue
		}
		unmet = append(unmet, Unmet{
			Request: request, Why: truncate(sanitizeBody(u.Why), 200),
		})
		if len(unmet) == maxUnmetPerRun {
			break
		}
	}
	if len(unmet) > 0 {
		s.plan.Unmet = unmet
	}
	return r.out("Finished.")
}

// Confidence levels, in the order a reviewer's attention should follow.
const (
	// ConfidenceSure means the request said what to do and the plan did it.
	ConfidenceSure = "sure"
	// ConfidenceReading means the request had more than one sensible meaning and
	// the run picked one — which the person is entitled to see and reject.
	ConfidenceReading = "reading"
	// ConfidenceGuess means the run had to invent the subject, because the
	// request never named it. The terse-intent path's own outcome, finally
	// nameable.
	ConfidenceGuess = "guess"
)

// resolveConfidence validates finish's confidence pair.
//
// An empty confidence is tolerated rather than refused, because the review turn
// forces a SECOND finish and that turn is asked to look at the arrangement, not
// to restate its certainty — the same silence rule `remember` and `unmet` needed
// after a second finish silently blanked both. Silence keeps what the first
// answer said; only a stated confidence overwrites it.
func resolveConfidence(confidence, reading string) (string, string, error) {
	conf := strings.ToLower(strings.TrimSpace(confidence))
	quoted := truncate(sanitizeBody(strings.TrimSpace(reading)), 300)
	switch conf {
	case "":
		return "", "", nil
	case ConfidenceSure:
		// A reading offered alongside `sure` is kept: a run that says plainly
		// what it took the request to mean is doing the right thing, and
		// discarding it would punish the good behaviour.
		return ConfidenceSure, quoted, nil
	case ConfidenceReading, ConfidenceGuess:
		if quoted == "" {
			return "", "", fmt.Errorf(
				"you said confidence=%q, which means you interpreted rather than understood — "+
					"so say WHICH interpretation. `reading` must carry the sentence the person "+
					"would have written if they had been explicit, e.g. "+
					"\"taking this as: finish filling the columns the last run left empty\". "+
					"An unquoted \"I made an assumption\" tells them nothing they can say no to. "+
					"Call finish again with reading set — or with confidence=\"sure\" if the "+
					"request really was unambiguous.", conf)
		}
		return conf, quoted, nil
	default:
		return "", "", fmt.Errorf(
			"confidence must be one of sure, reading or guess — %q is not one of them", confidence)
	}
}

func (s *staging) runReadBoard(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	boardID := in.BoardID
	if boardID == "" {
		boardID = s.scope.Board.ID
	}
	// Containment: a board the agent may read is the run's root or
	// something inside it. This is the same boundary the write path
	// enforces, applied to reads so the agent cannot browse sideways.
	if boardID != s.scope.Board.ID {
		if _, ok := s.scope.Elements[boardID]; !ok {
			if _, staged := s.created[boardID]; !staged {
				s.rejectID(boardID)
				return r.fail("there is no board %s here", boardID)
			}
			return r.out("That board is part of this plan and is still empty.")
		}
	}
	digest, err := s.readBoard(ctx, boardID)
	if err != nil {
		return r.fail("could not read that board: %v", err)
	}
	return r.out(digest)
}

func (s *staging) runSearch(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return r.fail("give me something to search for")
	}
	hits, err := s.elements.Search(ctx, s.task.Owner, q, 12)
	if err != nil {
		return r.fail("search failed")
	}
	if len(hits) == 0 {
		return r.out(fmt.Sprintf("Nothing matches %q.", q))
	}
	var b strings.Builder
	// Search reaches the whole account; the plan reaches one subtree. The two
	// lines looked identical — same id, type, trust label and text as a digest
	// line the run may freely write to — so the model would cite a hit in a
	// summary, try to move it, and get "there is no element X on this board":
	// a refusal that is correct and reads as the agent contradicting itself
	// thirty seconds after finding the thing. The boundary was right; its
	// legibility was not. Same design as the ⟨trust⟩ label, applied to
	// AUTHORITY instead of provenance.
	fmt.Fprintf(&b, "%d matches for %q — this searches your whole account, "+
		"so some of these are outside what this run may change:\n", len(hits), q)
	for _, el := range hits {
		text, trust := textFor(el, s.scope)
		if el.Type == domain.TypeBoard {
			// Found by the run's OWN search, so filing may target it. A
			// board id sitting in card text never reaches here.
			s.markDiscovered(el.ID)
		}
		reach := "[elsewhere — readable, not writable]"
		if s.scope.Has(el.ID) || el.ID == s.scope.Board.ID {
			reach = "[on this board]"
		}
		fmt.Fprintf(&b, "%s · %s · ⟨%s⟩ · %s %s\n",
			el.ID, el.Type, trust, truncate(sanitizeText(text), 90), reach)
	}
	return r.out(b.String())
}

func (s *staging) runCreateBoard(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	kind := map[string]ActionKind{
		toolCreateBoard: ActCreateBoard, toolCreateColumn: ActCreateColumn,
		toolCreateNote: ActCreateNote, toolCreateTodo: ActCreateTodo,
		toolCreateLink: ActCreateLink,
	}[r.call.Name]

	// The kind is resolved first because the parent check depends on it: a
	// column may hold a note and may not hold another column, and "is it a
	// container" cannot tell those apart.
	parent, section, err := s.resolveParentFor(in.ParentID, kind.ElementType())
	if err != nil {
		return r.fail("%v", err)
	}
	if in.Section == string(domain.SectionUnsorted) && parent == s.scope.Board.ID {
		section = string(domain.SectionUnsorted)
	}

	// Already made, or already staged. Point the model at the one that exists
	// rather than letting a second copy through: it cannot delete the first,
	// and both would reach the board.
	if twin := s.duplicateSibling(parent, in.Title, kind); twin != "" {
		// An EMPTY twin is not a conflict — it is the thing the model was
		// reaching for. Refusing is how "complete" became eighteen new empty
		// columns beside eighteen empty ones: the model wanted somewhere to put
		// cards, a refusal gave it nowhere, so it built its own. Hand back the id
		// and the same run turns into eighteen fills, which is what the person
		// meant by the word.
		//
		// Success-shaped on purpose. A rejection, however politely worded, is
		// read as "that route is closed"; this is "the route is already open".
		//
		// Only for the two kinds whose emptiness is a fact about their children.
		// A to-do list carries its items in its own content, so "it has no
		// children" says nothing about whether it is empty, and add_tasks is the
		// verb for that one anyway.
		if (kind == ActCreateBoard || kind == ActCreateColumn) && s.containerIsEmpty(ctx, twin) {
			return r.out(fmt.Sprintf("%q already exists here and is empty — its id is %s. "+
				"Use that as the parentId and fill it. I did not create a second one.",
				sanitizeName(in.Title), twin))
		}
		return r.fail("there is already a %s called %q here, with things in it — it is %s. "+
			"Put what you were going to add in that one rather than making a second. "+
			"If the first was a mistake, drop it with undo_staged.",
			kind.ElementType(), sanitizeName(in.Title), twin)
	}

	a := Action{Kind: kind, ParentID: parent, Section: section}
	switch kind {
	case ActCreateBoard, ActCreateColumn:
		a.Title = sanitizeName(in.Title)
		if a.Title == "" {
			return r.fail("that needs a title")
		}
		// A label the header clips is a label nobody can read. Reject it
		// rather than truncate it: the model still holds the intent and can
		// coin a shorter name, whereas a silent trim ships "SCENE 3: THE
		// DATA CHI" to the user and calls it done.
		if budget := labelBudget(kind); len([]rune(a.Title)) > budget {
			return r.fail("%q is %d characters; the %s header shows about %d before it clips. "+
				"Give it a shorter name — put the detail in a card, not the label.",
				a.Title, len([]rune(a.Title)), a.Kind.ElementType(), budget)
		}
		a.Summary = a.Title
	case ActCreateNote:
		a.Text = sanitizeBody(in.Text)
		if a.Text == "" {
			return r.fail("that note has no text")
		}
		a.Summary = truncate(a.Text, 60)
	case ActCreateTodo:
		a.Title = sanitizeName(in.Title)
		for _, t := range in.Tasks {
			if clean := sanitizeName(t); clean != "" {
				a.Tasks = append(a.Tasks, clean)
			}
		}
		if a.Title == "" || len(a.Tasks) == 0 {
			return r.fail("a to-do list needs a title and at least one task")
		}
		a.Summary = fmt.Sprintf("%s (%d tasks)", a.Title, len(a.Tasks))
	case ActCreateLink:
		a.URL = strings.TrimSpace(in.URL)
		if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
			return r.fail("a link needs a full http(s) URL")
		}
		a.Title = sanitizeName(in.Title)
		// Read the page, on the same resolver and the same quota read_url uses.
		//
		// The card is rich or it is not; the compiler used to turn the preview
		// OFF, so an agent-made link was worse than one the person dropped
		// themselves. A failure here is not an error — it degrades to exactly
		// the bare link that used to be the only outcome.
		if meta, pageTitle := s.previewFor(ctx, a.URL); meta != nil {
			a.Preview = meta
			if a.Title == "" {
				a.Title = pageTitle
			}
		}
		if a.Title == "" {
			a.Title = a.URL
		}
		a.Summary = truncate(a.Title, 60)
	}

	id, err := s.add(a)
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged. Its id is %s — use that as parentId to put things inside it.", id))
}

func (s *staging) runMove(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	// Refuse HERE rather than letting the plan reach validation and fail as
	// a whole. Rejecting a finished plan tells the model nothing it can act
	// on; refusing the call tells it now, while it can still choose.
	if s.placedThisRun[el.ID] {
		return r.fail("you already positioned %s on the canvas this run. Filing it into a "+
			"container would undo that. Do one job: either compose the canvas or restructure it.", el.ID)
	}
	if el.Type == domain.TypeLine {
		return r.fail("connector lines follow the cards they join; they are not moved directly")
	}
	// A conversation belongs where the thing it is about is. Reading one is a
	// different question from relocating one, and only the first was ever the
	// argument for keeping threads away from the agent.
	if el.Type == domain.TypeCommentThread {
		return r.fail("%s is a conversation — it stays where the discussion happened. "+
			"Read it with read_comments and act on what it says", el.ID)
	}
	parent, section, err := s.resolveParentFor(in.ParentID, el.Type)
	if err != nil {
		return r.fail("%v", err)
	}
	// A heading names a REGION of the canvas. Filed into a column it stops being
	// a landmark and becomes a card with shouty text at the top of a list — and
	// the region it named loses its name. Checked against the RESOLVED parent,
	// so a heading aimed at a column the same plan is creating is refused too.
	if v, _ := el.Content["variant"].(string); v == headingVariant {
		if parentTypeOf(Action{ParentID: parent}, s.created, s.scope) != domain.TypeBoard {
			return r.fail("%s is a heading — it labels a region of the canvas, so it belongs "+
				"on a board and not inside a container. Move the cards under it instead", el.ID)
		}
	}
	if parent == el.ID {
		return r.fail("an element cannot be moved into itself")
	}
	if in.Section == string(domain.SectionUnsorted) && parent == s.scope.Board.ID {
		section = string(domain.SectionUnsorted)
	}
	text, _ := textFor(el, s.scope)
	if _, err := s.add(Action{
		Kind: ActMove, ElementID: el.ID, ParentID: parent, Section: section,
		Summary: truncate(sanitizeText(text), 60),
		Because: truncate(sanitizeName(in.Because), 80),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged.")
}

func (s *staging) runRename(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	title := sanitizeName(in.Title)
	if title == "" {
		return r.fail("that needs a title")
	}
	// The join key is not the agent's to throw away.
	//
	// The naming rule the model reads three paragraphs earlier — "name the
	// category, not the item: \"Data Chip\", not \"Scene 3: The Data Chip\"" —
	// is a live wrong aimed at exactly this trade, and it teaches the model to
	// do this. A scene number is LOCKED at the shooting script and never
	// renumbered; breakdown, stripboard, DOOD, call sheet, lined script, camera
	// and sound reports and editorial all match on it. Strip it and the cards
	// are unschedulable and unmatchable to the script, silently. A budget
	// account code is the same fact wearing different digits.
	//
	// Refused here rather than discouraged in the prompt, because a prompt
	// sentence loses to a strong aesthetic instruction — which is how the
	// aesthetic instruction came to exist.
	if id, dropped := dropsIdentifier(textOf(el), title); dropped {
		return r.fail("that rename drops %q off the front of %s. In this trade a leading "+
			"number is an IDENTIFIER, not a style choice — a scene number is locked at the "+
			"shooting script and the breakdown, the stripboard, the DOOD, the call sheet and "+
			"editorial all key off it; a budget account code works the same way. Rename it to "+
			"something that still starts with %q, or leave it alone and say why in your summary",
			id, el.ID, id)
	}
	if _, err := s.add(Action{
		Kind: ActRename, ElementID: el.ID, Title: title,
		Summary: title,
	}); err != nil {
		return r.fail("%v", err)
	}
	if note := s.warnCloneFanOut(el.ID, "renaming"); note != "" {
		return r.out("Staged. " + note)
	}
	return r.out("Staged.")
}

func (s *staging) runSetText(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeCard && el.Type != domain.TypeDocument {
		return r.fail("only notes and documents hold editable text")
	}
	text := sanitizeBody(in.Text)
	if text == "" {
		return r.fail("that would leave %s empty. This replaces the whole body, so send the "+
			"revised text in full rather than the part that changed", el.ID)
	}
	// Non-negotiable: you may not overwrite what you have not read.
	//
	// This action replaces the entire body, and the board listing shows an
	// opening fragment of it — so a run asked to "tighten the second half"
	// would send back a rewrite of the paragraph it had seen and silently
	// destroy the rest. The review list makes that invisible: it shows what
	// will exist, never what will stop existing.
	if !s.hasReadAllOf(el) {
		have := len([]rune(fullTextOf(el)))
		return r.fail("%s holds %d characters and this run has read %d of them. "+
			"set_note_text replaces the WHOLE body, so writing now would destroy the part you "+
			"have not seen. Call read_text on %s until you reach the end, then write it back in full",
			el.ID, have, s.readSoFar[el.ID], el.ID)
	}
	// Non-negotiable, part two: you may not overwrite formatting you cannot
	// re-express.
	//
	// The body is written back through the markdown subset, which carries
	// headings, both lists, quotes, code, bold, italic and links. Anything else a
	// person put in this document — underlining, highlighting, coloured text, a
	// table, an inline image — cannot survive the round trip, and this action
	// replaces the whole body. The review row says "edit note X" and the true
	// effect is "delete the formatting somebody spent an afternoon on". A lossy
	// round trip that silently succeeds is worse than not having the capability.
	if lost := InexpressibleFormatting(el.Content["doc"]); lost != "" {
		return r.fail("%s is formatted with %s, and I can only write back headings, lists, "+
			"quotes, code, bold, italic and links. set_note_text replaces the WHOLE body, so "+
			"rewriting it would strip that formatting out — work somebody did by hand, with "+
			"nothing in the review list to show it had gone.\n\n"+
			"Leave it as it is and say so, or put the new material in a NEW note beside it "+
			"and say which is which.", el.ID, lost)
	}
	if _, err := s.add(Action{
		Kind: ActSetText, ElementID: el.ID, Text: text,
		Summary: truncate(text, 60),
	}); err != nil {
		return r.fail("%v", err)
	}
	if note := s.warnCloneFanOut(el.ID, "rewriting"); note != "" {
		return r.out("Staged. " + note)
	}
	return r.out("Staged.")
}

// warnCloneFanOut records the true blast radius of editing a synced card, and
// returns the sentence the model is told about it.
//
// The review list shows one row per action, and an edit to a card with live
// instances changes it on every board holding one — because the write path
// re-broadcasts the update op and the edit lands at the source. That is the only
// place in the system where an approved change has an effect the review cannot
// describe, which is precisely the property the staged-preview architecture
// exists to guarantee. The note rides on the plan so the outcome card surfaces
// it whether or not the model chose to mention it in its summary.
func (s *staging) warnCloneFanOut(id, verb string) string {
	sites := s.scope.CloneSites[id]
	if len(sites) == 0 {
		return ""
	}
	note := fmt.Sprintf("%s %s also changes it on %s — it is one synced card shown in several places.",
		verb, id, strings.Join(sites, ", "))
	for _, existing := range s.plan.Notes {
		if existing == note {
			return note
		}
	}
	s.plan.Notes = append(s.plan.Notes, note)
	return note
}

func (s *staging) runDelete(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	text, _ := textFor(el, s.scope)
	if _, err := s.add(Action{
		Kind: ActDelete, ElementID: el.ID,
		Summary: truncate(sanitizeText(text), 60),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged. The user must approve this before anything is trashed.")
}

// runUnstage removes a staged element and everything staged inside it.
//
// The harness could only ever ADD. A model that staged a structure, looked at
// it and decided on a different cut had no way to withdraw the first — so both
// reached the plan, and a five-stage documentary arrived as thirteen columns.
// Being able to revise is not a nicety; without it, thinking out loud is
// destructive.
func (s *staging) runUnstage(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	target := in.ElementID
	if _, staged := s.created[target]; !staged {
		return r.fail("%s is not something you staged in this run — undo_staged only "+
			"removes your own staged changes, never anything already on the board", target)
	}

	// Everything the plan put inside it goes too: a card in a column that is no
	// longer being created has nowhere to be.
	doomed := map[string]bool{target: true}
	for changed := true; changed; {
		changed = false
		for i := range s.plan.Actions {
			a := &s.plan.Actions[i]
			if a.ParentID != "" && doomed[a.ParentID] && !doomed[a.ElementID] {
				doomed[a.ElementID] = true
				changed = true
			}
		}
	}

	kept := make([]Action, 0, len(s.plan.Actions))
	removed := 0
	for _, a := range s.plan.Actions {
		// A connector to or from something withdrawn goes with it.
		if doomed[a.ElementID] || doomed[a.FromID] || doomed[a.ToID] {
			removed++
			delete(s.created, a.ElementID)
			continue
		}
		// Sequence is positional and Preconditions checks it; ids stay bound to
		// the original so a retried apply is still idempotent.
		a.Seq = len(kept)
		kept = append(kept, a)
	}
	s.plan.Actions = kept

	// Quotas are not refunded. They bound how much a run may ATTEMPT, and a
	// model that could stage and withdraw freely would have no ceiling at all.
	return r.out(fmt.Sprintf("Removed %d staged change(s). %d remain.", removed, len(kept)))
}

// runPlaceFile puts a file attached to the REQUEST onto the board.
//
// The agent could read an attachment and had no way to place one. Asked "put
// this image in the first scene" it replied that it could not add an image
// "without its content or a URL" — true, and reading as broken, because the
// file was uploaded, in scope, and already read.
//
// Only files attached to THIS request. An arbitrary attachment id is somebody
// else's upload, and resolving one would let a run reach a file its person
// never offered it.
func (s *staging) runPlaceFile(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	id := strings.TrimSpace(in.AttachmentID)
	if !containsStr(s.task.AttachmentIDs, id) {
		return r.fail("%q is not attached to this request. %s", id, s.attachedList())
	}
	if s.files == nil {
		return r.fail("attachments cannot be placed here")
	}
	att, err := s.files.Get(ctx, id)
	if err != nil || att == nil {
		return r.fail("that attachment could not be read")
	}
	if att.PublicURL == "" {
		return r.fail("%s has not finished uploading yet", att.Filename)
	}
	// The tool's own description promises "an image card if it is one, a file
	// card otherwise", and the type used to be derived from the ActionKind — a
	// pure function that never sees a mime type. Every attachment compiled as an
	// IMAGE, so an attached PDF became <img src={pdf}>: a broken picture with no
	// filename and no download, and unreadable to the next run, which was told
	// it was looking at a picture.
	kindType := domain.TypeFile
	if strings.HasPrefix(strings.ToLower(att.ContentType), "image/") {
		kindType = domain.TypeImage
	}
	parent, section, err := s.resolveParentFor(in.ParentID, kindType)
	if err != nil {
		return r.fail("%v", err)
	}
	caption := truncate(sanitizeName(in.Title), 120)
	if caption == "" {
		caption = sanitizeName(att.Filename)
	}
	noun := "File"
	if kindType == domain.TypeImage {
		noun = "Image"
	}
	newID, err := s.add(Action{
		Kind: ActPlaceFile, ParentID: parent, Section: section,
		ElementType: kindType,
		// The indirection, not att.PublicURL — the same thing the human upload
		// path writes. A presigned URL is a bearer credential with a seven-day
		// life; stored in content it outlives every permission change and then
		// the picture disappears from the board on day eight. This route signs
		// per request behind an ACL check.
		URL: attachmentBlobPath(att.ID), AssigneeID: att.ID, Title: caption,
		MimeType: att.ContentType, Size: att.Size,
		Summary: noun + ": " + truncate(caption, 48),
	})
	if err != nil {
		return r.fail("%v", err)
	}
	return r.out("Staged " + newID + " — " + caption + " will be placed on the board.")
}

// attachmentBlobPath is where an element points at an uploaded file. Same-origin
// and relative, matching what uploadFile writes on the client, so an
// agent-placed file and a dropped one are the same row.
func attachmentBlobPath(id string) string {
	return "/api/v1/attachments/" + id + "/blob"
}

// attachedList names what IS attached, so a wrong id is a correction rather
// than a dead end.
func (s *staging) attachedList() string {
	if len(s.task.AttachmentIDs) == 0 {
		return "Nothing is attached to this request."
	}
	return "Attached to this request: " + strings.Join(s.task.AttachmentIDs, ", ")
}

// runAddTasks appends items to a to-do list already on the board.
//
// The agent could create a checklist and never add to one, so "add three more
// items to that list" had no route but a SECOND list beside the first.
func (s *staging) runAddTasks(ctx context.Context, in *toolArgs, r *reply) cognition.ToolOutcome {
	el, err := s.resolveExisting(in.ElementID)
	if err != nil {
		return r.fail("%v", err)
	}
	if el.Type != domain.TypeTaskList {
		return r.fail("%s is a %s, not a to-do list", el.ID, el.Type)
	}
	var items []string
	for _, t := range in.Tasks {
		if clean := sanitizeName(t); clean != "" {
			items = append(items, truncate(clean, 200))
		}
	}
	if len(items) == 0 {
		return r.fail("that needs at least one item")
	}
	// After what the list already holds, so adding does not reorder it.
	next := lastIndexIn(el.ID, s.scope) + 1
	if _, err := s.add(Action{
		Kind: ActAddTasks, ElementID: el.ID, Tasks: items, Index: next,
		Summary: fmt.Sprintf("%d item(s) → %s", len(items), truncate(sanitizeText(textOf(el)), 32)),
	}); err != nil {
		return r.fail("%v", err)
	}
	return r.out(fmt.Sprintf("Staged %d item(s) onto that list.", len(items)))
}
