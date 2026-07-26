package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// The capability plane: the typed operations the model may ask for.
//
// The split that makes a general agent safe here is READ-LIVE / WRITE-STAGED.
// Read tools run immediately — they are pure, so letting the model look around
// mid-run costs nothing and is what allows it to build on what it just made.
// Write tools are STAGED into a plan and executed later, all at once, as one
// transaction. That preserves every property the narrow version had — preview
// writes nothing, one Ctrl+Z, one revert — while removing the ceiling on what
// the agent can propose.
//
// A staged create returns its real element id to the model, which is what lets
// the next call parent to it.

const (
	toolReadBoard    = "read_board"
	toolSearch       = "search"
	toolCreateBoard  = "create_board"
	toolCreateColumn = "create_column"
	toolCreateNote   = "create_note"
	toolCreateTodo   = "create_todo"
	toolCreateLink   = "create_link"
	toolMove         = "move_element"
	toolRename       = "rename"
	toolSetText      = "set_note_text"
	toolDelete       = "delete_element"
	toolApplyLabel   = "apply_label"
	toolCreateLabel  = "create_label"
	toolSetColor     = "set_color"
	toolSetTask      = "set_task_done"
	toolTree         = "board_tree"
	toolLook         = "look_at"
	toolClone        = "clone_here"
	toolComment      = "comment"
	toolReadURL      = "read_url"
	toolAssign       = "set_assignee"
	toolRemind       = "set_reminder"
	toolResize       = "resize"
	toolHeading      = "create_heading"
	toolArrange      = "arrange"
	toolTidy         = "tidy_board"
	toolMerge        = "merge_notes"
	toolSplit        = "split_note"
	toolConnect      = "connect"
	toolCreateTable  = "create_table"
	toolHistory      = "recent_changes"
	toolAsk          = "ask"
	toolPreview      = "preview_layout"
	toolFinish       = "finish"
)

// maxNewLabelsPerRun stops "tag everything" from spraying a taxonomy nobody
// asked for. Reuse is nearly always the better answer.
const maxNewLabelsPerRun = 4

// maxConnectionsPerRun keeps a relationship map readable. Roughly one line per
// two elements is the point past which a diagram stops explaining anything.
const maxConnectionsPerRun = 12

// maxURLsPerRun bounds outbound fetches. Each one is a request this server makes
// on the user's behalf to somewhere it does not control.
const maxURLsPerRun = 5

// maxToolOutputBytes bounds one observation. An unbounded read would let board
// content crowd out the instructions.
const maxToolOutputBytes = 6000

// cardSwatches mirrors NOTE_COLORS in the frontend. The model picks a NAME and
// the server resolves the hex: "#fff9db" means nothing to a language model, and
// letting it choose free hex would put colours outside the product's palette on
// the board.
var cardSwatches = map[string]string{
	"default": "",
	"yellow":  "#fff9db",
	"red":     "#ffe8e8",
	"green":   "#e6fcf0",
	"blue":    "#e7f5ff",
	"purple":  "#f3f0ff",
	"orange":  "#fff4e6",
	"pink":    "#f8f0fc",
	"dark":    "#2b3035",
}

func swatchNames() []string {
	out := make([]string, 0, len(cardSwatches))
	for name := range cardSwatches {
		out = append(out, name)
	}
	sort.Strings(out) // stable order keeps the prompt cacheable
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": required, "properties": props}
}

