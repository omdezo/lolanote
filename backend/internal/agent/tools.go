package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

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
	toolFileTo       = "file_to_board"
	toolLook         = "look_at"
	toolClone        = "clone_here"
	toolComment      = "comment"
	toolReadComments = "read_comments"
	toolReadTable    = "read_table"
	toolReadText     = "read_text"
	toolReadURL      = "read_url"
	toolAssign       = "set_assignee"
	toolRemind       = "set_reminder"
	toolResize       = "resize"
	toolHeading      = "create_heading"
	toolArrange      = "arrange"
	toolReorder      = "reorder"
	toolTidy         = "tidy_board"
	toolMerge        = "merge_notes"
	toolMergeColumns = "merge_columns"
	toolSplit        = "split_note"
	toolConnect      = "connect"
	toolCreateTable  = "create_table"
	toolHistory      = "recent_changes"
	toolStyleBoard   = "style_board"
	toolListTrash    = "list_trash"
	toolRestore      = "restore"
	toolAsk          = "ask"
	toolPreview      = "preview_layout"
	toolShape        = "design_as"
	toolUnstage      = "undo_staged"
	toolAlternative  = "propose_alternative"
	toolPlaceFile    = "place_attachment"
	toolAddTasks     = "add_tasks"
	toolDocument     = "write_document"
	toolColor        = "add_color"
	toolShortcut     = "link_board"
	toolEditTable    = "edit_table"
	toolSetURL       = "set_url"
	toolCaption      = "set_caption"
	toolDirection    = "set_direction"
	toolResolve      = "resolve_thread"
	toolCollapse     = "collapse_column"
	toolDuplicate    = "duplicate"
	toolConvert      = "convert"
	toolFinish       = "finish"
	// The four production reads. All of them are READS: they change nothing,
	// they state facts the server computed or looked up, and the model decides
	// what to stage from them. That is deliberate — a domain capability that
	// wrote to the board would be a second planner with none of the review, and
	// every one of these is either arithmetic (which the model gets wrong) or
	// a cited external rule (which the model would otherwise assert in its own
	// voice).
	toolFilmSpec = "film_spec"
	toolRegroup  = "regroup"
	toolBackward = "schedule_backward"
	toolCheck    = "check_constraints"
)

// maxNewLabelsPerRun stops "tag everything" from spraying a taxonomy nobody
// asked for. Reuse is nearly always the better answer.
const maxNewLabelsPerRun = 4

// maxConnectionsPerRun keeps a relationship map readable. Roughly one line per
// two elements is the point past which a diagram stops explaining anything.
const maxConnectionsPerRun = 12

// maxUnmetPerRun bounds the "did not do" list. Past a handful it stops being a
// disclosure and becomes a wall of text nobody reads — which is the same
// failure as saying nothing.
const maxUnmetPerRun = 5

// maxURLsPerRun bounds outbound fetches. Each one is a request this server makes
// on the user's behalf to somewhere it does not control.
const maxURLsPerRun = 5

