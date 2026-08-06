package agent

import (
	"fmt"
	"strings"

	"qomranote/backend/internal/domain"
)

// The action registry.
//
// Adding one capability used to mean edits in four places: the ActionKind
// constant, the CompileOps switch, shapeOf's validation case, and the tool that
// stages it. Nothing connected them, so a kind could exist, validate, stage —
// and fail to compile. That is exactly what happened to create_table, connect
// and clone_here: three capabilities that shipped, passed every test, and could
// not produce a single op, because the dispatch arm was missed and a `case` is
// not something the compiler can notice the absence of.
//
// A kind now carries its own behaviour. Registration is one call, the pieces
// travel together, and a test asserts every kind has a spec — so "declared but
// uncompilable" stops being expressible rather than being something we remember
// to check.
//
// This deliberately does NOT move the compile bodies yet. The registry owns the
// mapping; the bodies stay where they are and are referenced. That keeps this a
// structural change with no behavioural risk, and lets the bodies migrate one
// at a time afterwards.

// ActionSpec is everything the harness needs to know about one kind of action.
type ActionSpec struct {
	// Kind is the wire name. Duplicated from the map key on purpose: a spec
	// that cannot say what it is cannot be validated against its own key.
	Kind ActionKind

	// Creates reports whether this brings a new element into being, which
	// decides whether its ElementID is minted at staging time and whether
	// dropping it must cascade to its children.
	Creates bool

	// Container reports whether the created element can hold others, and so
	// whether a later action may parent to it.
	Container bool

	// Destructive reports whether the action loses information. Destructive
	// kinds cannot run unattended.
	Destructive bool

	// Validate checks the action carries the fields this kind requires. Pure:
	// it sees one action and nothing else.
	Validate func(a Action) error
}

// actionSpecs is the single registry. Every kind the system understands appears
// here exactly once.
var actionSpecs = map[ActionKind]ActionSpec{}

func init() {
	register(ActionSpec{
		Kind: ActAddTasks,
		Validate: func(a Action) error {
			if a.ElementID == "" {
				return fmt.Errorf("adding tasks needs the list to add them to")
			}
			if len(a.Tasks) == 0 {
				return fmt.Errorf("adding tasks needs at least one task")
			}
			return nil
		},
	})
	register(ActionSpec{
		Kind: ActPlaceFile, Creates: true,
		Validate: func(a Action) error {
			if a.AssigneeID == "" {
				return fmt.Errorf("placing a file needs the attachment it refers to")
			}
			if a.URL == "" {
				return fmt.Errorf("placing a file needs the attachment's url")
			}
			return nil
		},
	})

	// Authoring the three types the toolbar had and the plan did not.
	register(ActionSpec{
		Kind: ActWriteDocument, Creates: true,
		Validate: func(a Action) error {
			if strings.TrimSpace(a.Title) == "" {
				return fmt.Errorf("a document needs a title")
			}
			if strings.TrimSpace(a.Text) == "" {
				return fmt.Errorf("a document needs a body — an empty page is worse than a note")
			}
			return nil
		},
	})
	register(ActionSpec{
		Kind: ActAddColor, Creates: true,
		Validate: func(a Action) error {
			if !isHexColor(a.Color) {
				return fmt.Errorf("a swatch needs a hex colour like #1b2a4a, got %q", a.Color)
			}
			return nil
		},
	})
	register(ActionSpec{
		Kind: ActLinkBoard, Creates: true,
		Validate: func(a Action) error {
			if a.FromID == "" {
				return fmt.Errorf("a shortcut needs the board it points at")
			}
			return needsTitle(a)
		},
	})

	// Revising what already exists.
	register(ActionSpec{Kind: ActEditTable, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("editing a table needs the table")
		}
		return validateTable(a)
	}})
	register(ActionSpec{Kind: ActSetURL, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("repointing a link needs the link")
		}
		if a.URL == "" {
			return fmt.Errorf("repointing a link needs the new address")
		}
		return nil
	}})
	register(ActionSpec{Kind: ActSetCaption, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("captioning needs something to caption")
		}
		return needsTitle(a)
	}})
	register(ActionSpec{Kind: ActCollapse, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("collapsing needs the column")
		}
		if a.Collapsed == nil {
			return fmt.Errorf("collapsing needs to say open or shut")
		}
		return nil
	}})

	// Restructuring.
	register(ActionSpec{
		Kind: ActDuplicate, Creates: true, Container: true,
		Validate: func(a Action) error {
			if a.ElementID == "" {
				return fmt.Errorf("duplicating needs something to copy")
			}
			if len(a.Copies) == 0 {
				return fmt.Errorf("duplicating resolved to no elements")
			}
			return nil
		},
	})
	// A conversion REPLACES what an element is. Destructive, so a plan
	// containing one cannot run unattended: turning a note into a checklist
	// discards its formatting, and only a person can say that is fine.
	register(ActionSpec{
		Kind: ActConvert, Destructive: true,
		Validate: func(a Action) error {
			if a.ElementID == "" {
				return fmt.Errorf("converting needs something to convert")
			}
			switch domain.ElementType(a.Becomes) {
			case domain.TypeDocument, domain.TypeCard, domain.TypeTaskList:
				return nil
			}
			return fmt.Errorf("cannot convert into a %s", a.Becomes)
		},
	})
}

