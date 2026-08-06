package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Scripted is a deterministic Provider for tests and offline development.
//
// It exists because the harness's correctness lives almost entirely OUTSIDE the
// model: scope containment, plan compilation, layout, verification, revert. All
// of that must be provable without a network call or an API key, so the model
// is replaced with a fixture and every other layer runs for real.
//
// It also serves the adversarial suite: a step can name ids the model was never
// shown, or carry injection payloads in a title, and the test asserts the
// harness drops them.
type Scripted struct {
	mu    sync.Mutex
	steps []ScriptedStep
	next  int
	// Aside answers FORCED one-shot calls — the reflection after a rejection,
	// the independent second opinion, the outline pre-phase — from their own
	// queue.
	//
	// Those are not turns of the agentic conversation: they carry a fresh
	// Messages list, a single-tool catalogue and ForceTool, and the model they
	// hit is a different policy. Draining them from `steps` made every existing
	// fixture wrong the moment a second forced call was added anywhere — the
	// judge ate the turn the planner was about to take, and thirty tests that
	// assert nothing about judging failed with "no step N". A fixture opts into
	// the aside by scripting one; one that does not gets ErrNoOutput and the
	// caller carries on without it, which is exactly what a production run does
	// when the extra call fails.
	Aside     []ScriptedStep
	nextAside int
	// Calls records every request received, so tests can assert on what the
	// context compiler actually sent — including that untrusted board content
	// was labelled rather than presented as instructions.
	Calls []Request
}

// OnAside scripts the answers to forced one-shot calls, in order.
func (s *Scripted) OnAside(steps ...ScriptedStep) *Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Aside = append(s.Aside, steps...)
	return s
}

// ScriptedStep is one canned turn.
type ScriptedStep struct {
	// Text is the assistant's prose for this turn.
	Text string
	// Tools are the calls this turn makes. Input may be any JSON-marshalable
	// value for convenience in tests.
	Tools []ScriptedCall
	// Err, when set, is returned instead of the turn.
	Err   error
	Usage Usage
}

// ScriptedCall is one canned tool call.
type ScriptedCall struct {
	Name  string
	Input any
}

var _ Provider = (*Scripted)(nil)

// NewScripted constructs a provider that returns steps in order.
func NewScripted(steps ...ScriptedStep) *Scripted { return &Scripted{steps: steps} }

// Name implements Provider.
func (s *Scripted) Name() string { return "scripted" }

// Model implements Provider.
func (s *Scripted) Model() string { return "scripted" }

// Complete returns the next scripted turn.
func (s *Scripted) Complete(_ context.Context, req Request) (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Calls = append(s.Calls, req)

	var step ScriptedStep
	if req.ForceTool != "" {
		asided, ok := s.takeAside(req.ForceTool)
		if !ok {
			// Nothing scripted for this forced call. ErrNoOutput rather than a
			// conversation step: handing it the planner's next turn is how one
			// added forced call silently rewrote every other fixture's script.
			//
			// Zero usage, deliberately. No model was asked anything — the fixture
			// simply has no answer — so charging the run for it would make "a
			// three-turn run reports three calls" false for a call that never
			// happened, which is the exact class of lie the call counter was
			// fixed for once already.
			return &Response{StopReason: "no_output"}, ErrNoOutput
		}
		step = asided
	} else {
		if s.next >= len(s.steps) {
			return nil, fmt.Errorf("scripted provider: no step %d (only %d scripted)", s.next, len(s.steps))
		}
		step = s.steps[s.next]
		s.next++
	}

	// A scripted turn is a provider call and must account for itself like one.
	// Fixtures rarely bother stating usage, so with a zero here the telemetry
	// assertion — a three-turn run reports calls:3 — could only ever be proved
	// against the real providers, which is to say never.
	usage := step.Usage
	if usage.Calls == 0 {
		usage.Calls = 1
	}

	if step.Err != nil {
		return &Response{StopReason: "error", Usage: usage}, step.Err
	}
	out := &Response{Text: step.Text, StopReason: "end_turn", Usage: usage}
	for i, c := range step.Tools {
		raw, err := json.Marshal(c.Input)
		if err != nil {
			return nil, fmt.Errorf("scripted provider: marshal call %s: %w", c.Name, err)
		}
		out.Calls = append(out.Calls, ToolCall{
			ID:    fmt.Sprintf("%s-%d-%d", c.Name, s.next-1, i),
			Name:  c.Name,
			Input: raw,
		})
	}
	if len(out.Calls) > 0 {
		out.StopReason = "tool_use"
	}
	return out, nil
}

// takeAside serves a forced call, from the aside queue when one is scripted and
// otherwise from a conversation step that plainly answers THIS tool.
//
// The fallback keeps the fixtures that predate the aside queue working: a test
// that scripts one step calling propose_rule and then calls Reflect is scripting
// a forced call, whatever slice it put it in. The name match is what makes that
// safe — a step calling `stage.plan` is never mistaken for an answer to `judge`.
func (s *Scripted) takeAside(force string) (ScriptedStep, bool) {
	if s.nextAside < len(s.Aside) {
		step := s.Aside[s.nextAside]
		s.nextAside++
		return step, true
	}
	if s.next < len(s.steps) {
		if step := s.steps[s.next]; len(step.Tools) > 0 && step.Tools[0].Name == force {
			s.next++
			return step, true
		}
	}
	return ScriptedStep{}, false
}

// LastCall returns the most recent request, for assertions.
func (s *Scripted) LastCall() (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Calls) == 0 {
		return Request{}, false
	}
	return s.Calls[len(s.Calls)-1], true
}
