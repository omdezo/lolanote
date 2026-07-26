// Package cognition is the model plane: it abstracts inference behind one
// narrow interface so the rest of the harness never depends on a provider SDK.
//
// The interface models a TOOL-USE CONVERSATION, not a single completion. The
// harness sends a system prompt, a message history, and a tool catalogue; the
// model answers with text and/or tool calls; the harness executes what it
// permits, appends the outcomes, and asks again. That loop is what lets one
// agent create a board, look inside it, and then fill it — none of which a
// single forced-tool call can express.
//
// Three implementations exist: Anthropic, Gemini, and a Scripted provider for
// deterministic tests. The entire pipeline is testable offline, with no key.
package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ToolDef is a capability offered to the model for one turn.
type ToolDef struct {
	Name        string
	Description string
	// Schema is JSON Schema for the tool's input. Each adapter narrows it to
	// whatever dialect its provider accepts.
	Schema map[string]any
}

// ToolCall is the model asking to use a tool.
type ToolCall struct {
	// ID correlates the call with its outcome. Providers that do not supply
	// one (Gemini) get a synthesized id, so the harness sees one shape.
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolOutcome is what the harness sends back for a call.
type ToolOutcome struct {
	CallID string
	Name   string
	// Content is the observation, already normalized and size-bounded by the
	// tool gateway. Never a raw dump.
	Content string
	IsError bool
}

// Role values for Message.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn of the conversation.
type Message struct {
	Role string
	Text string
	// Calls are set on assistant turns; Outcomes on the user turn that answers
	// them. A turn carries one or the other, never both.
	Calls    []ToolCall
	Outcomes []ToolOutcome
}

// Request is one inference call.
type Request struct {
	// System is the stable, cacheable prefix: rules that do not vary between
	// turns. Volatile content belongs in Messages, after it, so a multi-step
	// run reads the cached prefix instead of rewriting it.
	System   string
	Messages []Message
	Tools    []ToolDef
	// ForceTool, when set, obliges the model to call that specific tool. Used
	// for one-shot structured extraction; left empty for agentic turns so the
	// model can choose, answer, or stop.
	ForceTool string
	MaxTokens int
	// Label identifies the call site in telemetry and in fixture lookup.
	Label string
}

// Response is a completed call.
type Response struct {
	Text       string
	Calls      []ToolCall
	StopReason string
	Usage      Usage
}

// Usage is spend for one call, in provider-reported units.
type Usage struct {
	InputTokens  int     `bson:"inputTokens"  json:"inputTokens"`
	OutputTokens int     `bson:"outputTokens" json:"outputTokens"`
	CachedTokens int     `bson:"cachedTokens" json:"cachedTokens"`
	CostUSD      float64 `bson:"costUsd"      json:"costUsd"`
	Calls        int     `bson:"calls"        json:"calls"`
}

// Add accumulates one provider call into a running total.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CachedTokens += o.CachedTokens
	u.CostUSD += o.CostUSD
	u.Calls++
}