// maxRefusalBytes bounds a rejection. Larger than it was: a refusal now carries
// the correction — the ids the run actually has — and at 400 bytes that list
// was cut off mid-way, so the model was told it was wrong and not told what
// would be right. It went on guessing, which is exactly what the correction
// exists to stop.
const maxRefusalBytes = 900

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
				"text": str("The note's content. Newlines make paragraphs, and the small markdown " +
					"subset works here too: - bullets, 1. numbers, **bold**, *italic*, `code`, " +
					"[text](url). A sticky rarely needs headings."),
				"section": map[string]any{"type": "string", "enum": []string{"CANVAS", "UNSORTED"}, "description": "Where on the board. Defaults to the canvas."},
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
			Name: toolSetText,
			Description: "Rewrite the body of an existing note OR document. Use only when the user asked for " +
				"the content to change. A document keeps its title and its id, so revising one leaves " +
				"every arrow and comment pointing at it intact — never write a second document beside the first.",
			Schema: obj([]string{"elementId", "text"}, map[string]any{
				"elementId": str("Note or document to edit."),
				"text": str("The whole new body. This replaces what is there rather than appending, " +
					"so read it first if you mean to keep any of it. The same markdown subset " +
					"write_document accepts works here: # headings, - bullets, 1. numbers, " +
					"> quotes, **bold**, *italic*, `code`, [text](url). " +
					"A note carrying formatting outside that subset — underlining, highlighting, " +
					"coloured text, a table — is REFUSED rather than flattened."),
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
				"Say plainly what you are proposing. If any part of the request is not in this plan — " +
				"you could not do it, you ran out of room, or you judged it wrong — list it in `unmet`. " +
				"An unmentioned omission reads to the user as a failure with no explanation.",
			Schema: obj([]string{"summary", "confidence"}, map[string]any{
				"summary": str("One or two sentences for the user."),
				"confidence": map[string]any{
					"type": "string", "enum": []string{"sure", "reading", "guess"},
					"description": "How certain you are that this is what they meant. " +
						"sure = the request said what to do and you did it. " +
						"reading = the request had more than one sensible meaning and you picked one. " +
						"guess = you had to invent the subject, because the request did not name it. " +
						"Anything but `sure` REQUIRES `reading`.",
				},
				"reading": str("Required unless confidence is `sure`. The interpretation you " +
					"acted on, quoted so the person can see it and say no — " +
					"\"taking this as: finish filling the columns the last run left empty\". " +
					"Not a hedge, not an apology: the sentence they would have written."),
				"applied": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Ids of the STANDING RULES you actually followed, e.g. [\"M1\",\"M3\"]. " +
						"Only ones you were shown. This is how a rule nobody needs any more gets " +
						"retired instead of sitting in the list forever.",
				},
				"remember": str("Only if this run was CORRECTED: one sentence stating the " +
					"convention you got wrong, phrased as a standing rule for this board " +
					"(\"Columns are pipeline stages — never add one\"). The user approves it " +
					"before it is saved. Leave empty otherwise."),
				"unmet": map[string]any{
					"type":        "array",
					"description": "Parts of the request this plan does not carry out. Leave empty if you did everything asked.",
					"items": obj([]string{"request"}, map[string]any{
						"request": str("The part of the request, in the user's terms."),
						"why":     str("Why not, in one short clause."),
					}),
				},
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
					// A colour, because in some vocabularies the colour IS the
					// meaning. A film breakdown is DEFINED by it — props violet,
					// extras green, stunts orange, effects blue — and twenty
					// identical grey chips express none of it. The model picks a
					// name and the server resolves the hex, exactly as it does
					// for a card's paper colour: free hex would put colours
					// outside the product's palette on the board.
					"color": map[string]any{
						"type": "string", "enum": swatchNames(),
						"description": "Optional chip colour. Only where the colour carries " +
							"meaning the name does not — a category system read at a glance. " +
							"Leave it out for an ordinary tag.",
					},
				}),
			},
		)
	}
	tools = append(tools,
		cognition.ToolDef{
			Name: toolFilmSpec,
			Description: "Get the real structure of a film production document before you build " +
				"it: its canonical columns, its required fields, the board shape it takes, and " +
				"the thing a practitioner knows about it that a generic answer misses. These " +
				"artefacts have shapes the trade decided and a filmmaker rejects a malformed one " +
				"on sight — a shot list with no lens or movement column, a call sheet as a " +
				"single note. Call it FIRST, then build what it describes.",
			Schema: obj([]string{"artefact"}, map[string]any{
				"artefact": map[string]any{
					"type": "string", "enum": artefactKeys(),
					"description": "Which document. Call with an unknown key to get the list back.",
				},
				// Only the delivery list varies this way, and it varies enormously:
				// handing a festival the streamer's list produces forty items
				// nobody asked for and omits the SMPTE-conformant DCP subtitle
				// track that stops the screening.
				"destination": map[string]any{
					"type": "string", "enum": deliveryDestinationKeys(),
					"description": "For deliverables only: where the finished film is going. " +
						"The list differs a great deal by destination — if you do not know " +
						"which it is, ASK rather than choosing one.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolRegroup,
			Description: "Group scene-shaped cards into SHOOTING order — by location, day/night " +
				"or set — and get back the grouping with the company moves it saves. Shooting " +
				"order is not story order: a schedule is solved, not sorted, and arranging " +
				"scenes along their numbers gives you back the script. This reads only; stage " +
				"the moves yourself from what it tells you.",
			Schema: obj([]string{"by"}, map[string]any{
				"elementIds": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Cards to group. Omit for every scene-shaped card in view.",
				},
				"by": map[string]any{
					"type": "string", "enum": regroupAxes,
					"description": "The axis a unit actually schedules on.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolBackward,
			Description: "Work backwards from a fixed date. Give the anchor — the shoot day, the " +
				"delivery date, the festival deadline — and the things that have to be ready " +
				"before it, and the server returns the date each one must start by, and says " +
				"which are ALREADY LATE. It knows the lead times for permits and visas, so you " +
				"can leave leadDays out for those. Never do this arithmetic yourself.",
			Schema: obj([]string{"anchor", "steps"}, map[string]any{
				"anchor": str("The immovable date, as YYYY-MM-DD."),
				"steps": map[string]any{
					"type": "array",
					"items": obj([]string{"name"}, map[string]any{
						"name":     str("What has to be ready."),
						"leadDays": map[string]any{"type": "integer", "description": "How many days it takes. Omit if it is a permit or a visa — the server knows."},
					}),
					"description": "The things that must happen before the anchor date.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolCheck,
			Description: "Check a plan against the constraints it has to satisfy, and get back " +
				"violations as facts with their source. It reads dates, call times, sluglines " +
				"and page counts off the cards and reports what breaks: the Omani midday " +
				"outdoor-work ban, Ramadan hours, the daylight window for an EXT/DAY scene, and " +
				"the page load per day. It also reads DATE RANGES — \"Layla available " +
				"2026-08-03 → 2026-08-14\", a permit's validity, a location's window — and says " +
				"how long each period is, how much of it falls in Ramadan or the summer ban, " +
				"what is dated outside a stated window, and where a wrap and the next " +
				"morning's call leave less than eleven hours of turnaround. A schedule is a " +
				"solution, not a list — this is how you find the violation the person missed. " +
				"It states weather as MISSING rather than guessing it.",
			Schema: obj(nil, map[string]any{
				"elementIds": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Restrict to these cards. Omit to check everything in view.",
				},
				"days": map[string]any{"type": "integer", "description": "How many shooting days the material is spread over, if you know. It turns a page total into a pages-per-day figure."},
			}),
		},
		cognition.ToolDef{
			Name: toolConnect,
			Description: "Draw a labelled arrow between two elements to show a relationship — " +
				"depends on, causes, contradicts, comes after. Use sparingly: a few meaningful " +
				"arrows read as insight, many read as a hairball and are worse than none.",
			Schema: obj([]string{"fromId", "toId"}, map[string]any{
				"fromId": str("Element the arrow starts at."),
				"toId":   str("Element it points to."),
				"label":  str("Optional short word for the relationship."),
				"relation": map[string]any{
					"type": "string",
					"enum": relationEnum(),
					"description": "What kind of relationship this is. It decides how the line " +
						"is drawn — a blocker reads red, a loose association reads as a soft " +
						"dashed line — so a diagram can be understood without reading every label.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolComment,
			Description: "Leave a short note explaining a decision that is not obvious from the " +
				"result. Give elementId to put it beside the thing it concerns; without one it " +
				"lands on the board itself, where nobody connects it to anything. Only where the " +
				"reasoning genuinely helps: an assistant that annotates everything teaches people " +
				"to ignore annotations.",
			Schema: obj([]string{"text"}, map[string]any{
				"text":      str("What you want the reader to know, in a sentence or two."),
				"elementId": str("Optional. The element this note is about — it lands in the same place as that element."),
				"mentions": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Optional. People to flag this to, as the handles from PEOPLE " +
						"(person1, person2). Each one is notified. Use it when the note needs a " +
						"specific person to see it, not on every note.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolReadTable,
			Description: "Read a page of a table's rows. The board listing shows the header and the " +
				"first rows of each table and states its true size; call this when the request is " +
				"ABOUT a table longer than what you were shown — a shot list, a schedule, a budget. " +
				"Never answer a question about a long table from the rows in the listing.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The table to read."),
				"fromRow":   map[string]any{"type": "integer", "description": "First data row to return, counting the header as row 0. Defaults to 1."},
				"count":     map[string]any{"type": "integer", "description": "How many rows to return. Defaults to 25."},
			}),
		},
		cognition.ToolDef{
			Name: toolReadText,
			Description: "Read the full text of a note or document, a page at a time. The board " +
				"listing shows an opening fragment and says how much more there is; a long " +
				"document is only its first paragraph until you call this. Read before you " +
				"rewrite: replacing text you have not read destroys what you never saw.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The note or document to read."),
				"offset":    map[string]any{"type": "integer", "description": "Character to start at. Defaults to 0."},
				"limit":     map[string]any{"type": "integer", "description": "How many characters to return. Defaults to 2000."},
			}),
		},
		cognition.ToolDef{
			Name: toolReadComments,
			Description: "Read a conversation on this board. The board's artifacts say what was " +
				"made; the comments say what was decided and what is still argued about, so read " +
				"them before summarising, before reorganising something people are discussing, and " +
				"whenever the request is about what anybody said or wanted.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The 💬 conversation to read."),
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
			Description: "See what changed across this board AND everything nested inside it, " +
				"newest first, by whom. Use for questions about time — what moved this week, " +
				"what has gone stale, what was decided while somebody was away.",
			Schema: obj(nil, map[string]any{
				"when": str("How far back to look: a date as YYYY-MM-DD, or one of " +
					"today / yesterday / week / month. Leave it out for everything recorded."),
				"limit": map[string]any{"type": "integer",
					"description": "Most rows to return, up to 60."},
			}),
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
	// Reading, composition and repair. Every one of these has an execute case;
	// a tool that exists in the switch and not in this catalogue is a
	// capability nobody can reach, which is indistinguishable from not having
	// built it. ToolCatalogueCovers, exercised by a test, keeps the two in step.
	tools = append(tools,
		cognition.ToolDef{
			Name: toolTree,
			Description: "See the shape of everything nested under this board — child boards by name " +
				"and how much each holds, without their contents. Use before filing things across " +
				"boards, so you place them somewhere that already exists.",
			Schema: obj(nil, map[string]any{}),
		},
		cognition.ToolDef{
			Name: toolLook,
			Description: "Read an image or a PDF on this board — what it SHOWS, not just its name. " +
				"For an image that is OCR and layout: pull the text out of a screenshot, compare " +
				"mockups. For a PDF it is the whole document, tables and figures included. Only " +
				"call it when the contents matter; filenames are already in the board listing.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The IMAGE or FILE element to read."),
			}),
		},
		cognition.ToolDef{
			Name: toolReadURL,
			Description: "Fetch the title and description of a URL already on this board or given to " +
				"you, so a link carries what the page actually is. Never invent a URL.",
			Schema: obj([]string{"url"}, map[string]any{"url": str("Full http(s) URL.")}),
		},
		cognition.ToolDef{
			Name: toolArrange,
			Description: "Position elements on the canvas so they read well. You choose the SHAPE; " +
				"the server computes the coordinates, so you never give x/y. " +
				"grid = rows that wrap · row = one horizontal band, for a sequence · " +
				"column = one vertical stack, for a ranking · tidy = keep roughly where they are, " +
				"just remove overlap and even the spacing · " +
				"tree = a hierarchy, parents above children · " +
				"flow = a process left to right, in dependency order. " +
				"tree and flow READ THE ARROWS between the elements, so connect them first.",
			Schema: obj([]string{"elementIds", "layout"}, map[string]any{
				"elementIds": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "Elements to place, in the order they should read.",
				},
				"layout": map[string]any{
					"type": "string",
					"enum": []string{
						string(LayoutGrid), string(LayoutRow), string(LayoutColumn),
						string(LayoutTidy), string(LayoutTree), string(LayoutFlow),
					},
				},
			}),
		},
		cognition.ToolDef{
			Name: toolReorder,
			Description: "Put the items of ONE ordered container into the order you give — a column, " +
				"a to-do list, or this board's unsorted tray. `arrange` is for the canvas and " +
				"is refused on a list; this is its complement. Give every id you want moved, " +
				"in the order they should read: they end up in that sequence, together, " +
				"beneath anything you did not name.",
			Schema: obj([]string{"elementIds"}, map[string]any{
				"elementIds": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "The items, in their new order. They must all sit in the same container.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolAddTasks,
			Description: "Add items to a to-do list that ALREADY EXISTS. They go after what is " +
				"already on it. Use this rather than making a second list beside the first.",
			Schema: obj([]string{"elementId", "tasks"}, map[string]any{
				"elementId": str("The to-do list to add to."),
				"tasks": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
					"description": "One line per item, in the order they should read.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolPlaceFile,
			Description: "Put a file the person ATTACHED to this request onto the board, as a card. " +
				"Use the attachment id exactly as given in the request. " +
				"This is how an attached picture actually gets onto the board — reading it with " +
				"look_at only lets you see it.",
			Schema: obj([]string{"attachmentId", "parentId"}, map[string]any{
				"attachmentId": str("Id of a file attached to this request."),
				"parentId":     str("Board or column to put it in."),
				"title":        str("Short caption. Optional."),
			}),
		},
		cognition.ToolDef{
			Name: toolDocument,
			Description: "Write a DOCUMENT — a page of prose. Use this whenever what you are " +
				"writing is longer than a few lines: a treatment, a brief, an outline, a summary. " +
				"A note is a sticky, and putting three paragraphs on a sticky is how the board " +
				"becomes unreadable.",
			Schema: obj([]string{"parentId", "title", "body"}, map[string]any{
				"parentId": str("Board or column to put it in."),
				"title":    str("What the document is called."),
				"body": str("The prose itself. Blank lines separate paragraphs, and the body " +
					"accepts a small markdown subset that the page really renders: " +
					"`# Heading`, `## Subheading`, `- bullet`, `1. numbered`, `> quote`, " +
					"```fenced code```, **bold**, *italic*, `code` and [text](url). " +
					"Use it — a treatment with headings and lists reads as a document; the " +
					"same words as one unbroken block read as a wall. " +
					"Write it properly — this is the substance, not a placeholder."),
			}),
		},
		cognition.ToolDef{
			Name: toolColor,
			Description: "Put a colour on the board as a swatch. This is how a palette gets made — " +
				"describing a colour in words on a note is not a palette.",
			Schema: obj([]string{"parentId", "color"}, map[string]any{
				"parentId": str("Board or column to put it in."),
				"color":    str("Hex, exactly six digits: #1b2a4a."),
				"title":    str("What this colour is FOR — 'night exteriors', 'brand primary'. Optional but nearly always worth it."),
			}),
		},
		cognition.ToolDef{
			Name: toolShortcut,
			Description: "Put a shortcut to an EXISTING board here, without moving it. Use this " +
				"when a board should be reachable from two places — an index board pointing at " +
				"the real work.",
			Schema: obj([]string{"parentId", "boardId", "title"}, map[string]any{
				"parentId": str("Where the shortcut goes."),
				"boardId":  str("The board it points at."),
				"title":    str("Label for the shortcut."),
			}),
		},
		cognition.ToolDef{
			Name: toolEditTable,
			Description: "Change a table that ALREADY EXISTS — add rows to it, or rewrite it. " +
				"Pass the WHOLE grid you want, header row first, including the rows already " +
				"there. Use this rather than building a second table beside the first.",
			Schema: obj([]string{"elementId", "rows"}, map[string]any{
				"elementId": str("The table to change."),
				"rows": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
					},
					"description": "Every row of the finished table, header first. Rows must be the same length.",
				},
			}),
		},
		cognition.ToolDef{
			Name:        toolSetURL,
			Description: "Point an existing link somewhere else — fixing a dead or wrong address.",
			Schema: obj([]string{"elementId", "url"}, map[string]any{
				"elementId": str("The link card."),
				"url":       str("The new address."),
				"title":     str("New label. Optional — omit to keep the current one."),
			}),
		},
		cognition.ToolDef{
			Name: toolCaption,
			Description: "Caption a picture or file already on the board. An uncaptioned image is " +
				"one nothing else can refer to.",
			Schema: obj([]string{"elementId", "title"}, map[string]any{
				"elementId": str("The image or file card."),
				"title":     str("The caption."),
			}),
		},
		cognition.ToolDef{
			Name: toolCollapse,
			Description: "Fold a column shut, or open one. Collapsing finished stages is how a " +
				"board that has outgrown a screen becomes readable again — it hides nothing " +
				"permanently.",
			Schema: obj([]string{"elementId", "collapsed"}, map[string]any{
				"elementId": str("The column."),
				"collapsed": map[string]any{"type": "boolean", "description": "true to fold shut, false to open."},
			}),
		},
		cognition.ToolDef{
			Name: toolDuplicate,
			Description: "Copy something and everything inside it, as an INDEPENDENT copy that can " +
				"then diverge. This is how episode two starts from episode one. " +
				"Not the same as clone_here, which makes an instance that stays in sync forever.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("What to copy. Its whole contents come with it."),
				"parentId":  str("Where the copy goes. Omit to put it beside the original."),
				"title":     str("Name for the copy — 'Episode 2'. Omit to keep the original's name."),
			}),
		},
		cognition.ToolDef{
			Name: toolConvert,
			Description: "Change what something IS, keeping what it says: a note that has grown " +
				"into a document, or a note listing steps into a real checklist. " +
				"The element keeps its id, so arrows drawn to it and comments on it survive — " +
				"which deleting and recreating would not.",
			Schema: obj([]string{"elementId", "becomes"}, map[string]any{
				"elementId": str("The element to convert."),
				"becomes": map[string]any{
					"type": "string",
					"enum": []string{
						string(domain.TypeDocument), string(domain.TypeCard), string(domain.TypeTaskList),
					},
					"description": "DOCUMENT for long prose, TASK_LIST for a checklist, CARD to go back to a note.",
				},
			}),
		},
		cognition.ToolDef{
			Name: toolUnstage,
			Description: "Remove something you staged earlier in this run, when you have changed " +
				"your mind about it. Anything you put inside it goes too. " +
				"Use this instead of building a second version alongside the first: nothing else " +
				"can remove a staged change, so an abandoned attempt reaches the user otherwise.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The id of the thing you staged and no longer want."),
			}),
		},
		cognition.ToolDef{
			Name: toolStyleBoard,
			Description: "Give a nested board's tile a colour and an icon, so a workspace of " +
				"boards can be told apart at a glance. A tile takes a GRADIENT name from the " +
				"list you were shown — NOT a card swatch, which produces a tile that looks " +
				"broken beside its siblings. Read what the siblings already carry before you " +
				"choose: an unstyled tile takes a colour derived from its id, so picking at " +
				"random is as likely to collide as to distinguish.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The nested board."),
				"color":     str("A tile colour name from the BOARD TILES list."),
				"icon": str("An icon name from the BOARD TILES list, or a single capital " +
					"letter or digit — \"Q\", \"3\" — which is how a board called Q3 gets a tile that says so."),
			}),
		},
		cognition.ToolDef{
			Name: toolDirection,
			Description: "Pin a card's text direction — rtl for Arabic, ltr for Latin, auto to let " +
				"the first letter decide. It is not only alignment: the direction also decides " +
				"whether numbers in the card render as ٠١٢٣ or 0123. Set it when you write " +
				"Arabic into a board that is otherwise Latin (or the reverse), and LEAVE IT " +
				"ALONE when somebody has already pinned one — the board listing marks those.",
			Schema: obj([]string{"elementId", "direction"}, map[string]any{
				"elementId": str("The card, document, column or board."),
				"direction": map[string]any{"type": "string", "enum": []string{"auto", "rtl", "ltr"}},
			}),
		},
		cognition.ToolDef{
			Name: toolResolve,
			Description: "Mark a conversation thread as settled, or reopen one. Use it after you " +
				"have answered what the thread was asking, so the board records that the " +
				"question is closed rather than leaving a live objection standing.",
			Schema: obj([]string{"elementId"}, map[string]any{
				"elementId": str("The conversation thread."),
				"reopen": map[string]any{"type": "boolean",
					"description": "Leave this out to resolve. Set true only to reopen a thread " +
						"somebody had already settled."},
			}),
		},
		cognition.ToolDef{
			Name: toolAlternative,
			Description: "Offer the plan you have staged as ONE OF TWO SHAPES, then build the other. " +
				"Use it only when the structural decision genuinely has two defensible answers — " +
				"one column per scene versus three acts, by owner versus by stage — and you would " +
				"otherwise be picking for them. Everything staged so far is kept as this shape and " +
				"staging is CLEARED, so your next calls build the alternative from nothing. " +
				"Not a way to hedge: if one shape is clearly right, stage it and say why.",
			Schema: obj([]string{"label", "rationale"}, map[string]any{
				"label":     str("Short name for the shape you have just built — \"one column per scene\"."),
				"rationale": str("Why somebody would choose this one over the other, in a clause."),
			}),
		},
		cognition.ToolDef{
			Name: toolShape,
			Description: "Declare that what you are BUILDING is a diagram, so the server lays it out " +
				"as one instead of packing it into rows. " +
				"flow = a process: steps left to right in dependency order · " +
				"tree = a hierarchy: a root above, branches beneath. " +
				"Call this ONCE, then create the cards and connect them — the arrows are what " +
				"the shape is computed from, so a diagram with no connections is just cards.",
			Schema: obj([]string{"shape"}, map[string]any{
				"shape": map[string]any{
					"type": "string",
					"enum": []string{string(LayoutFlow), string(LayoutTree)},
				},
			}),
		},
		cognition.ToolDef{
			Name: toolTidy,
			Description: "Tidy everything loose on this board's canvas at once: remove overlaps, even " +
				"the gutters, align to the grid, while keeping each element roughly where it was. " +
				"The right answer for \"clean this up\" on a board somebody arranged by hand.",
			Schema: obj(nil, map[string]any{}),
		},
		cognition.ToolDef{
			Name: toolHeading,
			Description: "Put a large text landmark on the canvas to name a region. Headings group by " +
				"proximity without boxing anything in — often what a freeform board wants instead " +
				"of another column.",
			Schema: obj([]string{"text"}, map[string]any{"text": str("Short heading, a few words.")}),
		},
		cognition.ToolDef{
			Name: toolResize,
			Description: "Change how much space something takes. For EMPHASIS — one large element gives " +
				"a board a focal point — or for CONSISTENCY, making a ragged group uniform.",
			Schema: obj([]string{"elementIds", "size"}, map[string]any{
				"elementIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"size":       map[string]any{"type": "string", "enum": []string{"small", "medium", "large"}},
			}),
		},
		cognition.ToolDef{
			Name:        toolAssign,
			Description: "Give a task an owner. Work with no owner is the work that quietly does not happen.",
			Schema: obj([]string{"elementId", "userId"}, map[string]any{
				"elementId": str("The task to assign."),
				"userId":    str("A person id from the PEOPLE list you were shown. Never invent one."),
			}),
		},
		cognition.ToolDef{
			Name: toolRemind,
			Description: "Set when a CHECKLIST ITEM is due, so the reminder sweep can act on it. " +
				"Only tasks are swept — a reminder on anything else is accepted and never " +
				"fires. Read the date out of the text rather than inventing one.",
			Schema: obj([]string{"elementId", "when"}, map[string]any{
				"elementId": str("The task."),
				// Wall clock, not an instant. The old schema asked for a UTC
				// timestamp and the agent obliged: a 05:30 crew call written as
				// 05:30Z is 09:30 where this user works.
				"when": str("Local wall-clock time, e.g. 2026-09-01T05:30 — the time a person " +
					"would read off a call sheet. The server converts it using the workspace's " +
					"own timezone, which is stated in the board listing."),
			}),
		},
		cognition.ToolDef{
			Name: toolFileTo,
			Description: "Move something onto a DIFFERENT board — the one it actually belongs on. Only " +
				"boards you have found with search or board_tree, and only ones the user can already " +
				"edit; anything else is refused. Use it to clear a tray of captures onto the " +
				"projects they are about.",
			Schema: obj([]string{"elementId", "boardId"}, map[string]any{
				"elementId": str("Element to file."),
				"boardId":   str("Destination board id, from search or board_tree. Never invent one."),
			}),
		},
	)

	if allowDelete {
		// Merge and split trash what they replace, so they are offered on the
		// same terms as delete: only where a person will see the plan first.
		//
		// The trash verbs ride the same gate from the other direction. A restore
		// loses nothing — but "restore one thing" can bring back thirteen, and an
		// unattended run quietly un-deleting a batch nobody is looking at is the
		// one shape of this feature nobody asked for. The handlers refuse it too;
		// a capability an unattended run cannot use should not be in its
		// catalogue, or the model spends a turn discovering that.
		tools = append(tools,
			cognition.ToolDef{
				Name: toolListTrash,
				Description: "List what the person themselves has deleted and could put back. " +
					"Each entry states how many items its restore would bring with it, because " +
					"a delete removed a container AND everything inside it as one unit.",
				Schema: obj(nil, map[string]any{
					"query": str("Optional. Only show entries whose text contains this."),
				}),
			},
			cognition.ToolDef{
				Name: toolRestore,
				Description: "Bring something back from the trash. It returns with everything the " +
					"same delete removed — restoring a column brings back its cards — so SAY THE " +
					"COUNT list_trash gave you before you propose it. Only things the person " +
					"deleted themselves can be restored; somebody else's clean-up is theirs.",
				Schema: obj([]string{"elementId"}, map[string]any{
					"elementId": str("An id from list_trash."),
				}),
			},
			cognition.ToolDef{
				Name: toolMerge,
				Description: "Combine several cards that say the same thing into one, and trash the " +
					"originals. For genuine duplicates and fragments of a single thought — not to " +
					"shorten a board.",
				Schema: obj([]string{"elementIds", "text"}, map[string]any{
					"elementIds": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
						"description": "Cards to combine. Two or more.",
					},
					"text": str("The merged content. Keep everything that mattered in any of them."),
				}),
			},
			cognition.ToolDef{
				Name: toolMergeColumns,
				Description: "Fold one column into another: everything inside dropId moves to the END of " +
					"keepId and the emptied column is trashed. This is the repair for two columns that are " +
					"the same shelf under two names — 'Editing' beside 'Editing', 'Dev' beside 'Dev & Scoping'. " +
					"Keep the one whose name and place are right.",
				Schema: obj([]string{"keepId", "dropId"}, map[string]any{
					"keepId": str("The column that survives and receives the cards."),
					"dropId": str("The column that is emptied and trashed."),
				}),
			},
			cognition.ToolDef{
				Name: toolSplit,
				Description: "Break one card carrying several separate ideas into one card each, and " +
					"trash the original. Use when a card has to be read twice to find the thing you wanted.",
				Schema: obj([]string{"elementId", "texts"}, map[string]any{
					"elementId": str("Card to break up."),
					"texts": map[string]any{
						"type": "array", "items": map[string]any{"type": "string"},
						"description": "One entry per resulting card. Two or more.",
					},
				}),
			},
		)
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
	// quotas holds every limit this run is held to, and the tally of ids the
	// model named that it was never shown.
	quotas   quotas
	finished bool
	// reviewed records that the model has seen its own arrangement. The loop
	// forces one look before accepting finish, so this is set either way.
	// nudgedToLand records that this run has already been told its answer is
	// not on the board. Once only: a model that means it can finish on the
	// second call, and a check that can fire twice is a loop.
	nudgedToLand bool
	// nudgedOnCreep records that this run has been refused a finish once for
	// widening a follow-up's scope. Same once-only shape as nudgedToLand: the
	// second finish stands, because a model that has been told and still means
	// it is stating a judgement, and a guard that can fire twice is a loop.
	nudgedOnCreep bool
	// reviews counts how many times the run has been made to look at its own
	// work. Bounded, and the second only happens if the plan is still broken.
	reviews int
	// steered records that a person corrected this run while it worked, which —
	// like a refinement — is evidence the board has a convention the agent does
	// not know.
	steered bool
	// failedCalls counts identical failing calls, so a model looping on the
	// same mistake can be told plainly rather than allowed to spend the whole
	// budget rediscovering it.
	failedCalls map[string]int
	// readSoFar is how far into each element's text this run has actually read,
	// in characters from the start.
	//
	// set_note_text replaces the WHOLE body, and the digest shows five hundred
	// characters of it — so "tighten the second half" was a run confidently
	// rewriting six thousand words it had seen a paragraph of, and the
	// difference was invisible in the review list, which shows what will exist
	// and never what will stop existing.
	readSoFar map[string]int
	// asked and question hold the one clarifying question a run may pose.
	images        ImageFetcher
	pendingImages []cognition.ImagePart
	// placedThisRun / movedThisRun catch a plan arguing with itself while it is
	// still being built.
	placedThisRun map[string]bool
	movedThisRun  map[string]bool
	// discovered is every board id this run legitimately found, via search or
	// the board tree. Filing may only target these, which is what stops board
	// content from naming a board at the agent and having it honoured.
	discovered map[string]bool
	links      LinkResolver
	// files resolves an attachment to its stored record, so a file the person
	// attached to the request can be PLACED on the board and not only read.
	files domain.AttachmentRepository
	// comments reads the conversations on the board. The bodies live in their
	// own collection, so a thread element carries only its resolved flag: the
	// argument itself was unreachable, and on a shared board the argument is
	// where the decisions are.
	comments domain.CommentRepository
	question *Question
	// everFinished stays true once the model has signalled it is done, even
	// though the review turn un-sets `finished` to buy one more step. Without
	// it, a run that completed and then reviewed would be reported as "may be
	// incomplete" — the false warning this loop was fixed for once already.
	everFinished bool
}

