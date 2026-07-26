package agent_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Tool-selection evals.
//
// The structural evals check that a plan is well FORMED. These check that it is
// the RIGHT KIND OF PLAN for what was asked — the weak link every live run
// exposed: asked to tidy, it rebuilt into columns; asked to merge duplicates, it
// deleted both; asked a question, it rearranged the board.
//
// A well-formed answer to the wrong question is still the wrong answer, and no
// amount of validation catches it, because nothing was malformed.
//
// These run on the scripted provider, so they assert on the HARNESS's response
// to a given sequence of calls rather than on a live model's judgement. That is
// the part we control and the part that must not regress: the tools a request
// makes available, the ones it refuses, and what the harness does with an
// answer that stages nothing.

// intentCase is one request and the shape its plan must have.
type intentCase struct {
	name string
	// forbidden kinds are the ones that mean the agent answered a different
	// question from the one asked.
	forbidden []agent.ActionKind
	// wantKinds, when set, must all appear.
	wantKinds []agent.ActionKind
	steps     []cognition.ScriptedStep
	autonomy  agent.Autonomy
}

func TestToolChoice_PlanShapeMatchesTheRequest(t *testing.T) {
	cases := []intentCase{
		{
			name: "a question is answered, not acted on",
			// No actions at all, and the answer survives as the summary.
			steps: []cognition.ScriptedStep{
				finish("Nothing owns the migration guide, and there is no launch date."),
			},
		},
		{
			name:      "tidying does not restructure",
			forbidden: []agent.ActionKind{agent.ActCreateColumn, agent.ActMove},
			wantKinds: []agent.ActionKind{agent.ActPlace},
			steps: []cognition.ScriptedStep{
				{Tools: []cognition.ScriptedCall{{Name: "tidy_board", Input: map[string]any{}}}},
				finish("Tidied the canvas."),
				confirm(),
			},
		},
		{
			name:      "grouping does not reposition",
			forbidden: []agent.ActionKind{agent.ActPlace},
			wantKinds: []agent.ActionKind{agent.ActCreateColumn},
			steps: []cognition.ScriptedStep{
				{Tools: []cognition.ScriptedCall{
					call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
				}},
				finish("Grouped."),
				confirm(),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.steps...)
			h.seedScattered(t, boardID)
			autonomy := c.autonomy
			if autonomy == "" {
				autonomy = agent.AutonomyPreview
			}
			run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
				BoardID: boardID, Intent: c.name, Autonomy: autonomy,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			final := h.awaitState(t, run.ID, agent.StateProposed, agent.StatePartial)
			if final.Plan == nil {
				t.Fatalf("no plan; state %s reason %q", final.State, final.Reason)
			}

			got := map[agent.ActionKind]int{}
			for _, a := range final.Plan.Actions {
				got[a.Kind]++
			}
			for _, k := range c.forbidden {
				if got[k] > 0 {
					t.Errorf("plan contains %d %s action(s) — that answers a different request", got[k], k)
				}
			}
			for _, k := range c.wantKinds {
				if got[k] == 0 {
					t.Errorf("plan has no %s action; kinds present: %v", k, keysOfKinds(got))
				}
			}
			// A question must leave an answer behind, not an empty result.
			if len(c.wantKinds) == 0 && len(c.forbidden) == 0 {
				if len(final.Plan.Actions) != 0 {
					t.Errorf("a question staged %d change(s); it should change nothing", len(final.Plan.Actions))
				}
				if strings.TrimSpace(final.Plan.Summary) == "" {
					t.Error("a question produced no answer — the summary was discarded")
				}
			}
		})
	}
}

// Destructive repairs must be unavailable to an unattended run: merging and
// splitting trash the originals, and nobody sees the plan.
func TestToolChoice_UnattendedRunsCannotDestroy(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			{Name: "merge_notes", Input: map[string]any{
				"elementIds": []string{cardID(boardID, 0), cardID(boardID, 1)},
				"text":       "combined",
			}},
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "one", "two")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "merge the duplicates", Autonomy: agent.AutonomyAuto,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed, agent.StatePartial,
		agent.StateCompleted, agent.StateFailed)
	if final.Plan != nil {
		for _, a := range final.Plan.Actions {
			if a.Kind == agent.ActDelete {
				t.Fatal("an unattended run staged a deletion via merge")
			}
		}
	}
}

func keysOfKinds(m map[agent.ActionKind]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return out
}