// isHexColor accepts the #rrggbb the swatch card parses. It slices fixed
// offsets out of the string, so a short or malformed value renders as NaN.
func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < 7; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func register(s ActionSpec) {
	if _, dup := actionSpecs[s.Kind]; dup {
		// A duplicate registration means two definitions of one kind disagree,
		// and whichever loads second wins silently. Panicking at init is the
		// only place this can be caught before it matters.
		panic(fmt.Sprintf("agent: action kind %q registered twice", s.Kind))
	}
	actionSpecs[s.Kind] = s
}

// SpecFor returns the spec for a kind, and whether it is one the system knows.
func SpecFor(k ActionKind) (ActionSpec, bool) {
	s, ok := actionSpecs[k]
	return s, ok
}

// KnownKinds lists every registered kind, for tests and diagnostics.
func KnownKinds() []ActionKind {
	out := make([]ActionKind, 0, len(actionSpecs))
	for k := range actionSpecs {
		out = append(out, k)
	}
	return out
}

func init() {
	// Creates ----------------------------------------------------------------
	register(ActionSpec{Kind: ActCreateBoard, Creates: true, Container: true, Validate: needsTitle})
	register(ActionSpec{Kind: ActCreateColumn, Creates: true, Container: true, Validate: needsTitle})
	register(ActionSpec{Kind: ActCreateTodo, Creates: true, Container: true, Validate: func(a Action) error {
		if a.Title == "" || len(a.Tasks) == 0 {
			return fmt.Errorf("a to-do list needs a title and tasks")
		}
		return nil
	}})
	register(ActionSpec{Kind: ActCreateNote, Creates: true, Validate: needsText("a note needs text")})
	register(ActionSpec{Kind: ActCreateHeading, Creates: true, Validate: needsText("a heading needs text")})
	register(ActionSpec{Kind: ActComment, Creates: true, Validate: needsText("a comment needs something to say")})
	register(ActionSpec{Kind: ActCreateLink, Creates: true, Validate: func(a Action) error {
		if a.URL == "" {
			return fmt.Errorf("a link needs a URL")
		}
		return nil
	}})
	register(ActionSpec{Kind: ActCreateTable, Creates: true, Validate: validateTable})
	register(ActionSpec{Kind: ActConnect, Creates: true, Validate: validateConnect})
	register(ActionSpec{Kind: ActCloneHere, Creates: true, Validate: func(a Action) error {
		if a.FromID == "" || a.ParentID == "" {
			return fmt.Errorf("a clone needs a source and a destination")
		}
		return nil
	}})

	// Moves and placement ----------------------------------------------------
	register(ActionSpec{Kind: ActMove, Validate: validateMove})
	register(ActionSpec{Kind: ActPlace, Validate: func(a Action) error {
		if a.ElementID == "" || a.Position == nil {
			return fmt.Errorf("placing needs a target and a position")
		}
		return nil
	}})
	register(ActionSpec{Kind: ActResize, Validate: func(a Action) error {
		if a.ElementID == "" || a.Position == nil || a.Position.Width <= 0 {
			return fmt.Errorf("resizing needs a target and a width")
		}
		return nil
	}})

	// Attribute edits --------------------------------------------------------
	register(ActionSpec{Kind: ActRename, Validate: needsTitle})
	register(ActionSpec{Kind: ActSetText, Validate: needsText("a rewrite needs text")})
	register(ActionSpec{Kind: ActApplyLabel, Validate: func(a Action) error {
		if a.ElementID == "" || a.LabelID == "" {
			return fmt.Errorf("tagging needs an element and a label")
		}
		return nil
	}})
	// An empty colour is the "default" swatch, so only the target is required —
	// clearing a colour is a legitimate edit.
	register(ActionSpec{Kind: ActSetColor, Validate: needsTarget("colouring")})
	register(ActionSpec{Kind: ActSetTask, Validate: needsTarget("ticking")})
	register(ActionSpec{Kind: ActSetAssignee, Validate: func(a Action) error {
		if a.ElementID == "" || a.AssigneeID == "" {
			return fmt.Errorf("assigning needs a task and a person")
		}
		return nil
	}})
	register(ActionSpec{Kind: ActSetReminder, Validate: func(a Action) error {
		if a.ElementID == "" || a.RemindAt == "" {
			return fmt.Errorf("a reminder needs a task and a time")
		}
		return nil
	}})

	register(ActionSpec{Kind: ActSetDirection, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("pinning a direction needs a target")
		}
		switch a.Text {
		case "auto", "rtl", "ltr":
			return nil
		}
		return fmt.Errorf("direction must be auto, rtl or ltr, not %q", a.Text)
	}})
	register(ActionSpec{Kind: ActResolveThread, Validate: needsTarget("resolving a thread")})
	// A restore is not destructive — it puts information BACK — but it is the one
	// verb whose blast radius is not visible in its own row: the trash batch means
	// "restore 1 thing" can return thirteen. The handler states the count and the
	// review row repeats it; the spec stays honest about what the action does.
	register(ActionSpec{Kind: ActRestore, Validate: needsTarget("a restore")})
	register(ActionSpec{Kind: ActStyleBoard, Validate: func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("styling needs a board")
		}
		if a.Color == "" && a.Text == "" {
			return fmt.Errorf("styling needs a colour, an icon, or both")
		}
		if a.Color != "" {
			if _, ok := boardColors[a.Color]; !ok {
				return fmt.Errorf("%q is not a board tile colour", a.Color)
			}
		}
		if !boardIconAllowed(a.Text) {
			return fmt.Errorf("%q is not an icon this product has", a.Text)
		}
		return nil
	}})

	// Destructive ------------------------------------------------------------
	register(ActionSpec{Kind: ActDelete, Destructive: true, Validate: needsTarget("a deletion")})
}