// ToolCatalogue is the set offered to the model for a run. Deletes appear only
// when the run is allowed to make them, so an unattended run cannot even see
// the capability, let alone reach it.
func ToolCatalogue(allowDelete, allowLabels bool) []cognition.ToolDef {
	tools := []cognition.ToolDef{
		{
			Name: toolReadBoard,
			Description: "Look inside a board to see what is already there. Call with no id for the board " +
				"you are working on, or with the id of a board you created or found.",
			Schema: obj(nil, map[string]any{"boardId": str("Board to inspect. Omit for the current board.")}),
		},
		{
			Name: toolPreview,
			Description: "See how what you have staged so far will actually be arranged on the canvas — " +
				"widths, rows, and how many items land in each container. Call this before finish " +
				"and fix anything that reads badly.",
			Schema: obj(nil, map[string]any{}),
		},
		{
			Name:        toolSearch,
			Description: "Search the user's own boards and cards by text. Use before creating something that may already exist.",
			Schema:      obj([]string{"query"}, map[string]any{"query": str("Words to look for.")}),
		},
		{
			Name:        toolCreateBoard,
			Description: "Create a nested board to hold related material. Boards are for whole topics; use a column for a list within a board.",
			Schema: obj([]string{"parentId", "title"}, map[string]any{
				"parentId": str("Board to create it inside."),
				"title":    str("Board name, 24 characters or fewer — the tile clips longer ones."),
			}),
		},
		{
			Name:        toolCreateColumn,
			Description: "Create a column: a vertical list on a board's canvas that holds cards.",
			Schema: obj([]string{"parentId", "title"}, map[string]any{
				"parentId": str("Board to create it on."),
				// Word counts do not survive contact with a fixed-width header:
				// "Scene 3: The Data Chip" is four words and still clips.
				// Budget in characters, which is what actually fits.
				"title": str("Column name, 20 characters or fewer — headers render uppercase and clip beyond that. " +
					"Name the category, not the item: \"Data Chip\", not \"Scene 3: The Data Chip\"."),
			}),
		},
		{
			Name:        toolCreateNote,
			Description: "Create a note card with text. This is the ordinary way to record something.",
			Schema: obj([]string{"parentId", "text"}, map[string]any{
				"parentId": str("Board or column to create it in."),
				"text":     str("The note's content. Plain text; newlines make paragraphs."),
				"section":  map[string]any{"type": "string", "enum": []string{"CANVAS", "UNSORTED"}, "description": "Where on the board. Defaults to the canvas."},
			}),
		},
		{
			Name:        toolCreateTodo,
			Description: "Create a to-do list with its items.",
			Schema: obj([]string{"parentId", "title", "tasks"}, map[string]any{
				"parentId": str("Board or column to create it in."),
				"title":    str("List name."),
				"tasks":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "One line per task."},
			}),
		},
		{
			Name:        toolCreateLink,
			Description: "Create a link card pointing at a URL the user supplied. Do not invent URLs.",
			Schema: obj([]string{"parentId", "url"}, map[string]any{
				"parentId": str("Board or column to create it in."),
				"url":      str("Full http(s) URL."),
				"title":    str("Label for the link."),
			}),
		},
		{
			Name: toolMove,
			Description: "Move an existing element into a board or column. " +
				"Cards land in the ORDER YOU STAGE THEM, so stage them in the order they should be read — " +
				"chronological, by priority, by sequence, whatever the material calls for.",
			Schema: obj([]string{"elementId", "parentId"}, map[string]any{
				"elementId": str("Element to move."),
				"parentId":  str("Destination board or column."),
				"section":   map[string]any{"type": "string", "enum": []string{"CANVAS", "UNSORTED"}, "description": "Defaults to the canvas."},
				"because":   str("Optional. One short clause on why this belongs there — shown to the user when they review the plan."),
			}),
		},
		{
			Name:        toolRename,
			Description: "Rename an existing board, column or list.",
			Schema: obj([]string{"elementId", "title"}, map[string]any{
				"elementId": str("Element to rename."), "title": str("New name."),
			}),
		},
		{
			Name:        toolSetText,
			Description: "Replace the text of an existing note. Use only when the user asked for the content to change.",
			Schema: obj([]string{"elementId", "text"}, map[string]any{
				"elementId": str("Note to edit."), "text": str("New content."),
			}),
		},
		{
			Name: toolAsk,
			Description: "Ask ONE clarifying question, and only before you have staged anything. " +
				"Use it when the request genuinely has two sensible readings and guessing wrong " +
				"would waste the whole run. Prefer attempting and being corrected: a question " +
				"costs the person a round trip, so it must earn it.",
			Schema: obj([]string{"question"}, map[string]any{
				"question": str("One short question."),
				"options": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Two or three concrete answers to offer.",
				},
			}),
		},
		{
			Name: toolFinish,
			Description: "Finish. Call this when the work is planned, or immediately if nothing should be done. " +
				"Say plainly what you are proposing and anything you deliberately left alone.",
			Schema: obj([]string{"summary"}, map[string]any{
				"summary": str("One or two sentences for the user."),
			}),
		},
	}
	if allowLabels {
		tools = append(tools,
			cognition.ToolDef{
				Name: toolApplyLabel,
				Description: "Tag an element with one of the user's existing labels. Labels cut ACROSS structure: " +
					"use one when items belong together but should stay where they are. Prefer this to moving " +
					"things when the user asks you to mark, flag or highlight.",
				Schema: obj([]string{"elementId", "labelId"}, map[string]any{
					"elementId": str("Element to tag."),
					"labelId":   str("A label id from the LABELS list you were shown. Never invent one."),
				}),
			},
			cognition.ToolDef{
				Name: toolCreateLabel,
				Description: "Create a new label, only when no existing one fits. Reuse before you coin: " +
					"two labels meaning the same thing is worse than one imperfect one.",
				Schema: obj([]string{"name"}, map[string]any{
					"name": str("Short label name, 24 characters or fewer."),
				}),
			},
		)
	}
	tools = append(tools,
		cognition.ToolDef{
			Name: toolConnect,
			Description: "Draw a labelled arrow between two elements to show a relationship — " +
				"depends on, causes, contradicts, comes after. Use sparingly: a few meaningful " +
				"arrows read as insight, many read as a hairball and are worse than none.",
			Schema: obj([]string{"fromId", "toId"}, map[string]any{
				"fromId": str("Element the arrow starts at."),
				"toId":   str("Element it points to."),
				"label":  str("Optional short word for the relationship."),
			}),
		},
		cognition.ToolDef{
			Name: toolComment,
			Description: "Leave one short note on the board explaining a decision that is not obvious " +
				"from the result. At most one per run, and only where the reasoning genuinely helps: " +
				"an assistant that annotates everything teaches people to ignore annotations.",
			Schema: obj([]string{"text"}, map[string]any{
				"text": str("What you want the reader to know, in a sentence or two."),
			}),
		},
		cognition.ToolDef{
			Name: toolClone,
			Description: "Show an existing card in a second place. The two stay in sync, so this is " +
				"right when something genuinely belongs in two contexts — copying instead means the " +
				"two drift apart the first time either is edited.",
			Schema: obj([]string{"sourceId", "parentId"}, map[string]any{
				"sourceId": str("Card to show elsewhere."),
				"parentId": str("Board or column to show it in."),
			}),
		},
		cognition.ToolDef{
			Name: toolCreateTable,
			Description: "Create a table. This is the right answer whenever items share repeating " +
				"attributes — a comparison, a cast list, a budget. A sequence of thoughts wants cards; " +
				"repeating attributes want a grid.",
			Schema: obj([]string{"parentId", "rows"}, map[string]any{
				"parentId": str("Board or column to create it in."),
				"title":    str("Optional table name."),
				"rows": map[string]any{
					"type":        "array",
					"description": "First row is the header. Every row must have the same number of cells.",
					"items": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
					},
				},
			}),
		},
		cognition.ToolDef{
			Name: toolHistory,
			Description: "See what changed on this board recently, newest first. Use for questions " +
				"about time — what moved this week, what has gone stale, what was decided.",
			Schema: obj(nil, map[string]any{}),
		},
		cognition.ToolDef{
			Name: toolSetColor,
			Description: "Set a card's colour. A second grouping axis that survives re-filing. " +
				"If you colour anything, say in your summary what the colours mean.",
			Schema: obj([]string{"elementId", "color"}, map[string]any{
				"elementId": str("Card to colour."),
				"color": map[string]any{
					"type": "string", "description": "One of the app's swatches.",
					"enum": swatchNames(),
				},
			}),
		},
		cognition.ToolDef{
			Name:        toolSetTask,
			Description: "Tick or untick a task in a to-do list. Only when the user clearly asked for it — marking someone's work done is a claim about the world.",
			Schema: obj([]string{"elementId", "done"}, map[string]any{
				"elementId": str("Task to change."),
				"done":      map[string]any{"type": "boolean", "description": "True to tick, false to untick."},
			}),
		},
	)
	if allowDelete {
		tools = append(tools, cognition.ToolDef{
			Name: toolDelete,
			Description: "Move an element to the trash. Only when the user clearly asked for removal. " +
				"NOT for duplicates: trashing both copies of something loses the content. " +
				"Use merge_notes, which writes the combined card first and then trashes the originals.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("Element to trash."),
			}),
		})
	}
	return tools
}