// Provider is the model gateway. Implementations must not perform
// authorization, commit side effects, or interpret the harness's domain — they
// translate a Request into a Response and account for spend.
type Provider interface {
	Name() string
	Model() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Provider-level sentinels. Callers map these onto run outcomes: ErrRefused is
// a content outcome; ErrUnavailable is infrastructure and retryable.
var (
	// ErrRefused means the provider's safety classifiers declined. It arrives
	// on a successful HTTP response, so it must be checked before reading
	// content — never as an exception.
	ErrRefused = errors.New("the model declined this request")
	// ErrNoOutput means a forced call produced no tool use.
	ErrNoOutput = errors.New("the model returned no structured output")
	// ErrUnavailable means the provider is unreachable, rate-limited, or
	// erroring. Retryable.
	ErrUnavailable = errors.New("the model provider is unavailable")
	// ErrNotConfigured means no provider is wired on this deployment.
	ErrNotConfigured = errors.New("no model provider configured")
)

// ---------------------------------------------------------------------------
// Pricing
// ---------------------------------------------------------------------------

// Price is a model's per-million-token rates in USD.
type Price struct {
	InputPer1M  float64
	OutputPer1M float64
	// CachedPer1M is the cache-read rate, roughly a tenth of input.
	CachedPer1M float64
}

// prices exist so the cost ledger reconciles against the provider invoice. A
// model absent from the table reports zero cost rather than an invented number
// — RegisterPrice is how a deployment supplies rates it is actually billed.
// Rates are USD per million tokens, paid tier, standard inference.
// Anthropic: docs.anthropic.com/en/docs/about-claude/pricing.
// Google: ai.google.dev/gemini-api/docs/pricing — verified 2026-07-26.
//
// Two tiering rules, and they pull in opposite directions:
//
//   - PROMPT SIZE (≤200k vs >200k): take the HIGHER rate. A cap exists to stop
//     runaway cost, so it must not under-count; binding early is the safe way to
//     be wrong.
//   - MODALITY (text vs audio): take the TEXT rate. This harness sends text and
//     only text, so the audio rate is not a conservative estimate — it is the
//     wrong number, over-stating input by 3x on Flash and making the meter the
//     user reads plainly false.
var prices = map[string]Price{
	"claude-opus-5":    {InputPer1M: 5, OutputPer1M: 25, CachedPer1M: 0.5},
	"claude-opus-4-8":  {InputPer1M: 5, OutputPer1M: 25, CachedPer1M: 0.5},
	"claude-sonnet-5":  {InputPer1M: 3, OutputPer1M: 15, CachedPer1M: 0.3},
	"claude-haiku-4-5": {InputPer1M: 1, OutputPer1M: 5, CachedPer1M: 0.1},

	"gemini-3.6-flash":      {InputPer1M: 1.50, OutputPer1M: 7.50},
	"gemini-3.5-flash-lite": {InputPer1M: 0.30, OutputPer1M: 2.50},
	"gemini-3.5-flash":      {InputPer1M: 1.50, OutputPer1M: 9.00},
	"gemini-3.1-flash-lite": {InputPer1M: 0.25, OutputPer1M: 1.50},
	"gemini-3.1-pro":        {InputPer1M: 4.00, OutputPer1M: 18.00},
	"gemini-2.5-pro":        {InputPer1M: 2.50, OutputPer1M: 15.00},
	"gemini-2.5-flash-lite": {InputPer1M: 0.10, OutputPer1M: 0.40},
	"gemini-2.5-flash":      {InputPer1M: 0.30, OutputPer1M: 2.50},
}

// lookup resolves a price by exact id, then by longest matching prefix, so a
// dated or suffixed id ("gemini-2.5-flash-002") prices as its family instead of
// falling through to zero. Longest-first matters: "gemini-2.5-flash-lite" must
// not be priced as "gemini-2.5-flash".
func lookup(model string) (Price, bool) {
	if p, ok := prices[model]; ok {
		return p, true
	}
	best, bestLen, found := Price{}, 0, false
	for id, p := range prices {
		if len(id) > bestLen && strings.HasPrefix(model, id) {
			best, bestLen, found = p, len(id), true
		}
	}
	return best, found
}

// RegisterPrice records the rates for a model at runtime.
//
// It exists because a spend cap that silently never binds is worse than no cap
// at all: an unpriced model reports zero cost, so a daily ceiling would never
// trigger.
func RegisterPrice(model string, p Price) {
	if model == "" {
		return
	}
	if p.CachedPer1M == 0 {
		p.CachedPer1M = p.InputPer1M / 10
	}
	prices[model] = p
}

// Priced reports whether a model's rates are known.
func Priced(model string) bool { _, ok := lookup(model); return ok }

// CostUSD prices one call. Unknown models return (0, false), which the caller
// logs once rather than silently reporting a fabricated figure.
func CostUSD(model string, u Usage) (float64, bool) {
	p, ok := lookup(model)
	if !ok {
		return 0, false
	}
	const perMillion = 1_000_000.0
	return float64(u.InputTokens)/perMillion*p.InputPer1M +
		float64(u.OutputTokens)/perMillion*p.OutputPer1M +
		float64(u.CachedTokens)/perMillion*p.CachedPer1M, true
}
