package cognition

import (
	"context"
	"testing"
)

// CG8. `Request.Label` was set at every call site and read by nothing, so every
// turn — the ambiguity classification, the drift scan, the summary synthesis,
// the reflection after a revert — paid authoring rates. The converse bit harder:
// the shipping model was chosen for cost, and the two probes that stayed flaky
// were both judgement probes.
//
// The claim this test defends is the routing itself: a request annotated fast
// reaches the fast policy and a request annotated strong reaches the strong one,
// with nothing above the Provider interface changing.
func TestRouter_DispatchesOnTier(t *testing.T) {
	fast := &stub{name: "fast"}
	strong := &stub{name: "strong"}
	r := NewRouter(fast, strong)

	got, err := r.Complete(context.Background(), Request{Tier: TierFast, Label: "plan.step.0"})
	if err != nil || got.Text != "fast" {
		t.Fatalf("a fast turn was answered by %q (%v)", got.Text, err)
	}
	got, err = r.Complete(context.Background(), Request{Tier: TierStrong, Label: "review.second_opinion"})
	if err != nil || got.Text != "strong" {
		t.Fatalf("a judgement turn was answered by %q (%v)", got.Text, err)
	}
	if fast.calls != 1 || strong.calls != 1 {
		t.Fatalf("fast=%d strong=%d; each tier must be asked exactly once", fast.calls, strong.calls)
	}
}

// An UNSET tier is strong, not fast. Every call site predates routing and was
// written against one strong model, so downgrading by default would silently
// make every unconverted path worse — the failure mode a routing change is most
// likely to ship with and least likely to notice.
func TestRouter_UnsetTierIsStrong(t *testing.T) {
	fast := &stub{name: "fast"}
	strong := &stub{name: "strong"}
	r := NewRouter(fast, strong)
	got, _ := r.Complete(context.Background(), Request{})
	if got.Text != "strong" {
		t.Fatalf("an un-annotated call routed to %q; it must keep the behaviour it had", got.Text)
	}
}

// Silent substitution is the regression routing introduces. A run that quietly
// executed on the cheap tier and produced a worse plan is impossible to diagnose
// afterwards unless the provenance is recorded — the same property Fallback.Model
// exists for.
func TestRouter_ModelNamesWhoActuallyAnswered(t *testing.T) {
	r := NewRouter(&stub{name: "fast"}, &stub{name: "strong"})
	if r.Model() != "strong-model" {
		t.Fatalf("before any call Model() = %q, want the strong policy's", r.Model())
	}
	_, _ = r.Complete(context.Background(), Request{Tier: TierFast})
	if r.Model() != "fast-model" {
		t.Fatalf("after a fast turn Model() = %q; it must name what answered", r.Model())
	}
}

// A deployment that configures one model must behave exactly as it did. The
// degenerate case has to be the identity, or routing ships as a quality
// regression on every deployment that did not opt in.
func TestRouter_OneModelAnswersEverything(t *testing.T) {
	only := &stub{name: "only"}
	r := NewRouter(nil, only)
	for _, tier := range []Tier{TierFast, TierStrong, ""} {
		if got, _ := r.Complete(context.Background(), Request{Tier: tier}); got.Text != "only" {
			t.Fatalf("tier %q was answered by %q", tier, got.Text)
		}
	}
	if only.calls != 3 {
		t.Fatalf("the single provider was asked %d times, want 3", only.calls)
	}
}

// "Is the review turn worth what it costs" is a question the harness made
// askable and never answered, because the only number kept was one total for the
// whole run. Attribution happens at the Router, the single seam every call passes
// through, so a call site that forgets to account for itself is not expressible.
func TestRouter_AttributesSpendToItsPhase(t *testing.T) {
	r := NewRouter(&priced{name: "fast"}, &priced{name: "strong"})
	a, _ := r.Complete(context.Background(), Request{Tier: TierFast, Label: "plan.step.3"})
	b, _ := r.Complete(context.Background(), Request{Tier: TierStrong, Label: "review.second_opinion"})

	var total Usage
	total.Add(a.Usage)
	total.Add(b.Usage)

	if total.Phases["fast:plan"].Calls != 1 {
		t.Errorf("the planning phase recorded %+v", total.Phases["fast:plan"])
	}
	if total.Phases["strong:review"].CostUSD != 0.5 {
		t.Errorf("the review phase recorded %+v; its cost is the thing that makes it "+
			"answerable whether a second opinion is worth buying", total.Phases["strong:review"])
	}
	if total.Phases["fast"].Calls != 1 || total.Phases["strong"].Calls != 1 {
		t.Errorf("per-tier totals are wrong: %+v", total.Phases)
	}
	if total.CostUSD != 1.0 {
		t.Errorf("the grand total is %v; the breakdown must not change it", total.CostUSD)
	}
}

// priced is a stub that reports spend, so the phase ledger has something to
// accumulate.
type priced struct {
	name  string
	calls int
}

func (p *priced) Name() string  { return p.name }
func (p *priced) Model() string { return p.name + "-model" }
func (p *priced) Complete(context.Context, Request) (*Response, error) {
	p.calls++
	return &Response{Text: p.name, Usage: Usage{Calls: 1, InputTokens: 100, CostUSD: 0.5}}, nil
}