// staging accumulates the plan across the loop and enforces every rule the
// model does not get to decide.
type staging struct {
	runID    string
	scope    *BoardScope
	task     TaskSpec
	elements domain.ElementRepository
	labels   domain.LabelRepository
	txns     domain.TransactionRepository
	emit     emitFunc

	plan *Plan
	// created maps a staged element id to its kind, so a later action can be
	// checked for whether it is parenting to something that can hold children.
	created map[string]ActionKind
	// outOfScope counts ids the model named that it was never shown — the
	// clearest signal of a successful injection.
	outOfScope int
	finished   bool
	// reviewed records that the model has seen its own arrangement. The loop
	// forces one look before accepting finish, so this is set either way.
	reviewed  bool
	newLabels int
	// failedCalls counts identical failing calls, so a model looping on the
	// same mistake can be told plainly rather than allowed to spend the whole
	// budget rediscovering it.
	failedCalls map[string]int
	// asked and question hold the one clarifying question a run may pose.
	images        ImageFetcher
	imagesSeen    int
	pendingImages []cognition.ImagePart
	// placedThisRun / movedThisRun catch a plan arguing with itself while it is
	// still being built.
	placedThisRun map[string]bool
	movedThisRun  map[string]bool
	links         LinkResolver
	urlsRead      int
	connections   int
	commented     bool
	asked         bool
	question      *Question
	// everFinished stays true once the model has signalled it is done, even
	// though the review turn un-sets `finished` to buy one more step. Without
	// it, a run that completed and then reviewed would be reported as "may be
	// incomplete" — the false warning this loop was fixed for once already.
	everFinished bool
}

func newStaging(runID string, scope *BoardScope, task TaskSpec, elements domain.ElementRepository, labels domain.LabelRepository, txns domain.TransactionRepository, images ImageFetcher, links LinkResolver, emit emitFunc) *staging {
	return &staging{
		runID: runID, scope: scope, task: task, elements: elements, labels: labels, txns: txns, images: images, links: links, emit: emit,
		plan: &Plan{}, created: map[string]ActionKind{}, failedCalls: map[string]int{},
	}
}

// resolveParent validates a proposed parent and reports the section a child of
// it should land in.
func (s *staging) resolveParent(id string) (string, string, error) {
	if id == "" || id == s.scope.Board.ID {
		return s.scope.Board.ID, string(domain.SectionCanvas), nil
	}
	if kind, ok := s.created[id]; ok {
		if !kind.Container() {
			return "", "", fmt.Errorf("%s is a %s and cannot hold other items", id, kind.ElementType())
		}
		return id, string(domain.SectionCanvas), nil
	}
	el, ok := s.scope.Elements[id]
	if !ok {
		s.outOfScope++
		return "", "", fmt.Errorf("there is no element %s on this board", id)
	}
	if !el.Type.IsContainer() {
		return "", "", fmt.Errorf("%s is a %s and cannot hold other items", id, el.Type)
	}
	return id, string(domain.SectionCanvas), nil
}

// resolveExisting validates that an id refers to an element the run may touch.
func (s *staging) resolveExisting(id string) (*domain.Element, error) {
	el, ok := s.scope.Elements[id]
	if !ok {
		s.outOfScope++
		return nil, fmt.Errorf("there is no element %s on this board", id)
	}
	if isHomeBoard(el) {
		return nil, fmt.Errorf("the Home board cannot be changed")
	}
	return el, nil
}

// add appends a staged action, enforcing the plan's size budget.
func (s *staging) add(a Action) (string, error) {
	if len(s.plan.Actions) >= s.task.Budget.MaxActions {
		return "", fmt.Errorf("this plan already has the maximum of %d changes; call finish", s.task.Budget.MaxActions)
	}
	a.Seq = len(s.plan.Actions)
	if a.Kind.Creates() {
		a.ElementID = ActionID(s.runID, a.Seq)
		s.created[a.ElementID] = a.Kind
	}
	s.plan.Actions = append(s.plan.Actions, a)
	// Announce it immediately. Watching the plan appear line by line is both
	// better company than a spinner and more honest: the user sees the shape of
	// the change forming, and can stop it before it is ever offered.
	s.emit(EvActionStaged, a.Summary, map[string]any{
		"seq": a.Seq, "kind": string(a.Kind), "elementId": a.ElementID,
		"parentId": a.ParentID, "destructive": a.Kind.Destructive(),
	})
	return a.ElementID, nil
}

