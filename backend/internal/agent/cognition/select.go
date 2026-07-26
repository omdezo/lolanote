package cognition

import "strings"

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

	AnthropicAPIKey string
	GeminiAPIKey    string
}

// New builds the configured Provider. It returns ErrNotConfigured when the
// deployment has no key at all, which the caller reports as "the agent is not
// enabled here" rather than as a failure.
func New(opts Options) (Provider, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Provider)) {
	case "anthropic":
		return NewAnthropic(opts.AnthropicAPIKey, opts.Model)
	case "gemini", "google":
		return NewGemini(opts.GeminiAPIKey, opts.Model)
	case "":
		// Inference, in preference order. Anthropic first because it is the
		// documented default for this deployment; Gemini is the alternative a
		// developer may have on hand.
		if opts.AnthropicAPIKey != "" {
			return NewAnthropic(opts.AnthropicAPIKey, opts.Model)
		}
		if opts.GeminiAPIKey != "" {
			return NewGemini(opts.GeminiAPIKey, opts.Model)
		}
		return nil, ErrNotConfigured
	default:
		return nil, ErrNotConfigured
	}
}
