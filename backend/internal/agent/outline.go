package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qomranote/backend/internal/agent/cognition"
)

// Outline-first: the person steers before the budget is spent.
//
// The loop runs to exhaustion — twenty-four turns after W4 — and the FIRST
// chance to redirect it is the PROPOSED→PLANNING refine edge, which is to say
// after the whole run has been paid for. So a misread intent costs a full run's
// tokens and a full minute of somebody's attention before they can say "no, not
// like that", and Refine then pays for a second full run.
//
// It is also the only cure for step starvation that does not simply raise the
// budget: a person who trims the outline to four columns spends the twenty-four
// steps on four columns.
//
// A PRE-PHASE, not a new state machine. One provider call on the cheap tier,
// read-only, forced onto a typed shape. It stages nothing and the PROPOSED
// review is unchanged.

// Outline is what the run says it is about to do, before it does any of it.
//
// TYPED, not prose. An outline the UI cannot edit is decorative: the whole
// mechanism turns on a person being able to strike a step out, and a free-text
// paragraph gives them nothing to strike.
type Outline struct {
	Steps []OutlineStep `bson:"steps" json:"steps"`
	// EstimatedActions is the run's own guess at the size of the plan, so the
	// ceiling line has something to show before any of it exists.
	EstimatedActions int `bson:"estimatedActions,omitempty" json:"estimatedActions,omitempty"`
	// Uncertain names the parts the run is least sure about — the ones most
	// worth a person's glance, and the first thing they should read.
	Uncertain []string `bson:"uncertain,omitempty" json:"uncertain,omitempty"`
}

// OutlineStep is one line of the checklist.
type OutlineStep struct {
	// Verb is what will be done — "create columns", "file cards", "write".
	Verb string `bson:"verb" json:"verb"`
	// Target is what it will be done to, in the person's terms.
	Target string `bson:"target" json:"target"`
	// Count is roughly how many things, for the steps where a number is the
	// thing somebody would object to.
	Count int `bson:"count,omitempty" json:"count,omitempty"`
	// Note is one clause of reasoning, where the step is not self-evident.
	Note string `bson:"note,omitempty" json:"note,omitempty"`
}

// Empty reports whether the outline says anything worth showing.
func (o *Outline) Empty() bool { return o == nil || len(o.Steps) == 0 }