// Execute runs one tool call: reads answer immediately, writes stage.
func (s *staging) Execute(ctx context.Context, call cognition.ToolCall) cognition.ToolOutcome {
	out := func(msg string) cognition.ToolOutcome {
		return cognition.ToolOutcome{CallID: call.ID, Name: call.Name, Content: truncate(msg, maxToolOutputBytes)}
	}
	fail := func(format string, args ...any) cognition.ToolOutcome {
		msg := fmt.Sprintf(format, args...)
		// A model that gets the same rejection twice will usually try a third
		// time, identically, until the step budget runs out — the user waits
		// and pays for a run that stopped progressing several turns ago.
		// Escalating the wording is what breaks the loop: the same fact, said
		// in a way that makes repeating the call visibly not an option.
		key := call.Name + "|" + msg
		s.failedCalls[key]++
		switch n := s.failedCalls[key]; {
		case n == 2:
			msg += " — this is the second identical failure. Do not retry it; " +
				"take a different approach or leave this part alone and say so."
		case n > 2:
			msg = "STOP repeating this call. It has failed " + fmt.Sprint(n) +
				" times with: " + msg + ". Finish with what you have and explain what you could not do."
		}
		return cognition.ToolOutcome{CallID: call.ID, Name: call.Name, IsError: true,
			Content: truncate(msg, 400)}
	}

	var in struct {
		BoardID    string     `json:"boardId"`
		Query      string     `json:"query"`
		ParentID   string     `json:"parentId"`
		ElementID  string     `json:"elementId"`
		Title      string     `json:"title"`
		Text       string     `json:"text"`
		URL        string     `json:"url"`
		Section    string     `json:"section"`
		Summary    string     `json:"summary"`
		Tasks      []string   `json:"tasks"`
		LabelID    string     `json:"labelId"`
		Name       string     `json:"name"`
		Color      string     `json:"color"`
		Done       bool       `json:"done"`
		Because    string     `json:"because"`
		Question   string     `json:"question"`
		Options    []string   `json:"options"`
		SourceID   string     `json:"sourceId"`
		FromID     string     `json:"fromId"`
		ToID       string     `json:"toId"`
		Rows       [][]string `json:"rows"`
		ElementIDs []string   `json:"elementIds"`
		Texts      []string   `json:"texts"`
		Layout     string     `json:"layout"`
		UserID     string     `json:"userId"`
		When       string     `json:"when"`
		Size       string     `json:"size"`
	}
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return fail("could not read those arguments: %v", err)
		}
	}

	switch call.Name {

	case toolApplyLabel:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		label, err := s.resolveLabel(ctx, in.LabelID)
		if err != nil {
			return fail("%v", err)
		}
		if containsStr(el.LabelIDs, label.ID) {
			return out(fmt.Sprintf("%s already carries %q.", el.ID, label.Name))
		}
		s.add(Action{
			Kind: ActApplyLabel, ElementID: el.ID, LabelID: label.ID,
			Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 40), label.Name),
		})
		return out(fmt.Sprintf("Staged: tag %s with %q.", el.ID, label.Name))

	case toolCreateLabel:
		if s.labels == nil {
			return fail("labels are not available here")
		}
		name := sanitizeName(in.Name)
		if name == "" || len([]rune(name)) > 24 {
			return fail("a label needs a name of 24 characters or fewer")
		}
		// Reuse before coining: two labels meaning the same thing is a worse
		// outcome than one imperfect one, and the model cannot see that from
		// its own turn alone.
		existing, err := s.ownedLabels(ctx)
		if err != nil {
			return fail("could not read labels")
		}
		for _, l := range existing {
			if strings.EqualFold(l.Name, name) {
				return out(fmt.Sprintf("%q already exists as %s — use that.", l.Name, l.ID))
			}
		}
		if s.newLabels >= maxNewLabelsPerRun {
			return fail("that is enough new labels for one run (%d); reuse one of the existing ones", maxNewLabelsPerRun)
		}
		l := &domain.Label{ID: s.nextLabelID(), OwnerID: s.task.Owner, Name: name, Color: "#5e5ce6"}
		s.plan.NewLabels = append(s.plan.NewLabels, l)
		s.newLabels++
		return out(fmt.Sprintf("Created label %q as %s. Use that id with apply_label.", l.Name, l.ID))

	case toolSetColor:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		hex, ok := cardSwatches[strings.ToLower(strings.TrimSpace(in.Color))]
		if !ok {
			return fail("%q is not one of the app's swatches (%s)", in.Color, strings.Join(swatchNames(), ", "))
		}
		s.add(Action{
			Kind: ActSetColor, ElementID: el.ID, Color: hex,
			Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 40), in.Color),
		})
		return out(fmt.Sprintf("Staged: colour %s %s.", el.ID, in.Color))

	case toolSetTask:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		if el.Type != domain.TypeTask {
			return fail("%s is a %s, not a task", el.ID, el.Type)
		}
		verb := "tick"
		if !in.Done {
			verb = "untick"
		}
		s.add(Action{
			Kind: ActSetTask, ElementID: el.ID, Done: in.Done,
			Summary: fmt.Sprintf("%s %s", verb, truncate(sanitizeText(textOf(el)), 40)),
		})
		return out(fmt.Sprintf("Staged: %s %s.", verb, el.ID))

	case toolAsk:
		// Only before anything is staged, and only once. An agent that can ask
		// twice will ask forever, and the bias must stay toward attempting.
		if len(s.plan.Actions) > 0 {
			return fail("you have already staged changes; finish the plan and let the person adjust it")
		}
		if s.asked {
			return fail("you have already asked; make your best attempt and say what you assumed")
		}
		q := sanitizeName(in.Question)
		if q == "" {
			return fail("that needs a question")
		}
		s.asked = true
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
		return out("Asked. The run will pause for an answer.")

	case toolLook:
		if s.images == nil {
			return fail("images cannot be viewed on this deployment")
		}
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		if el.Type != domain.TypeImage {
			return fail("%s is a %s, not an image", el.ID, el.Type)
		}
		if s.imagesSeen >= maxImagesPerRun {
			return fail("that is enough images for one run (%d); work from what you have seen", maxImagesPerRun)
		}
		attachmentID, _ := el.Content["attachmentId"].(string)
		if attachmentID == "" {
			return fail("that image has no file attached yet")
		}
		data, mediaType, err := s.images.Fetch(ctx, attachmentID)
		if err != nil {
			return fail("%v", err)
		}
		s.imagesSeen++
		// The bytes ride on the NEXT user turn rather than in this outcome:
		// a tool result is text, and an image is not.
		s.pendingImages = append(s.pendingImages, cognition.ImagePart{MediaType: mediaType, Data: data})
		return out(fmt.Sprintf("Attached %s — it is included with this turn, so describe what you see and use it.",
			truncate(sanitizeName(contentStr(el.Content, "filename")), 60)))

	case toolReadURL:
		if s.links == nil {
			return fail("link previews are not available here")
		}
		raw := strings.TrimSpace(in.URL)
		if raw == "" {
			return fail("that needs a URL")
		}
		if s.urlsRead >= maxURLsPerRun {
			return fail("that is enough pages for one run (%d)", maxURLsPerRun)
		}
		meta, err := s.links.Resolve(ctx, raw)
		if err != nil {
			return fail("could not read that page")
		}
		s.urlsRead++
		// Whatever comes back is ⟨web⟩: a page title is content someone else
		// wrote, and it is labelled as such wherever it lands.
		return out(fmt.Sprintf("⟨web⟩ %s — %s",
			truncate(sanitizeText(meta.Title), 120), truncate(sanitizeText(meta.Description), 240)))

	case toolTree:
		tree, elided, err := s.renderTree(ctx, s.scope.Board.ID, 0)
		if err != nil {
			return fail("could not read the tree")
		}
		if tree == "" {
			return out("This board has no nested boards.")
		}
		if elided > 0 {
			// Silent truncation is how an agent confidently concludes that
			// something does not exist. Say what was left out.
			tree += fmt.Sprintf("(%d board(s) not expanded — this is as deep as the outline goes)", elided)
		}
		return out(tree)

	case toolAssign:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		if el.Type != domain.TypeTask {
			return fail("%s is a %s; only tasks carry an owner", el.ID, el.Type)
		}
		var person *PersonRef
		for i := range s.scope.People {
			if s.scope.People[i].ID == in.UserID {
				person = &s.scope.People[i]
			}
		}
		if person == nil {
			// Same treatment as a foreign element id: assigning work to somebody
			// without access to the board is not a thing to do quietly.
			s.outOfScope++
			return fail("%s is not one of this board's people", in.UserID)
		}
		s.add(Action{
			Kind: ActSetAssignee, ElementID: el.ID, AssigneeID: person.ID,
			Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(el)), 34), person.Name),
		})
		return out("Staged.")

	case toolRemind:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		when, perr := time.Parse(time.RFC3339, strings.TrimSpace(in.When))
		if perr != nil {
			return fail("%q is not an RFC3339 timestamp (e.g. 2026-09-01T09:00:00Z)", in.When)
		}
		s.add(Action{
			Kind: ActSetReminder, ElementID: el.ID, RemindAt: when.UTC().Format(time.RFC3339),
			Summary: fmt.Sprintf("%s · %s", truncate(sanitizeText(textOf(el)), 34), when.UTC().Format("2 Jan")),
		})
		return out("Staged.")

	case toolResize:
		width, ok := map[string]float64{"small": 220, "medium": 320, "large": 460}[strings.ToLower(in.Size)]
		if !ok {
			return fail("size must be small, medium or large")
		}
		staged := 0
		for _, id := range in.ElementIDs {
			el, err := s.resolveExisting(id)
			if err != nil {
				return fail("%v", err)
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
			return fail("everything is already that size")
		}
		return out(fmt.Sprintf("Staged: %d resized.", staged))

	case toolHeading:
		text := sanitizeName(in.Text)
		if text == "" {
			return fail("a heading needs text")
		}
		id, err := s.add(Action{
			Kind: ActCreateHeading, ParentID: s.scope.Board.ID,
			Section: string(domain.SectionCanvas),
			Text:    truncate(text, 60), Summary: truncate(text, 60),
		})
		if err != nil {
			return fail("%v", err)
		}
		return out("Staged heading " + id + ".")

	case toolArrange:
		boxes, err := ComputeArrangement(in.ElementIDs, Layout(in.Layout), s.scope)
		if err != nil {
			return fail("%v", err)
		}
		if err := s.stagePlacements(in.ElementIDs, boxes); err != nil {
			return fail("%v", err)
		}
		return out(fmt.Sprintf("Staged: %d element(s) arranged as a %s.", len(boxes), in.Layout))

	case toolTidy:
		loose := s.looseOnCanvas()
		if len(loose) == 0 {
			return out("Nothing is loose on the canvas — everything is already inside a column or board.")
		}
		boxes, err := ComputeArrangement(loose, LayoutTidy, s.scope)
		if err != nil {
			return fail("%v", err)
		}
		if err := s.stagePlacements(loose, boxes); err != nil {
			return fail("%v", err)
		}
		return out(fmt.Sprintf("Staged: tidied %d element(s), keeping each roughly where it was.", len(boxes)))

	case toolMerge:
		if len(in.ElementIDs) < 2 {
			return fail("merging needs at least two cards")
		}
		text := sanitizeBody(in.Text)
		if text == "" {
			return fail("the merged card needs text")
		}
		// Resolve every id BEFORE staging anything. A merge that creates the
		// replacement and then fails on the third id would leave the board with
		// a duplicate of its own content.
		var parent string
		els := make([]*domain.Element, 0, len(in.ElementIDs))
		for _, id := range in.ElementIDs {
			el, err := s.resolveExisting(id)
			if err != nil {
				return fail("%v", err)
			}
			if el.Type != domain.TypeCard {
				return fail("%s is a %s; only cards can be merged", el.ID, el.Type)
			}
			if parent == "" {
				parent = el.Location.ParentID
			}
			els = append(els, el)
		}
		if !s.canDelete() {
			return fail("merging trashes the originals, which this run is not allowed to do")
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
			return fail("%v", err)
		}
		for _, el := range els {
			if _, err := s.add(Action{
				Kind: ActDelete, ElementID: el.ID,
				Summary: truncate(sanitizeText(textOf(el)), 60),
			}); err != nil {
				return fail("%v", err)
			}
		}
		return out(fmt.Sprintf("Staged: %d cards merged into %s, originals to trash.", len(els), id))

	case toolSplit:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		if el.Type != domain.TypeCard {
			return fail("%s is a %s; only cards can be split", el.ID, el.Type)
		}
		parts := make([]string, 0, len(in.Texts))
		for _, t := range in.Texts {
			if clean := sanitizeBody(t); clean != "" {
				parts = append(parts, truncate(clean, 4000))
			}
		}
		if len(parts) < 2 {
			return fail("splitting needs at least two resulting cards")
		}
		if !s.canDelete() {
			return fail("splitting trashes the original, which this run is not allowed to do")
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
				return fail("%v", err)
			}
		}
		if _, err := s.add(Action{
			Kind: ActDelete, ElementID: el.ID,
			Summary: truncate(sanitizeText(textOf(el)), 60),
		}); err != nil {
			return fail("%v", err)
		}
		return out(fmt.Sprintf("Staged: split into %d cards, original to trash.", len(parts)))

	case toolComment:
		if s.commented {
			return fail("you have already left a note; put anything else in your summary")
		}
		text := sanitizeBody(in.Text)
		if text == "" {
			return fail("that comment has no text")
		}
		id, err := s.add(Action{
			Kind: ActComment, ParentID: s.scope.Board.ID, Section: string(domain.SectionCanvas),
			Text: truncate(text, 500), Summary: truncate(text, 60),
		})
		if err != nil {
			return fail("%v", err)
		}
		s.commented = true
		// The thread element is staged; its body is written at apply time, so a
		// discarded preview leaves no orphan thread.
		s.plan.NewComments = append(s.plan.NewComments, &domain.Comment{
			ThreadID: id, AuthorID: s.task.Owner, Body: truncate(text, 500),
		})
		return out("Staged a note on the board.")

	case toolClone:
		src, err := s.resolveExisting(in.SourceID)
		if err != nil {
			return fail("%v", err)
		}
		parent, section, err := s.resolveParent(in.ParentID)
		if err != nil {
			return fail("%v", err)
		}
		if src.Type == domain.TypeClone || src.Type == domain.TypeBoard {
			return fail("%s is a %s; only ordinary cards can be shown in two places", src.ID, src.Type)
		}
		id, err := s.add(Action{
			Kind: ActCloneHere, ParentID: parent, Section: section, FromID: src.ID,
			Summary: truncate(sanitizeText(textOf(src)), 60),
		})
		if err != nil {
			return fail("%v", err)
		}
		return out("Staged clone " + id + ".")

	case toolConnect:
		from, err := s.resolveExisting(in.FromID)
		if err != nil {
			return fail("%v", err)
		}
		to, err := s.resolveExisting(in.ToID)
		if err != nil {
			return fail("%v", err)
		}
		if from.ID == to.ID {
			return fail("an element cannot be connected to itself")
		}
		// Asked to "connect related ideas", an unbounded model draws N-squared
		// edges and produces a hairball strictly worse than no lines at all.
		if s.connections >= maxConnectionsPerRun {
			return fail("that is enough connections for one run (%d) — keep only the ones that carry meaning", maxConnectionsPerRun)
		}
		s.connections++
		id, err := s.add(Action{
			Kind: ActConnect, ParentID: s.scope.Board.ID,
			FromID: from.ID, ToID: to.ID, Title: truncate(sanitizeName(in.Title), 40),
			Summary: fmt.Sprintf("%s → %s", truncate(sanitizeText(textOf(from)), 24), truncate(sanitizeText(textOf(to)), 24)),
		})
		if err != nil {
			return fail("%v", err)
		}
		return out("Staged connection " + id + ".")

	case toolCreateTable:
		parent, section, err := s.resolveParent(in.ParentID)
		if err != nil {
			return fail("%v", err)
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
			return fail("a table needs a header row and at least one data row")
		}
		width := len(rows[0])
		for i, r := range rows {
			if len(r) != width {
				return fail("row %d has %d cells but the header has %d — every row must match", i, len(r), width)
			}
		}
		id, err := s.add(Action{
			Kind: ActCreateTable, ParentID: parent, Section: section,
			Title: truncate(sanitizeName(in.Title), 40), Rows: rows,
			Summary: fmt.Sprintf("%s (%d×%d)", firstNonEmpty(sanitizeName(in.Title), "Table"), len(rows)-1, width),
		})
		if err != nil {
			return fail("%v", err)
		}
		return out("Staged table " + id + ".")

	case toolHistory:
		if s.txns == nil {
			return fail("history is not available here")
		}
		list, err := s.txns.ListByBoard(ctx, s.scope.Board.ID, 20)
		if err != nil {
			return fail("could not read the history")
		}
		if len(list) == 0 {
			return out("Nothing has changed on this board yet.")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d recent change(s), newest first:\n", len(list))
		for _, t := range list {
			fmt.Fprintf(&b, "%s · %s · %s\n", humanAge(t.CreatedAt), originOf(t), opSummary(t))
		}
		return out(b.String())

	case toolPreview:
		s.reviewed = true
		view := RenderSelfView(s.plan, s.scope)
		if view == "" {
			return out("Nothing is placed on the canvas yet, so there is no arrangement to review.")
		}
		return out(view)

	case toolFinish:
		s.finished, s.everFinished = true, true
		s.plan.Summary = truncate(sanitizeBody(in.Summary), 600)
		return out("Finished.")

	case toolReadBoard:
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
					s.outOfScope++
					return fail("there is no board %s here", boardID)
				}
				return out("That board is part of this plan and is still empty.")
			}
		}
		digest, err := s.readBoard(ctx, boardID)
		if err != nil {
			return fail("could not read that board: %v", err)
		}
		return out(digest)

	case toolSearch:
		q := strings.TrimSpace(in.Query)
		if q == "" {
			return fail("give me something to search for")
		}
		hits, err := s.elements.Search(ctx, s.task.Owner, q, 12)
		if err != nil {
			return fail("search failed")
		}
		if len(hits) == 0 {
			return out(fmt.Sprintf("Nothing matches %q.", q))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d matches for %q:\n", len(hits), q)
		for _, el := range hits {
			text, trust := textFor(el)
			fmt.Fprintf(&b, "%s · %s · ⟨%s⟩ · %s\n", el.ID, el.Type, trust, truncate(sanitizeText(text), 90))
		}
		return out(b.String())

	case toolCreateBoard, toolCreateColumn, toolCreateNote, toolCreateTodo, toolCreateLink:
		parent, section, err := s.resolveParent(in.ParentID)
		if err != nil {
			return fail("%v", err)
		}
		if in.Section == string(domain.SectionUnsorted) && parent == s.scope.Board.ID {
			section = string(domain.SectionUnsorted)
		}
		kind := map[string]ActionKind{
			toolCreateBoard: ActCreateBoard, toolCreateColumn: ActCreateColumn,
			toolCreateNote: ActCreateNote, toolCreateTodo: ActCreateTodo,
			toolCreateLink: ActCreateLink,
		}[call.Name]

		a := Action{Kind: kind, ParentID: parent, Section: section}
		switch kind {
		case ActCreateBoard, ActCreateColumn:
			a.Title = sanitizeName(in.Title)
			if a.Title == "" {
				return fail("that needs a title")
			}
			// A label the header clips is a label nobody can read. Reject it
			// rather than truncate it: the model still holds the intent and can
			// coin a shorter name, whereas a silent trim ships "SCENE 3: THE
			// DATA CHI" to the user and calls it done.
			if budget := labelBudget(kind); len([]rune(a.Title)) > budget {
				return fail("%q is %d characters; the %s header shows about %d before it clips. "+
					"Give it a shorter name — put the detail in a card, not the label.",
					a.Title, len([]rune(a.Title)), a.Kind.ElementType(), budget)
			}
			a.Summary = a.Title
		case ActCreateNote:
			a.Text = sanitizeBody(in.Text)
			if a.Text == "" {
				return fail("that note has no text")
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
				return fail("a to-do list needs a title and at least one task")
			}
			a.Summary = fmt.Sprintf("%s (%d tasks)", a.Title, len(a.Tasks))
		case ActCreateLink:
			a.URL = strings.TrimSpace(in.URL)
			if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
				return fail("a link needs a full http(s) URL")
			}
			a.Title = sanitizeName(in.Title)
			if a.Title == "" {
				a.Title = a.URL
			}
			a.Summary = truncate(a.Title, 60)
		}

		id, err := s.add(a)
		if err != nil {
			return fail("%v", err)
		}
		return out(fmt.Sprintf("Staged. Its id is %s — use that as parentId to put things inside it.", id))

	case toolMove:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		// Refuse HERE rather than letting the plan reach validation and fail as
		// a whole. Rejecting a finished plan tells the model nothing it can act
		// on; refusing the call tells it now, while it can still choose.
		if s.placedThisRun[el.ID] {
			return fail("you already positioned %s on the canvas this run. Filing it into a "+
				"container would undo that. Do one job: either compose the canvas or restructure it.", el.ID)
		}
		if el.Type == domain.TypeLine {
			return fail("connector lines follow the cards they join; they are not moved directly")
		}
		parent, section, err := s.resolveParent(in.ParentID)
		if err != nil {
			return fail("%v", err)
		}
		if parent == el.ID {
			return fail("an element cannot be moved into itself")
		}
		if in.Section == string(domain.SectionUnsorted) && parent == s.scope.Board.ID {
			section = string(domain.SectionUnsorted)
		}
		text, _ := textFor(el)
		if _, err := s.add(Action{
			Kind: ActMove, ElementID: el.ID, ParentID: parent, Section: section,
			Summary: truncate(sanitizeText(text), 60),
			Because: truncate(sanitizeName(in.Because), 80),
		}); err != nil {
			return fail("%v", err)
		}
		return out("Staged.")

	case toolRename:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		title := sanitizeName(in.Title)
		if title == "" {
			return fail("that needs a title")
		}
		if _, err := s.add(Action{
			Kind: ActRename, ElementID: el.ID, Title: title,
			Summary: title,
		}); err != nil {
			return fail("%v", err)
		}
		return out("Staged.")

	case toolSetText:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		if el.Type != domain.TypeCard && el.Type != domain.TypeDocument {
			return fail("only notes and documents hold editable text")
		}
		text := sanitizeBody(in.Text)
		if text == "" {
			return fail("that would leave the note empty")
		}
		if _, err := s.add(Action{
			Kind: ActSetText, ElementID: el.ID, Text: text,
			Summary: truncate(text, 60),
		}); err != nil {
			return fail("%v", err)
		}
		return out("Staged.")

	case toolDelete:
		el, err := s.resolveExisting(in.ElementID)
		if err != nil {
			return fail("%v", err)
		}
		text, _ := textFor(el)
		if _, err := s.add(Action{
			Kind: ActDelete, ElementID: el.ID,
			Summary: truncate(sanitizeText(text), 60),
		}); err != nil {
			return fail("%v", err)
		}
		return out("Staged. The user must approve this before anything is trashed.")

	default:
		return fail("there is no tool called %q", call.Name)
	}
}