// The audit answers "what has the AI changed here". It must list runs that
// actually wrote something and stay quiet about the rest — an audit log full of
// discarded previews is one nobody reads.
func TestAudit_ListsOnlyRunsThatChangedSomething(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			call("create_column", map[string]any{"parentId": boardID, "title": "Pricing"}),
		}},
		finish("Made a column."),
		confirm(),
		// A second run that answers without changing anything.
		finish("Nothing needs doing here."),
	)
	h.seedBoard(t, boardID, "a note")
	ctx := context.Background()

	applied, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Group these", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, applied.ID, agent.StateProposed)
	if _, err := h.svc.Apply(ctx, h.principal, applied.ID, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	answered, err := h.svc.Create(ctx, h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "Anything missing?", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	h.awaitState(t, answered.ID, agent.StatePartial)

	entries, err := h.svc.Audit(ctx, h.principal, boardID, 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit listed %d entries; only the run that wrote something belongs there", len(entries))
	}
	e := entries[0]
	if e.RunID != applied.ID {
		t.Errorf("audit named run %s, want %s", e.RunID, applied.ID)
	}
	if e.Intent != "Group these" || e.Ops == 0 {
		t.Errorf("audit entry is not legible: %+v", e)
	}
	if e.Reverted {
		t.Error("an applied run was reported as reverted")
	}
}

// Every implemented tool must be OFFERED. A capability that exists in the
// execute switch and not in the catalogue is one nobody can reach, which is
// indistinguishable from never having built it — and it fails silently, because
// the code compiles, the tests pass, and the model simply never calls it.
//
// This shipped for real: eleven tools, including all of vision, composition and
// repair, were implemented and absent from the catalogue.
func TestToolCatalogue_OffersEveryImplementedTool(t *testing.T) {
	source, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatalf("read tools.go: %v", err)
	}
	src := string(source)

	// Implemented: every `case toolX:` in the execute switch.
	implemented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\tcase (tool[A-Za-z]+)`).FindAllStringSubmatch(src, -1) {
		implemented[m[1]] = true
	}
	// Offered: every `Name: toolX` in the catalogue.
	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`Name:\s+(tool[A-Za-z]+)`).FindAllStringSubmatch(src, -1) {
		offered[m[1]] = true
	}

	if len(implemented) == 0 || len(offered) == 0 {
		t.Fatal("could not parse the tool surface; this test needs updating alongside tools.go")
	}
	var missing []string
	for name := range implemented {
		if !offered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d tool(s) are implemented but never offered to the model: %s",
			len(missing), strings.Join(missing, ", "))
	}

	// And the catalogue actually built at runtime must be non-trivial, so a
	// refactor that empties it fails here rather than in production.
	if n := len(agent.ToolCatalogue(true, true)); n < len(implemented)-2 {
		t.Errorf("the built catalogue offers %d tools; %d are implemented", n, len(implemented))
	}
}

// Cross-board filing widens containment, so it is the one capability that could
// turn the agent into a way to write somewhere the person cannot. These pin the
// two properties that stop it: the destination must have been DISCOVERED by the
// run, and it must be one the HUMAN can already edit.
func TestFiling_RefusesABoardTheRunNeverFound(t *testing.T) {
	h := newHarness(t,
		cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
			// A board id that never appeared in any tool output — exactly what
			// an id lifted from card text looks like.
			{Name: "file_to_board", Input: map[string]any{
				"elementId": cardID(boardID, 0),
				"boardId":   "ffffffffffffffffffffffff",
			}},
		}},
		finish("done"),
		confirm(),
	)
	h.seedBoard(t, boardID, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, agent.CreateRequest{
		BoardID: boardID, Intent: "File this elsewhere", Autonomy: agent.AutonomyPreview,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	final := h.awaitState(t, run.ID, agent.StateProposed, agent.StatePartial)
	if final.Plan != nil {
		for _, a := range final.Plan.Actions {
			if a.Kind == agent.ActMove && a.ParentID == "ffffffffffffffffffffffff" {
				t.Fatal("filed to a board the run never discovered")
			}
		}
		if len(final.Plan.Destinations) > 0 {
			t.Errorf("an undiscovered board became a destination: %v", final.Plan.Destinations)
		}
	}
}

// The grant carries destinations only for boards the human can edit, and the
// stored delegation is never widened — a destination authorised for one commit
// must not persist into the next.
func TestFiling_GrantIsAttenuatedNotExpanded(t *testing.T) {
	d := &domain.Delegation{RootBoardID: "root"}
	if got := d.Roots(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("a grant with no destinations should permit only its root, got %v", got)
	}
	d.DestinationBoardIDs = []string{"other"}
	roots := d.Roots()
	if len(roots) != 2 || roots[0] != "root" || roots[1] != "other" {
		t.Fatalf("Roots() must list the root first, then destinations; got %v", roots)
	}
	// A nil grant permits nothing rather than everything — the direction a
	// mistake here has to fail in.
	var none *domain.Delegation
	if got := none.Roots(); got != nil {
		t.Errorf("a nil delegation returned roots %v; it must permit nothing", got)
	}
}