// Render is the outline as a person reads it.
func (o *Outline) Render() string {
	if o.Empty() {
		return ""
	}
	var b strings.Builder
	for _, s := range o.Steps {
		line := "- " + s.Verb
		if s.Count > 0 {
			line += fmt.Sprintf(" ×%d", s.Count)
		}
		if s.Target != "" {
			line += ": " + s.Target
		}
		if s.Note != "" {
			line += " — " + s.Note
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// outlineTool is the one shape the pre-phase may return.
var outlineTool = cognition.ToolDef{
	Name:        "outline",
	Description: "State the plan you are ABOUT to make, as a short checklist. You are not doing it yet.",
	Schema: map[string]any{
		"type":     "object",
		"required": []string{"steps"},
		"properties": map[string]any{
			"steps": map[string]any{
				"type":        "array",
				"description": "Three to seven steps. Fewer is better; this is a sketch, not the plan.",
				"items": map[string]any{
					"type":     "object",
					"required": []string{"verb", "target"},
					"properties": map[string]any{
						"verb":   map[string]any{"type": "string", "description": "What you will do — \"create columns\", \"file cards\", \"write a treatment\"."},
						"target": map[string]any{"type": "string", "description": "What it acts on, in the person's own terms."},
						"count":  map[string]any{"type": "integer", "description": "Roughly how many, where a number is what somebody would argue with."},
						"note":   map[string]any{"type": "string", "description": "One clause, only where the step is not self-evident."},
					},
				},
			},
			"estimatedActions": map[string]any{"type": "integer",
				"description": "Roughly how many changes the whole plan will be."},
			"uncertain": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "The decisions you are least sure about. This is what they will look at first.",
			},
		},
	},
}

// outlineSystem is deliberately short. A model given the whole authoring rulebook
// starts authoring, and this turn exists precisely to happen before that.
const outlineSystem = `You are about to reorganise or build something on a visual board, and this is
the sketch you show first. Do NOT plan the work in detail and do not decide
anything you could still change your mind about.

Three to seven steps. Say what you will DO and what it acts on, in the person's
own words. Name what you are unsure about — they are going to strike out
whatever they did not want, and the steps they strike are the ones you must not
do.

Call outline exactly once.`

// ComposeOutline runs the pre-phase.
//
// Cheap tier by construction: this is a summary of an intent against a board
// listing, which is transcription rather than judgement — and the whole
// justification for the extra call is that it costs a fraction of the run it
// might redirect.
//
// A failure here is not a failed run. The outline is an accelerator; losing it
// to a 502 leaves the loop exactly as it was before this existed.
func ComposeOutline(ctx context.Context, provider cognition.Provider, scope *BoardScope, task TaskSpec) (*Outline, cognition.Usage, error) {
	var usage cognition.Usage
	if provider == nil {
		return nil, usage, cognition.ErrNotConfigured
	}
	var b strings.Builder
	fmt.Fprintf(&b, "REQUEST ⟨user⟩: %s\n\n", task.Intent)
	if scope != nil {
		b.WriteString(scope.Render(""))
	}
	b.WriteString("\nSketch what you are about to do. Call outline.")

	resp, err := provider.Complete(ctx, cognition.Request{
		System:    outlineSystem,
		Messages:  []cognition.Message{{Role: cognition.RoleUser, Text: b.String()}},
		Tools:     []cognition.ToolDef{outlineTool},
		ForceTool: outlineTool.Name,
		MaxTokens: 600,
		Label:     "plan.outline",
		Tier:      cognition.TierFast,
	})
	if resp != nil {
		usage.Add(resp.Usage)
	}
	if err != nil {
		return nil, usage, err
	}
	for _, call := range resp.Calls {
		if call.Name != outlineTool.Name {
			continue
		}
		var out Outline
		if err := json.Unmarshal(call.Input, &out); err != nil {
			return nil, usage, nil
		}
		// Sanitized here, on the way IN, because it was composed over board
		// content and will be rendered to a person and replayed to the model.
		for i := range out.Steps {
			out.Steps[i].Verb = truncate(sanitizeText(out.Steps[i].Verb), 60)
			out.Steps[i].Target = truncate(sanitizeText(out.Steps[i].Target), 120)
			out.Steps[i].Note = truncate(sanitizeText(out.Steps[i].Note), 160)
		}
		for i := range out.Uncertain {
			out.Uncertain[i] = truncate(sanitizeText(out.Uncertain[i]), 160)
		}
		if len(out.Steps) > maxOutlineSteps {
			out.Steps = out.Steps[:maxOutlineSteps]
		}
		return &out, usage, nil
	}
	return nil, usage, nil
}

// maxOutlineSteps bounds the checklist. Past about seven it stops being a sketch
// somebody skims and becomes a plan they have to review — which is the thing
// this exists to happen before.
const maxOutlineSteps = 7

// OutlineSteer turns the steps a person struck out into an instruction the
// authoring turn cannot ignore.
//
// Stated as removals rather than as a whitelist: "do not do X, Y" is checkable
// against a finished plan and "here is what to do" is not, and the same asymmetry
// is why the adjustment replay states drops as decisions rather than summarising
// them.
func OutlineSteer(o *Outline, kept []bool) string {
	if o.Empty() || len(kept) != len(o.Steps) {
		return ""
	}
	var dropped []string
	for i, k := range kept {
		if !k {
			s := o.Steps[i]
			dropped = append(dropped, strings.TrimSpace(s.Verb+" "+s.Target))
		}
	}
	if len(dropped) == 0 {
		return ""
	}
	return "\nBefore you started, they looked at your sketch and REMOVED these steps ⟨user⟩:\n- " +
		strings.Join(dropped, "\n- ") +
		"\nThose are decisions, not suggestions. Do not do them, and do not do a variation " +
		"of them under another name. Everything else in the sketch stands.\n"
}