// readBoard renders a board the agent asked to see, and folds what it found
// into the scope so subsequent actions may legitimately reference it.
func (s *staging) readBoard(ctx context.Context, boardID string) (string, error) {
	board, err := s.elements.Get(ctx, boardID)
	if err != nil {
		return "", err
	}
	children, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: boardID})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	title, _ := board.Content["title"].(string)
	fmt.Fprintf(&b, "BOARD %s %q — %d items\n", board.ID, title, len(children))
	live := 0
	for _, el := range children {
		if el.IsDeleted() || el.Type == domain.TypeLine {
			continue
		}
		live++
		s.scope.Elements[el.ID] = el
		text, trust := textFor(el)
		fmt.Fprintf(&b, "%s · %s · ⟨%s⟩ · %s", el.ID, el.Type, trust, truncate(sanitizeText(text), 100))
		if el.Location.Section == domain.SectionUnsorted {
			b.WriteString("  [unsorted]")
		}
		b.WriteString("\n")
	}
	if live == 0 {
		b.WriteString("(empty)\n")
	}
	return b.String(), nil
}

// sanitizeBody cleans model-authored prose that becomes card content. Newlines
// survive because they carry meaning in a note; control characters do not.
func sanitizeBody(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, sanitizeText(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ownedLabels lists the run owner's labels. Scoped to the owner for the same
// reason element ids are scoped to the board: a shared board must not become a
// way to enumerate somebody else's taxonomy.
func (s *staging) ownedLabels(ctx context.Context) ([]*domain.Label, error) {
	if s.labels == nil {
		return nil, nil
	}
	return s.labels.ListByOwner(ctx, s.task.Owner)
}

// resolveLabel accepts only ids the run owner actually holds, including ones
// coined earlier in this same run.
func (s *staging) resolveLabel(ctx context.Context, id string) (*domain.Label, error) {
	if s.labels == nil {
		return nil, fmt.Errorf("labels are not available here")
	}
	for _, l := range s.plan.NewLabels {
		if l.ID == id {
			return l, nil
		}
	}
	owned, err := s.ownedLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not read labels")
	}
	for _, l := range owned {
		if l.ID == id {
			return l, nil
		}
	}
	// Same treatment as an out-of-scope element id: refused and counted, because
	// a label id the model was never shown can only have come from content.
	s.outOfScope++
	return nil, fmt.Errorf("%s is not one of your labels", id)
}

func (s *staging) nextLabelID() string {
	return ActionID(s.runID+":label", len(s.plan.NewLabels))
}

func containsStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// textOf is the human handle for an element in a summary line.
func textOf(el *domain.Element) string {
	if t, ok := el.Content["textPreview"].(string); ok && t != "" {
		return t
	}
	if t, ok := el.Content["title"].(string); ok && t != "" {
		return t
	}
	return el.ID
}

// humanAge renders a timestamp the way a person would say it. Relative, because
// "3 days ago" is what a question about staleness actually means; absolute
// timestamps would make the model do date arithmetic it is bad at.
func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.UTC().Format("2006-01-02")
}