func newStaging(runID string, scope *BoardScope, task TaskSpec, elements domain.ElementRepository, labels domain.LabelRepository, txns domain.TransactionRepository, images ImageFetcher, links LinkResolver, files domain.AttachmentRepository, comments domain.CommentRepository, emit emitFunc) *staging {
	return &staging{
		runID: runID, scope: scope, task: task, elements: elements, labels: labels, txns: txns, images: images, links: links, files: files, comments: comments, emit: emit,
		plan: &Plan{RunID: runID}, created: map[string]ActionKind{}, failedCalls: map[string]int{},
		quotas: newQuotas(),
	}
}

// resolveParent validates a proposed parent and reports the section a child of
// it should land in.
// resolveParentFor resolves a parent AND checks it may hold this kind of child.
//
// "Is it a container?" was the only question, and a column answers yes — so a
// column was a legal home for another column, which the canvas cannot draw.
func (s *staging) resolveParentFor(id string, child domain.ElementType) (string, string, error) {
	parentID, section, err := s.resolveParent(id)
	if err != nil {
		return "", "", err
	}
	parentType := domain.TypeBoard
	if parentID != s.scope.Board.ID {
		if kind, staged := s.created[parentID]; staged {
			parentType = kind.ElementType()
		} else if el, ok := s.scope.Elements[parentID]; ok {
			parentType = el.Type
		}
	}
	if !CanHold(parentType, child) {
		return "", "", containmentError(parentType, child, parentID)
	}
	return parentID, section, nil
}

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
		s.rejectID(id)
		return "", "", fmt.Errorf("there is no element %s on this board", id)
	}
	if !el.Type.IsContainer() {
		return "", "", fmt.Errorf("%s is a %s and cannot hold other items", id, el.Type)
	}
	return id, string(domain.SectionCanvas), nil
}

