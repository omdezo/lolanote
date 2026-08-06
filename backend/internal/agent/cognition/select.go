package cognition

import (
	"strings"
	"time"
)

// Choosing a provider from configuration.
//
// The harness treats the model as a replaceable component, so this is the only
// place that knows which vendors exist. Everything upstream — admission,
// context compilation, validation, verification, revert — is identical
// whichever one answers.

// Options is the provider-selection configuration.
type Options struct {
	// Provider is "anthropic", "gemini", or "" to infer from the keys present.
	Provider string
	// Model overrides the provider's default. Empty means the provider decides.
	Model string
	// FastModel / StrongModel are the two routing policies: cheap for reads,
	// classification and summary synthesis; strong for authoring and judging.
	//
	// Both optional and both defaulting to Model, so a deployment that sets
	// neither gets exactly one provider object and exactly today's behaviour —
	// the degenerate case has to be the identity, or a routing change ships as a
	// silent quality regression on every deployment that did not opt in.
	FastModel   string
	StrongModel string

	AnthropicAPIKey string
	GeminiAPIKey    string

	// CallTimeout bounds ONE model call. It belongs to the caller because only
	// the caller knows how long the whole run may take: a flat three minutes
	// against three retries is nine minutes of hanging, which is longer than the
	// eight-minute run deadline — so one wedged call could consume the entire
	// run and the person saw a budget message for an outage. Zero means the
	// default below.
	CallTimeout time.Duration
}

// defaultCallTimeout is the fallback when no caller states a budget. Deliberately
// shorter than any plausible run deadline so the flat-timeout hazard cannot
// return by omission.
const defaultCallTimeout = 90 * time.Second

func (o Options) callTimeout() time.Duration {
	if o.CallTimeout <= 0 {
		return defaultCallTimeout
	}
	return o.CallTimeout
}

// New builds the configured Provider.
//
// With FAST and STRONG naming different models it returns a Router over two
// clients; with both empty or equal it returns the single client it always
// returned, because a Router whose two sides are the same object is a layer of
// indirection with no observable effect and one more thing to reason about when
// a run misbehaves.
//
// It returns ErrNotConfigured when the deployment has no key at all, which the
// caller reports as "the agent is not enabled here" rather than as a failure.
func New(opts Options) (Provider, error) {
	fast, strong := firstNonEmpty(opts.FastModel, opts.Model), firstNonEmpty(opts.StrongModel, opts.Model)
	if fast == strong {
		return newSingle(opts, opts.Model)
	}
	fastP, err := newSingle(opts, fast)
	if err != nil {
		return nil, err
	}
	strongP, err := newSingle(opts, strong)
	if err != nil {
		return nil, err
	}
	// Each side gets its own retry envelope, so a tier that is rate-limited
	// backs off without the other tier's turns inheriting the wait.
	return NewRouter(NewFallback(fastP), NewFallback(strongP)), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// newSingle builds one vendor client at a named model.
func newSingle(opts Options, model string) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Provider)) {
	case "anthropic":
		return NewAnthropic(opts.AnthropicAPIKey, model, opts.callTimeout())
	case "gemini", "google":
		return NewGemini(opts.GeminiAPIKey, model, opts.callTimeout())
	case "":
		// Inference, in preference order. Anthropic first because it is the
		// documented default for this deployment; Gemini is the alternative a
		// developer may have on hand.
		if opts.AnthropicAPIKey != "" {
			return NewAnthropic(opts.AnthropicAPIKey, model, opts.callTimeout())
		}
		if opts.GeminiAPIKey != "" {
			return NewGemini(opts.GeminiAPIKey, model, opts.callTimeout())
		}
		return nil, ErrNotConfigured
	default:
		return nil, ErrNotConfigured
	}
}