// originOf labels who made a change. Transactions written before the agent
// existed carry no origin at all, and those were all human.
func originOf(t *domain.Transaction) string {
	if t.Origin == "" {
		return "human"
	}
	return t.Origin
}

// opSummary describes a transaction by what it did, since transactions store
// operations rather than a sentence.
func opSummary(t *domain.Transaction) string {
	counts := map[domain.Action]int{}
	order := make([]domain.Action, 0, 4)
	for _, op := range t.Ops {
		if counts[op.Action] == 0 {
			order = append(order, op.Action)
		}
		counts[op.Action]++
	}
	parts := make([]string, 0, len(order))
	for _, a := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[a], a))
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// maxTreeDepth bounds the outline. Structure is cheap in tokens and content is
// not, so the tree goes wide and shallow: names and counts only.
const maxTreeDepth = 3

// renderTree outlines the boards nested under one board. It reports how many
// were left unexpanded so the model never mistakes the edge of the outline for
// the edge of the workspace.
func (s *staging) renderTree(ctx context.Context, boardID string, depth int) (string, int, error) {
	if depth >= maxTreeDepth {
		return "", 1, nil
	}
	kids, err := s.elements.Children(ctx, domain.ElementFilter{
		ParentID: boardID, Types: []domain.ElementType{domain.TypeBoard},
	})
	if err != nil {
		return "", 0, err
	}
	var b strings.Builder
	elided := 0
	for _, k := range kids {
		if k.IsDeleted() {
			continue
		}
		all, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: k.ID})
		if err != nil {
			return "", 0, err
		}
		live := 0
		for _, c := range all {
			if !c.IsDeleted() {
				live++
			}
		}
		fmt.Fprintf(&b, "%s%s · %s · %d item(s)\n",
			strings.Repeat("  ", depth+1), k.ID, truncate(sanitizeName(contentStr(k.Content, "title")), 40), live)
		sub, subElided, err := s.renderTree(ctx, k.ID, depth+1)
		if err != nil {
			return "", 0, err
		}
		b.WriteString(sub)
		elided += subElided
	}
	return b.String(), elided, nil
}

