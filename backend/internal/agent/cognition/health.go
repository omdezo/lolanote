package cognition

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Whether the model plane can actually answer right now.
//
// Enablement was decided once, at boot, from the presence of an API key, and
// never re-checked. So a rotated, expired or rate-limited key produced a
// deployment that reported itself ready while every run failed — the load
// balancer kept sending traffic and the only symptom was users watching runs
// fail one at a time, with nothing anywhere saying why. That exact outage has
// already been lived through here once: "Gemini key rotation" is an entry in a
// deploy log, not a hypothetical.
//
// The probe is one minimal call through the ordinary Provider interface, which
// is the only thing that proves the whole chain — key, quota, network, model
// name — rather than proving a string is non-empty.

// healthTTL is how long one probe's answer stands. A readiness endpoint is
// polled every few seconds by every orchestrator that exists; an uncached probe
// against a paid API is a bill, so the cache is not an optimisation but the
// thing that makes the probe safe to have at all.
const healthTTL = 60 * time.Second

// Health is a cached liveness probe over a Provider.
type Health struct {
	provider Provider
	ttl      time.Duration
	// now is injectable so a test can age the cache without sleeping.
	now func() time.Time

	mu      sync.Mutex
	checked time.Time
	err     error
	probes  int
}

// NewHealth wraps a provider. A nil provider yields a nil Health, whose Err
// reports the feature as simply absent rather than broken — a deployment with
// no agent is not a degraded deployment.
func NewHealth(p Provider) *Health {
	if p == nil {
		return nil
	}
	return &Health{provider: p, ttl: healthTTL, now: time.Now}
}

// Err returns nil when the provider answered within the cache window, or the
// failure it gave. Failures are cached too: a provider that is down will be
// asked again in a minute, not on every poll.
func (h *Health) Err(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.checked.IsZero() && h.now().Sub(h.checked) < h.ttl {
		return h.err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// One token. The answer is discarded — what is being tested is whether the
	// provider will talk to us at all, and the cheapest possible question is
	// the honest form of that test.
	_, err := h.provider.Complete(probeCtx, Request{
		Messages:  []Message{{Role: RoleUser, Text: "ok"}},
		MaxTokens: 1,
		Label:     "health",
	})
	// A refusal is the provider working. The classifier declining a one-token
	// prompt would be strange, but treating it as an outage would take a
	// healthy deployment out of the load balancer.
	if errors.Is(err, ErrRefused) {
		err = nil
	}
	h.checked, h.err, h.probes = h.now(), err, h.probes+1
	return h.err
}

// Healthy is Err reduced to a boolean, for surfaces that only report a state.
func (h *Health) Healthy(ctx context.Context) bool { return h.Err(ctx) == nil }

// Probes is how many times the provider has actually been asked. Exists so a
// test can prove the cache is real rather than assuming it.
func (h *Health) Probes() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.probes
}