// duplicateSibling reports an existing or already-staged container whose name
// means the same thing under the same parent, or "".
//
// A model that stages "Pre-Production" and later stages it again has changed
// its mind, not asked for two. It cannot retract the first — nothing could,
// until now — so a documentary plan arrived as thirteen columns for five
// stages, eight of them duplicates and most of those empty. The board looked
// disorganised because it WAS: the abandoned first attempt survived alongside
// the real one.
//
// The scope half of this guard only started working with the subtree walk. The
// "complete" run created an exact-name `Editing` beside the existing `Editing`
// in the same parent and this function fired on nothing, because the sibling
// lived inside a nested board and the scope could not see into one.
func (s *staging) duplicateSibling(parentID, title string, kind ActionKind) string {
	if title == "" || !kind.Container() {
		return ""
	}
	for i := range s.plan.Actions {
		a := &s.plan.Actions[i]
		if a.Kind == kind && a.ParentID == parentID && sameStructureName(a.Title, title) {
			return a.ElementID
		}
	}
	for _, el := range s.scope.Elements {
		if el.Type != kind.ElementType() || el.Location.ParentID != parentID || el.IsDeleted() {
			continue
		}
		if sameStructureName(el.Title(), title) {
			return el.ID
		}
	}
	return ""
}

// normalizeTitle folds the differences that do not make two names different:
// case, punctuation, the joining word "and", and the hyphen inside a compound.
//
// The hyphen JOINS rather than splits — "pre-production" becomes
// "preproduction" — and that is the whole reason this is token-based rather
// than a substring test. Split it and "Production" looks like a piece of
// "Pre-Production", which are two distinct stages on every board this product
// has ever seeded; refusing the second because the first exists would be a
// worse failure than the duplicates the guard is for.
func normalizeTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '\'' || r == '’':
			// swallowed, so the compound stays one word
		default:
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// "&" is already gone as punctuation; "and" is the same word spelled out,
		// and "Cast & Crew" must not read as a different shelf from "Cast Crew".
		if f == "and" {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// sameStructureName reports whether two container names name the same shelf.
//
// The work order states the rule as "a prefix or contains match covering ≥ 60%
// of the shorter name". It is implemented as WHOLE-TOKEN containment, which is
// the same answer for the case it names — "Concept" is all of itself inside
// "Concept & Premise" — and is the only version that survives real board names.
// A letter ratio calls "Week 1" and "Week 2" an 80% match, and the token that
// differs is the entire point of the name.
func sameStructureName(a, b string) bool {
	if sameNameExactly(a, b) {
		return true
	}
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == "" || nb == "" {
		return false
	}
	return tokensContained(na, nb)
}

// sameNameExactly is the strict half: two spellings of ONE name, with no
// containment. "Casting" inside "Pre-Production Casting" is a proper column and
// must not read as an echo, so the shell-in-shell critique asks this rather
// than the looser sibling test.
func sameNameExactly(a, b string) bool {
	na, nb := normalizeTitle(a), normalizeTitle(b)
	if na == "" || nb == "" {
		return false
	}
	// "Pre Production" and "Pre-Production" are one name spelled two ways, and
	// the hyphen rule above would otherwise keep them apart.
	return na == nb || strings.ReplaceAll(na, " ", "") == strings.ReplaceAll(nb, " ", "")
}

// tokensContained reports whether every word of the shorter name appears in the
// longer one — "Concept" inside "Concept & Premise", "Editing" inside "Sound
// Editing". Whole words only, so a shared syllable does not count.
func tokensContained(a, b string) bool {
	short, long := strings.Fields(a), strings.Fields(b)
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) == 0 {
		return false
	}
	have := make(map[string]bool, len(long))
	for _, t := range long {
		have[t] = true
	}
	for _, t := range short {
		if !have[t] {
			return false
		}
	}
	return true
}

