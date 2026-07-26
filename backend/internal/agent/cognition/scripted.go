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
	// Calls records every request received, so tests can assert on what the
	// context compiler actually sent — including that untrusted board content
	// was labelled rather than presented as instructions.
	Calls []Request
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
	if s.next >= len(s.steps) {
		return nil, fmt.Errorf("scripted provider: no step %d (only %d scripted)", s.next, len(s.steps))
	}
	step := s.steps[s.next]
	s.next++

	if step.Err != nil {
		return &Response{StopReason: "error", Usage: step.Usage}, step.Err
	}
	out := &Response{Text: step.Text, StopReason: "end_turn", Usage: step.Usage}
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

// LastCall returns the most recent request, for assertions.
func (s *Scripted) LastCall() (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Calls) == 0 {
		return Request{}, false
	}
	return s.Calls[len(s.Calls)-1], true
}