// canDelete reports whether this run may trash anything. Merge and split both
// destroy the originals, so they are only offered where a person will see the
// plan before it commits — the same rule the delete tool follows.
func (s *staging) canDelete() bool { return s.task.Autonomy == AutonomyPreview }

// stagePlacements turns computed geometry into one ActPlace per element,
// skipping anything already exactly where it should be. A no-op move is still a
// row in the review list and an op in the transaction, and a "tidy" that
// reports forty changes when it moved three is a plan nobody can check.
func (s *staging) stagePlacements(ids []string, boxes map[string]ColumnBox) error {
	if s.placedThisRun == nil {
		s.placedThisRun = map[string]bool{}
	}
	staged := 0
	for _, id := range ids {
		box, ok := boxes[id]
		if !ok {
			continue
		}
		if el, ok := s.scope.Elements[id]; ok && el != nil {
			if el.Location.Position.X == box.X && el.Location.Position.Y == box.Y {
				continue
			}
		}
		if s.movedThisRun[id] {
			// The mirror of the check in toolMove: composing something the run
			// has already re-filed is the same contradiction from the other end.
			return fmt.Errorf("%s was already filed into a container this run; positioning it "+
				"on the canvas would undo that", id)
		}
		s.placedThisRun[id] = true
		pos := box
		if _, err := s.add(Action{
			Kind: ActPlace, ElementID: id, Position: &pos,
			Summary: truncate(sanitizeText(textOf(s.scope.Elements[id])), 60),
		}); err != nil {
			return err
		}
		staged++
	}
	if staged == 0 {
		return fmt.Errorf("everything is already positioned that way")
	}
	return nil
}

// looseOnCanvas is everything sitting directly on the board's canvas — the set
// a tidy applies to. Things inside a column are ordered by that column and have
// no position of their own.
func (s *staging) looseOnCanvas() []string {
	var out []string
	for _, it := range s.scope.Items {
		el, ok := s.scope.Elements[it.ID]
		if !ok || el == nil {
			continue
		}
		if el.Location.ParentID != s.scope.Board.ID {
			continue
		}
		if el.Location.Section == domain.SectionUnsorted || el.Type == domain.TypeLine {
			continue
		}
		out = append(out, el.ID)
	}
	sort.Strings(out) // deterministic, then reordered into reading order by the packer
	return out
}

// LinkResolver fetches page metadata. An interface so the agent depends on
// "can I find out what this page is" rather than on the HTTP client that does
// it — and so a test answers without touching the network.
type LinkResolver interface {
	Resolve(ctx context.Context, rawURL string) (*service.LinkMetadata, error)
}