// containerIsEmpty reports whether a container this run might redirect into
// holds nothing at all yet.
//
// A question nobody could ask until the scope walked the subtree, because the
// twin itself was invisible: the guard could only ever refuse, and a refusal is
// what turned "complete" into eighteen new empty columns beside eighteen empty
// ones. The plan is consulted first because it is the only place a staged
// container exists, and the board last because it is the authority.
func (s *staging) containerIsEmpty(ctx context.Context, id string) bool {
	// Anything this plan already files into it counts. Redirecting a second
	// create into a container the run is halfway through filling would hand back
	// an id and quietly lose the name the model meant by the second one.
	for i := range s.plan.Actions {
		if s.plan.Actions[i].ParentID == id {
			return false
		}
	}
	if _, staged := s.created[id]; staged {
		return true // staged this run, and nothing has gone into it
	}
	// The budget elided its contents, so "nothing in scope points at it" is the
	// edge of what was read, not the edge of what is there.
	if s.scope.Elided[id] > 0 {
		return false
	}
	for _, el := range s.scope.Elements {
		if el.Location.ParentID == id && !el.IsDeleted() {
			return false
		}
	}
	// Confirm against the board itself. read_board widens the scope with a
	// board's children and not its grandchildren, so a column found that way is
	// in scope with its contents unknown — and redirecting into a column that
	// turns out to hold thirty cards is a worse answer than refusing.
	if s.elements != nil {
		kids, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: id})
		if err != nil {
			return false // unknown is not empty; never redirect on a guess
		}
		for _, k := range kids {
			if !k.IsDeleted() {
				return false
			}
		}
	}
	return true
}

