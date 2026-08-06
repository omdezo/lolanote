package cognition

import (
	"context"
	"testing"
	"time"
)

// The deploy log entry this exists for reads "Gemini key rotation". A rotated
// key made every run fail while readiness reported healthy, so the load
// balancer kept sending traffic and the only symptom anyone saw was runs
// failing one at a time.

func TestHealth_ReportsAProviderThatWillNotAnswer(t *testing.T) {
	p := &stub{name: "dead", fails: 99, err: ErrUnavailable}
	h := NewHealth(p)
	if h.Healthy(context.Background()) {
		t.Fatal("a provider that answers nothing is reported healthy — this is the readiness lie itself")
	}
	if p.calls == 0 {
		t.Fatal("the provider was never actually asked, so the answer means nothing")
	}
}

// The caching is not an optimisation. Readiness is polled every few seconds by
// every orchestrator that exists, and an uncached probe against a paid API is a
// bill rather than a health check.
func TestHealth_DoesNotProbeOnEveryCall(t *testing.T) {
	p := &stub{name: "live"}
	h := NewHealth(p)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if !h.Healthy(ctx) {
			t.Fatalf("call %d reported unhealthy against a working provider", i)
		}
	}
	if p.calls != 1 {
		t.Fatalf("%d provider calls for 50 readiness polls — the probe is a spend path", p.calls)
	}
	if h.Probes() != 1 {
		t.Fatalf("probes = %d, want 1", h.Probes())
	}
}

// And it does eventually ask again, or a provider that came back would stay
// marked dead until the next deploy.
func TestHealth_ReprobesAfterTheWindow(t *testing.T) {
	p := &stub{name: "flaky", fails: 1, err: ErrUnavailable}
	h := NewHealth(p)
	now := time.Now()
	h.now = func() time.Time { return now }

	if h.Healthy(context.Background()) {
		t.Fatal("the first probe failed and was reported healthy")
	}
	now = now.Add(healthTTL + time.Second)
	if !h.Healthy(context.Background()) {
		t.Fatal("the provider recovered and readiness is still serving the cached failure")
	}
}

// A safety refusal is the provider working. Treating it as an outage would take
// a healthy deployment out of rotation over a one-token prompt.
func TestHealth_ARefusalIsNotAnOutage(t *testing.T) {
	p := &stub{name: "prim", fails: 99, err: ErrRefused}
	if !NewHealth(p).Healthy(context.Background()) {
		t.Fatal("a refusal was read as the provider being down")
	}
}

// No provider configured is not a degraded deployment — it is a deployment
// without the agent, which readiness must not fail on.
func TestHealth_NoProviderIsNotAFailure(t *testing.T) {
	if err := NewHealth(nil).Err(context.Background()); err != nil {
		t.Fatalf("an agent-less deployment reports %v, and would never become ready", err)
	}
}