func needsTitle(a Action) error {
	if a.Title == "" {
		return fmt.Errorf("%s needs a title", a.Kind)
	}
	return nil
}

func needsText(msg string) func(Action) error {
	return func(a Action) error {
		if a.Text == "" {
			return fmt.Errorf("%s", msg)
		}
		return nil
	}
}

func needsTarget(what string) func(Action) error {
	return func(a Action) error {
		if a.ElementID == "" {
			return fmt.Errorf("%s needs a target", what)
		}
		return nil
	}
}

func validateMove(a Action) error {
	if a.ParentID == "" {
		return fmt.Errorf("a move needs a destination")
	}
	if a.ParentID == a.ElementID {
		return fmt.Errorf("an element cannot contain itself")
	}
	return nil
}

func validateConnect(a Action) error {
	if a.FromID == "" || a.ToID == "" {
		return fmt.Errorf("a connection needs both ends")
	}
	if a.FromID == a.ToID {
		return fmt.Errorf("an element cannot be connected to itself")
	}
	return nil
}

func validateTable(a Action) error {
	if len(a.Rows) < 2 {
		return fmt.Errorf("a table needs a header row and at least one data row")
	}
	width := len(a.Rows[0])
	for i, r := range a.Rows {
		if len(r) != width {
			return fmt.Errorf("table row %d has %d cells, header has %d", i, len(r), width)
		}
	}
	return nil
}