// resolveConnectable resolves an endpoint for a connector: something already on
// the board, OR something this same plan just staged.
//
// The second half is what makes designing possible at all. resolveExisting only
// knows the compiled scope, so an agent that created three steps could not then
// wire them together — every connect between new cards failed, and the shape
// the diagram was supposed to have never existed. Creating the parts and then
// relating them is the whole activity.
func (s *staging) resolveConnectable(id string) (elementID, label string, err error) {
	if el, ok := s.scope.Elements[id]; ok {
		text, _ := textFor(el, s.scope)
		return el.ID, text, nil
	}
	if _, staged := s.created[id]; staged {
		return id, s.stagedLabel(id), nil
	}
	s.rejectID(id)
	// Name what IS available. A bare "no such element" leaves the model to
	// guess again, and it does: connectors were the last big source of wasted
	// changes because a run inventing an endpoint id had nothing to correct
	// against. Listing the ids it has actually been given turns a dead end into
	// a usable answer.
	return "", "", fmt.Errorf("there is no element %s here. %s", id, s.availableIDs())
}

// availableIDs lists what the run may legitimately point at: the things it has
// staged so far. Bounded, because a run with forty staged cards would otherwise
// answer one mistake with a wall of ids.
func (s *staging) availableIDs() string {
	if len(s.plan.Actions) == 0 {
		return "You have not staged anything yet — create it first, then use the id you are given back."
	}
	var ids []string
	for i := range s.plan.Actions {
		a := &s.plan.Actions[i]
		made := a.CreatedIDs()
		if len(made) == 0 {
			continue
		}
		// The id the model can USE, not the one the action happens to name — a
		// duplicate's ElementID is the original it copied.
		ids = append(ids, fmt.Sprintf("%s (%s)", made[0], truncate(sanitizeText(a.Title+a.Text), 24)))
		if len(ids) == maxSuggestedIDs {
			break
		}
	}
	if len(ids) == 0 {
		return "Use only ids from the board listing above."
	}
	return "Ids you have staged: " + strings.Join(ids, ", ") +
		". Use these exactly as given — never a shortened or reconstructed one."
}

// maxSuggestedIDs bounds the correction. Enough to find the right one, short
// enough not to become the whole turn.
const maxSuggestedIDs = 8

// stagedLabel is what to call an element this plan is about to create.
func (s *staging) stagedLabel(id string) string {
	for i := range s.plan.Actions {
		a := &s.plan.Actions[i]
		if a.ElementID != id {
			continue
		}
		if a.Title != "" {
			return a.Title
		}
		if a.Text != "" {
			return a.Text
		}
		return a.Summary
	}
	return "a new card"
}

// resolveExisting validates that an id refers to an element the run may touch.
func (s *staging) resolveExisting(id string) (*domain.Element, error) {
	el, ok := s.scope.Elements[id]
	if !ok {
		// The plan is a LIVE OVERLAY on the scope, not a write-only log.
		//
		// This read only the compiled scope, so roughly eighteen revise verbs —
		// set_color, apply_label, set_task_done, assign, caption, collapse,
		// resize, convert, edit_table, set_url, duplicate, merge, delete and the
		// rest — refused every id the same run had just created. Create twelve
		// cards and tag them all "Q3": impossible. Create a table and correct a
		// cell: impossible. connect was the lone exception, and its own comment
		// says create-then-relate "is the whole activity" — which is just as
		// true of create-then-revise.
		//
		// The consequence was subtler than the refusals: every value had to be
		// right in the create call, because nothing could be adjusted
		// afterwards. That is a hidden cause of the register failure where the
		// agent builds a second thing instead of editing the first.
		if staged := s.stagedElement(id); staged != nil {
			return staged, nil
		}
		s.rejectID(id)
		return nil, fmt.Errorf("there is no element %s on this board", id)
	}
	if isHomeBoard(el) {
		return nil, fmt.Errorf("the Home board cannot be changed")
	}
	return el, nil
}

// stagedElement synthesises a domain.Element from the action that created it,
// so a tool checking a target's TYPE or reading its text gets the same answer
// for something staged a moment ago as for something already on the board.
//
// Deliberately built from the Action's own fields rather than by compiling the
// op: the compiler is the authority on what reaches the database, and a second
// caller of it here would be a second place for the two to drift.
func (s *staging) stagedElement(id string) *domain.Element {
	kind, staged := s.created[id]
	if !staged {
		return nil
	}
	for i := range s.plan.Actions {
		a := &s.plan.Actions[i]
		if a.ElementID != id {
			continue
		}
		content := domain.Content{}
		if a.Title != "" {
			content["title"] = a.Title
		}
		if a.Text != "" {
			content["textPreview"] = a.Text
		}
		if a.URL != "" {
			content["url"] = a.URL
		}
		if len(a.Rows) > 0 {
			content["cells"] = a.Rows
		}
		if a.Color != "" {
			content["backgroundColor"] = a.Color
		}
		return &domain.Element{
			ID:      id,
			Type:    kind.ElementType(),
			Content: content,
			Location: domain.Location{
				ParentID: a.ParentID,
				Section:  domain.Section(a.Section),
				Index:    a.Index,
			},
		}
	}
	return nil
}

