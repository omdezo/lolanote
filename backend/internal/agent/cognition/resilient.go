package cognition

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Resilience for the model plane.
//
// A run stages work across several turns. Losing turn five to a 502 throws away
// every staged action AND the money already spent producing them — the user
// waits, pays, and gets nothing. Retrying one call is far cheaper than redoing
// a run, so the retry lives here rather than at the run level.

// Fallback wraps an ordered list of providers. The first is used for every
// call; the rest exist for when it is unreachable.
type Fallback struct {
	providers []Provider
	// attempts is how many times ONE provider is retried before moving on.
	attempts int
	// backoff is the pause after the first failure; it doubles thereafter.
	backoff time.Duration
	// sleep is injectable so tests do not actually wait.
	sleep func(time.Duration)
	// lastUsed records which provider answered, so a quality regression can be
	// explained later. Silent substitution is the thing to avoid: a run that
	// quietly ran on a weaker model and produced a worse plan is impossible to
	// diagnose after the fact.
	lastUsed Provider
}

// NewFallback builds a resilient provider. With a single provider it still
// retries, which is the common case worth having.
func NewFallback(providers ...Provider) *Fallback {
	live := make([]Provider, 0, len(providers))
	for _, p := range providers {
		if p != nil {
			live = append(live, p)
		}
	}
	return &Fallback{
		providers: live,
		attempts:  3,
		backoff:   400 * time.Millisecond,
		sleep:     time.Sleep,
	}
}

func (f *Fallback) Name() string {
	if len(f.providers) == 0 {
		return "none"
	}
	return f.providers[0].Name()
}

// Model reports the model that actually answered last, falling back to the
// primary's before any call has been made.
func (f *Fallback) Model() string {
	if f.lastUsed != nil {
		return f.lastUsed.Model()
	}
	if len(f.providers) == 0 {
		return ""
	}
	return f.providers[0].Model()
}

func (f *Fallback) Complete(ctx context.Context, req Request) (*Response, error) {
	if len(f.providers) == 0 {
		return nil, ErrNotConfigured
	}
	var lastErr error
	for _, p := range f.providers {
		wait := f.backoff
		for attempt := 0; attempt < f.attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			resp, err := p.Complete(ctx, req)
			if err == nil {
				f.lastUsed = p
				return resp, nil
			}
			lastErr = err
			// Only infrastructure failures are worth retrying. A refusal is a
			// content decision: asking the same provider the same question
			// again wastes tokens and produces the same answer.
			if !errors.Is(err, ErrUnavailable) {
				return nil, err
			}
			if attempt < f.attempts-1 {
				f.sleep(wait)
				wait *= 2
			}
		}
	}
	return nil, fmt.Errorf("%w: every provider failed (%v)", ErrUnavailable, lastErr)
}

var _ Provider = (*Fallback)(nil)
