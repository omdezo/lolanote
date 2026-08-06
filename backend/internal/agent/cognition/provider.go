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

// IsDocument reports whether a part is a PDF rather than an image. The two
// travel the same way to Gemini (inlineData) but not to Anthropic, which has a
// distinct document block — so the harness carries the distinction rather than
// making each adapter re-derive it from the media type.
func (p ImagePart) IsDocument() bool { return p.MediaType == "application/pdf" }

// ImagePart is one image OR document attached to a turn.
//
// Images are carried as bytes rather than URLs: the provider must not be handed
// a link into this deployment's storage, and a signed URL would expire between
// staging and the retry that follows a transient failure.
type ImagePart struct {
	MediaType string // "image/png", "image/jpeg", "image/webp", "image/gif"
	Data      []byte
}

// Message is one turn of the conversation.
type Message struct {
	Role string
	Text string
	// Calls are set on assistant turns; Outcomes on the user turn that answers
	// them. A turn carries one or the other, never both.
	Calls    []ToolCall
	Outcomes []ToolOutcome
	// Images ride on a user turn, attached only when the run explicitly asked
	// to look at something. Never by default: a board of forty screenshots
	// would exhaust the context and the budget in one turn.
	Images []ImagePart
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
	// Tier is which model policy this call site wants — fast for reading and
	// classifying, strong for authoring and judging. Empty means strong, so a
	// caller written before routing existed keeps exactly its old behaviour.
	//
	// Label was already set at every call site and read by nothing. Tier is the
	// field a Router can actually dispatch on; the two travel together because
	// "which policy" and "which phase" are the same question asked of cost and of
	// capability.
	Tier Tier
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
	InputTokens  int `bson:"inputTokens"  json:"inputTokens"`
	OutputTokens int `bson:"outputTokens" json:"outputTokens"`
	CachedTokens int `bson:"cachedTokens" json:"cachedTokens"`
	// CacheWriteTokens is the prefix this call PUT into the cache, billed above
	// the base input rate and reported by the API separately from both of the
	// fields above. Counted because it was not: see Price.CacheWritePer1M.
	CacheWriteTokens int     `bson:"cacheWriteTokens,omitempty" json:"cacheWriteTokens,omitempty"`
	CostUSD          float64 `bson:"costUsd"      json:"costUsd"`
	// Calls is how many times a provider was actually asked. Every provider sets
	// it to 1 on the Usage it returns; only the accumulators below carry more.
	Calls int `bson:"calls" json:"calls"`
	// Phases is spend broken out by the tier and call site that produced it,
	// keyed "<tier>" and "<tier>:<phase>".
	//
	// One total for a whole run cannot answer the question the review turn
	// raises every time it fires — is a second opinion worth what it costs — and
	// that question is the whole justification for paying for one. Attributed by
	// the Router at the single seam every call passes through, so a call site
	// that forgets to account for itself is not expressible.
	Phases map[string]Usage `bson:"phases,omitempty" json:"phases,omitempty"`
}

// phaseOf reduces a Label to the phase it belongs to. Labels carry a step
// number ("plan.step.7") because they also serve fixture lookup; a per-step
// breakdown is noise, and a map that grows with the step budget is a map nobody
// aggregates.
func phaseOf(label string) string {
	if i := strings.IndexByte(label, '.'); i > 0 {
		return label[:i]
	}
	return label
}

// attribute records this call against its tier and phase. Called exactly once,
// by the Router, on the response it is about to hand back.
func (u *Usage) attribute(tier Tier, label string) {
	if u.Phases == nil {
		u.Phases = map[string]Usage{}
	}
	self := Usage{
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CachedTokens: u.CachedTokens, CacheWriteTokens: u.CacheWriteTokens,
		CostUSD: u.CostUSD, Calls: u.Calls,
	}
	if self.Calls == 0 {
		self.Calls = 1
	}
	keys := []string{string(tier)}
	if phase := phaseOf(label); phase != "" {
		keys = append(keys, string(tier)+":"+phase)
	}
	for _, k := range keys {
		cur := u.Phases[k]
		cur.InputTokens += self.InputTokens
		cur.OutputTokens += self.OutputTokens
		cur.CachedTokens += self.CachedTokens
		cur.CacheWriteTokens += self.CacheWriteTokens
		cur.CostUSD += self.CostUSD
		cur.Calls += self.Calls
		u.Phases[k] = cur
	}
}

// Add folds one Usage into a running total — a single call, or another total.
//
// Calls was `u.Calls++`, which counted the Add rather than the call. The planner
// adds once per turn and the service then adds the planner's whole total exactly
// once, so every run in the database reported calls:1 no matter how many turns
// it took. That is the one number that would have made the step starvation
// visible while it was happening, and it read as a constant.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CachedTokens += o.CachedTokens
	u.CacheWriteTokens += o.CacheWriteTokens
	u.CostUSD += o.CostUSD
	u.Calls += o.Calls
	// The per-phase breakdown folds the same way the totals do, or a run's
	// phases would report only whatever the last Add happened to carry.
	for k, v := range o.Phases {
		if u.Phases == nil {
			u.Phases = map[string]Usage{}
		}
		cur := u.Phases[k]
		cur.InputTokens += v.InputTokens
		cur.OutputTokens += v.OutputTokens
		cur.CachedTokens += v.CachedTokens
		cur.CostUSD += v.CostUSD
		cur.Calls += v.Calls
		u.Phases[k] = cur
	}
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
	// CacheWritePer1M is what it costs to PUT the prefix in the cache, and it is
	// ABOVE the base input rate — 1.25x on Anthropic's five-minute TTL.
	//
	// SC2: this term did not exist, and the ledger was silently short by it on
	// every run. The API reports input_tokens exclusive of both cache reads and
	// cache writes, so the first turn of every run wrote the ~10.8k-token prefix
	// and recorded it as $0. That figure is what MaxCostUSD is checked against,
	// what the daily cap sums, and what the audit view shows the person — all
	// three low, and low by MORE precisely when the cache is doing the most
	// work, which is the opposite of the direction an operator would guess.
	//
	// It also made SC1 unmeasurable: adding cache breakpoints raises real
	// first-turn spend while a meter blind to cache writes shows it falling.
	CacheWritePer1M float64
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
	"claude-opus-5":    {InputPer1M: 5, OutputPer1M: 25, CachedPer1M: 0.5, CacheWritePer1M: 6.25},
	"claude-opus-4-8":  {InputPer1M: 5, OutputPer1M: 25, CachedPer1M: 0.5, CacheWritePer1M: 6.25},
	"claude-sonnet-5":  {InputPer1M: 3, OutputPer1M: 15, CachedPer1M: 0.3, CacheWritePer1M: 3.75},
	"claude-haiku-4-5": {InputPer1M: 1, OutputPer1M: 5, CachedPer1M: 0.1, CacheWritePer1M: 1.25},

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
		float64(u.CachedTokens)/perMillion*p.CachedPer1M +
		float64(u.CacheWriteTokens)/perMillion*p.cacheWriteRate(), true
}

// cacheWriteRate falls back to the documented 1.25x multiple of the input rate
// when a price entry does not state one.
//
// Defaulting rather than zeroing, because zero is exactly the bug: a fourth
// term that quietly prices at nothing is indistinguishable from the term not
// existing, which is the state this replaced. A provider that does not report
// cache writes contributes zero tokens and pays nothing either way.
func (p Price) cacheWriteRate() float64 {
	if p.CacheWritePer1M > 0 {
		return p.CacheWritePer1M
	}
	return p.InputPer1M * 1.25
}