// arrangeScope is the board as the arrangement pass must see it: what is
// already there, plus what this run has staged and not yet written.
//
// resolveExisting already falls through to stagedElement, so every id-taking
// tool could act on something the same run created — except the layout pass,
// which read scope.Elements directly and answered "%s is not on this board".
// The result was that "create five steps then arrange them as a flow" half
// worked: the cards appeared, the connectors appeared, and the layout silently
// failed, which is the confusing shape rather than an honest refusal.
//
// A fresh map, never a write into scope.Elements: the compiled scope is what
// the membership fingerprint and the staleness check are taken against, and
// widening it with elements that do not exist yet would make both of them lie.
func (s *staging) arrangeScope() *BoardScope {
	if len(s.created) == 0 {
		return s.scope
	}
	view := *s.scope
	view.Elements = make(map[string]*domain.Element, len(s.scope.Elements)+len(s.created))
	for id, el := range s.scope.Elements {
		view.Elements[id] = el
	}
	for id := range s.created {
		if el := s.stagedElement(id); el != nil {
			// The staging action carries no geometry — nothing has laid it out
			// yet — so the arranger falls back to the per-type defaults it
			// already uses for a live element with no explicit size.
			view.Elements[id] = el
		}
	}
	return &view
}

// add appends a staged action, enforcing the plan's size budget.
// preStage seeds this run's staging with a plan it already produced.
//
// A refinement used to replay the prior plan as PROSE — `- create_column:
// Casting` per action, no ids, no parents, no destinations — and then instruct
// "you are staging from scratch, so include everything you still want". The
// person's mental model is "change this one thing"; the system's was "do the
// whole job again from a lossy summary of itself". A forty-action plan refined
// with "make the last column purple" spent its entire budget re-deriving
// thirty-nine correct actions, and every re-derivation was a fresh chance to
// silently drop something — which reads to the person as the agent forgetting,
// not as an artefact of re-authoring.
//
// IDS ARE COPIED VERBATIM, never re-minted. Per-action revert indexes into the
// proposed plan by element id and the fingerprint is taken over the same ids, so
// a refinement that re-derived them would break both — and would do it silently,
// because the plan would still look right. `undo_staged` already re-sequences
// while leaving ids bound to their original, which is the same invariant seen
// from the other end.
func (s *staging) preStage(prior *Plan) int {
	if prior == nil || len(prior.Actions) == 0 {
		return 0
	}
	for _, a := range prior.Actions {
		if len(s.plan.Actions) >= s.task.Budget.MaxActions {
			break
		}
		a.Seq = len(s.plan.Actions)
		// Only what CREATES is registered as this run's own work, because that is
		// what `undo_staged` may withdraw and what a later action may parent to.
		for _, id := range a.CreatedIDs() {
			s.created[id] = a.Kind
		}
		s.plan.Actions = append(s.plan.Actions, a)
	}
	// Carried forward with the actions, because they are decisions about the same
	// plan and re-deriving them is the same waste one level up.
	s.plan.Shape = prior.Shape
	if s.plan.Summary == "" {
		s.plan.Summary = prior.Summary
	}
	return len(s.plan.Actions)
}

// describeStaged renders the pre-staged plan as the CURRENT staged list, with
// ids, so the model can address it rather than reconstruct it.
func describeStaged(p *Plan) string {
	var b strings.Builder
	for _, a := range p.Actions {
		label := actionLabel(a)
		fmt.Fprintf(&b, "- %s  %s: %s", a.ElementID, a.Kind, label)
		if a.ParentID != "" {
			fmt.Fprintf(&b, "  → into %s", a.ParentID)
		}
		if a.Destination != "" {
			fmt.Fprintf(&b, " (%s)", a.Destination)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s *staging) add(a Action) (string, error) {
	if len(s.plan.Actions) >= s.task.Budget.MaxActions {
		return "", fmt.Errorf("this plan already has the maximum of %d changes; call finish", s.task.Budget.MaxActions)
	}
	// A stencil is not work. Refused at the boundary where the model can still
	// do something about it, and checked again in Preconditions — the same
	// two-layer shape containment already uses, because a rule enforced only at
	// commit fails the whole run for something the model would have fixed in one
	// turn if asked.
	if s.scope.IsTemplate(a.ParentID) {
		return "", fmt.Errorf("%s", templateRefusal(a.Kind, a.ParentID))
	}
	if !a.Kind.Creates() && s.scope.IsTemplate(a.ElementID) {
		return "", fmt.Errorf("%s", templateRefusal(a.Kind, a.ElementID))
	}
	// Finished work, same rule and same boundary (JN17). Checked after the
	// template pair rather than folded into it so the refusal names the actual
	// reason — "this is a stencil" and "this production wrapped" are different
	// facts and lead the model to different next moves.
	if s.scope.IsArchived(a.ParentID) {
		return "", fmt.Errorf("%s", archivedRefusal(a.Kind, a.ParentID))
	}
	if !a.Kind.Creates() && s.scope.IsArchived(a.ElementID) {
		return "", fmt.Errorf("%s", archivedRefusal(a.Kind, a.ElementID))
	}
	// Corrections the person made themselves, compiled into constraints. Quoting
	// their own words back is the escalation this model obeys: a bare policy
	// assertion gets argued with, and "you already removed this, twice" gets
	// complied with.
	for _, rule := range s.scope.LearnedRules {
		if rule.Matches(a) {
			return "", fmt.Errorf("%s", rule.Refusal())
		}
	}
	a.Seq = len(s.plan.Actions)
	switch {
	case a.Kind == ActDuplicate:
		// A duplicate creates, but its ids are already decided: ElementID names
		// the SOURCE being copied, and the new ids live in Copies — resolved
		// when the subtree was walked, so the review list can say how many
		// elements this really writes. Minting one here would overwrite the
		// source id and the copy would be of nothing.
		if len(a.Copies) > 0 {
			s.created[a.Copies[0].NewID] = a.Kind
		}
	case a.Kind.Creates():
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
// toolArgs is the union of every tool's arguments.
//
// One struct rather than one per tool: the provider hands back a JSON object
// and the handler reads the fields it cares about. A per-tool struct would be
// tidier on paper and would mean 30 more types, 30 more unmarshal sites, and a
// new place for a field name to be spelled differently from its schema.
type toolArgs struct {
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
	KeepID     string     `json:"keepId"`
	DropID     string     `json:"dropId"`
	FromID     string     `json:"fromId"`
	ToID       string     `json:"toId"`
	Rows       [][]string `json:"rows"`
	ElementIDs []string   `json:"elementIds"`
	Mentions   []string   `json:"mentions"`
	Texts      []string   `json:"texts"`
	Layout     string     `json:"layout"`
	FromRow    int        `json:"fromRow"`
	Count      int        `json:"count"`
	// Offset is read_text's cursor, in characters. Separate from FromRow so a
	// page of prose and a page of table rows cannot be confused for each other.
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	UserID   string `json:"userId"`
	When     string `json:"when"`
	Size     string `json:"size"`
	Remember string `json:"remember"`
	// Applied are the standing-rule ids a run says it followed.
	Applied  []string `json:"applied"`
	Relation string   `json:"relation"`
	// Label is the connect tool's own field name. It was missing, so the
	// handler read Title instead and every arrow the agent drew came out blank
	// — the schema asked for one thing and the parser looked for another.
	Label        string `json:"label"`
	Rationale    string `json:"rationale"`
	Direction    string `json:"direction"`
	Reopen       bool   `json:"reopen"`
	Shape        string `json:"shape"`
	AttachmentID string `json:"attachmentId"`
	Body         string `json:"body"`
	Becomes      string `json:"becomes"`
	// Collapsed is a pointer so an omitted field and an explicit false are
	// distinguishable — the schema requires it, and a plain bool would turn a
	// malformed call into a silent "open this column".
	Collapsed *bool `json:"collapsed"`
	// Confidence and Reading are finish's account of how sure it is.
	//
	// Every other field on a Plan is binary or prose, so a guess and a certainty
	// reached the review bar in the same voice. The terse-intent path is
	// explicitly a guess-management mechanism — it tells the model to "SAY WHICH
	// READING YOU TOOK in your summary" — and nothing verified that it had, which
	// made the one place the system KNOWS it interpreted rather than understood
	// the one place it reported in the same register as everything else.
	Confidence string `json:"confidence"`
	Reading    string `json:"reading"`
	// The production reads. Artefact and By are their own names rather than
	// reusing Name and Section: the connect tool spent its whole shipped life
	// reading Title because its schema said `label`, and every arrow came out
	// blank. One field per schema key, always.
	Artefact string `json:"artefact"`
	// Destination is where a finished film is GOING, which is the axis the
	// delivery list actually varies on: a festival DCP, a broadcaster, a
	// streamer and a self-release share a core and then diverge completely.
	Destination string `json:"destination"`
	By          string `json:"by"`
	Anchor      string `json:"anchor"`
	Days        int    `json:"days"`
	Steps       []struct {
		Name     string `json:"name"`
		LeadDays int    `json:"leadDays"`
	} `json:"steps"`
	Unmet []struct {
		Request string `json:"request"`
		Why     string `json:"why"`
	} `json:"unmet"`
}

// reply builds a tool's outcome, and is the one place that notices a model
// stuck repeating a call that cannot succeed.
type reply struct {
	staging *staging
	call    cognition.ToolCall
}

func (r *reply) out(msg string) cognition.ToolOutcome {
	return cognition.ToolOutcome{CallID: r.call.ID, Name: r.call.Name,
		Content: truncate(msg, maxToolOutputBytes)}
}

// fail rejects a call, escalating the wording when the same rejection recurs.
//
// A model that gets the same rejection twice will usually try a third time,
// identically, until the step budget runs out — the user waits and pays for a
// run that stopped progressing several turns ago. Escalating is what breaks the
// loop: the same fact, said in a way that makes repeating the call visibly not
// an option.
func (r *reply) fail(format string, args ...any) cognition.ToolOutcome {
	msg := fmt.Sprintf(format, args...)
	key := r.call.Name + "|" + msg
	r.staging.failedCalls[key]++
	switch n := r.staging.failedCalls[key]; {
	case n == 2:
		msg += " — this is the second identical failure. Do not retry it; " +
			"take a different approach or leave this part alone and say so."
	case n > 2:
		msg = "STOP repeating this call. It has failed " + fmt.Sprint(n) +
			" times with: " + msg + ". Finish with what you have and explain what you could not do."
	}
	return cognition.ToolOutcome{CallID: r.call.ID, Name: r.call.Name, IsError: true,
		Content: truncate(msg, maxRefusalBytes)}
}

// Execute parses one tool call and hands it to the handler registered for that
// name. The dispatch used to be an 800-line switch in this function; each arm
// now lives in toolhandlers.go under a name of its own.
func (s *staging) Execute(ctx context.Context, call cognition.ToolCall) cognition.ToolOutcome {
	r := &reply{staging: s, call: call}

	var in toolArgs
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return r.fail("could not read those arguments: %v", err)
		}
	}

	handler, ok := toolHandlers[call.Name]
	if !ok {
		return r.fail("there is no tool called %q", call.Name)
	}
	return handler(s, ctx, &in, r)
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
	// What this board's canvas already holds, measured while the children are in
	// hand. Reading widened the ids the run may name and left geometry blind:
	// the model would read a board precisely because the digest had elided it,
	// file a column into it, and the packer — with no occupancy for that canvas —
	// would start at the origin, on top of what had just been read out to it.
	canvas := Rect{Empty: true}
	excluded := 0
	for _, el := range children {
		if el.IsDeleted() {
			continue
		}
		// This loop is the THIRD door into scope.Elements — it reads children
		// live from the repository rather than from the compiled scope — so the
		// private flag has to be honoured here too, or read_board is the way
		// round it. Anything the person marked private is neither printed nor
		// made addressable.
		if agentExcluded(el) {
			excluded++
			continue
		}
		// A connector is an edge, not a line of the listing — but reading a board
		// widens what the run may name, and a diagram whose boxes just became
		// addressable while its arrows stayed invisible is the same blindness
		// one level in.
		if el.Type == domain.TypeLine {
			collectEdge(s.scope, el, boardID)
			continue
		}
		if el.Location.Section == domain.SectionCanvas {
			canvas = canvas.include(el)
		}
		live++
		s.scope.Elements[el.ID] = el
		text, trust := textFor(el, s.scope)
		fmt.Fprintf(&b, "%s · %s · ⟨%s⟩ · %s", el.ID, el.Type, trust, truncate(sanitizeText(text), 100))
		if el.Location.Section == domain.SectionUnsorted {
			b.WriteString("  [unsorted]")
		}
		b.WriteString("\n")
	}
	if live == 0 {
		b.WriteString("(empty)\n")
	}
	// Said, not hidden — otherwise a deliberate read comes back looking
	// exhaustive and the model concludes the private material does not exist.
	if excluded > 0 {
		fmt.Fprintf(&b, "(%d item(s) here are marked private and were not read)\n", excluded)
	}
	if s.scope.OccupiedByCanvas == nil {
		s.scope.OccupiedByCanvas = map[string]Rect{}
	}
	s.scope.OccupiedByCanvas[boardID] = canvas
	// The root's box is read through Occupied rather than through the map, so
	// refresh both or the fresher number is the one nothing looks at.
	if boardID == s.scope.Board.ID {
		s.scope.Occupied = canvas
	}
	// Endpoints that were unresolvable when the scope compiled may resolve now
	// that this board's children are addressable.
	s.scope.resolveEdges()
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
	s.rejectID(id)
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
	// A TASK stores its wording under "text" and nothing else, so every summary
	// line about a checklist item — the review row the person approves — used to
	// read as a raw element id.
	if t, ok := el.Content["text"].(string); ok && t != "" {
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
		// Walking the tree is discovery too: these are boards the run has seen
		// for itself, so filing may target them.
		s.markDiscovered(k.ID)
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
		// An element this run created gets the position written onto its own
		// create action rather than a second action that moves it there. Two
		// ops for one card would be honest but noisy in the review list, and
		// LayoutPlan's default shelf pack would still run over the create and
		// hand the arranger's slot to somebody else before the move undid it.
		if s.created[id] != "" {
			pos := box
			if s.setStagedPosition(id, &pos) {
				s.placedThisRun[id] = true
				staged++
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

// setStagedPosition writes a computed box onto the create action that staged
// this element. Reports whether it found one, so the caller can fall back to a
// place action for anything already on the board.
func (s *staging) setStagedPosition(id string, box *ColumnBox) bool {
	for i := range s.plan.Actions {
		a := &s.plan.Actions[i]
		if a.ElementID == id && a.Kind.Creates() {
			a.Position = box
			return true
		}
	}
	return false
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
