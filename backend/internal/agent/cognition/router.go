package cognition

import (
	"context"
	"sync"
)

// Model routing: one deployment, two policies.
//
// Every turn used to pay authoring rates. That is wrong in both directions and
// the second one bites harder:
//
//   - Turns that only READ the board — the ambiguity classification, the drift
//     scan, the finish-summary synthesis, a reflection after a revert — bought
//     the strongest model available to do work a cheap one does identically.
//   - The shipping model was chosen for cost, and the probes that stayed flaky
//     (restructuring while answering a question; follow-up scope) are JUDGEMENT
//     probes. Neither is a prompt problem, and both were being answered with
//     another escalation of prompt wording.
//
// `Request.Label` already existed and named the call site; nothing read it.
// Tier is the field an adapter can actually dispatch on, and Router is the
// dispatcher — sitting behind the same Provider interface, so retry, backoff,
// the `lastUsed` provenance recording and every caller above compose unchanged.

// Tier is which policy a call site wants. Deliberately two values: a knob with
// five settings is a knob nobody sets correctly, and the honest distinction is
// "does this turn have to JUDGE something".
type Tier string

const (
	// TierFast is for reading, classifying, summarising and reflecting — turns
	// whose answer is transcription rather than judgement.
	TierFast Tier = "fast"
	// TierStrong is for authoring and for judging: staging structure, the review
	// turn, the independent second opinion. The default when nothing says.
	TierStrong Tier = "strong"
)

// Or resolves an unset tier. Unset means STRONG, not fast: every call site that
// predates routing was written against one strong model, so an un-annotated
// caller must keep exactly the behaviour it had. Downgrading by default would
// silently make every unconverted path worse, which is the failure mode a
// routing change is most likely to ship with and least likely to notice.
func (t Tier) Or() Tier {
	if t == TierFast {
		return TierFast
	}
	return TierStrong
}

// Router dispatches one Request to the provider its Tier asks for.
//
// It holds Providers, not clients: in production each side is a *Fallback, so a
// tier that is unreachable retries and then falls through exactly as the single
// provider did. In tests each side is a *Scripted, which is what makes "the
// review turn hit the strong policy" a provable claim rather than a comment.
type Router struct {
	fast   Provider
	strong Provider

	mu sync.Mutex
	// lastUsed is the provider that answered most recently, so Model() reports
	// what actually ran. A run that quietly executed on the cheap tier and
	// produced a worse plan is impossible to diagnose after the fact, and that is
	// the specific regression routing introduces if nobody records it.
	lastUsed Provider
}

// NewRouter builds a two-policy provider. A nil side falls back to the other, so
// a deployment that configures only one model gets that model for everything —
// the degenerate case, and the one an unchanged deployment must land in.
func NewRouter(fast, strong Provider) *Router {
	if fast == nil {
		fast = strong
	}
	if strong == nil {
		strong = fast
	}
	return &Router{fast: fast, strong: strong}
}

// For returns the provider a tier routes to.
func (r *Router) For(t Tier) Provider {
	if t.Or() == TierFast {
		return r.fast
	}
	return r.strong
}

// Name reports the routing policy rather than one vendor, because with two
// providers configured there is no single honest answer and "anthropic" on a run
// that used Gemini for half its turns is worse than saying so.
func (r *Router) Name() string {
	if r.fast == r.strong && r.strong != nil {
		return r.strong.Name()
	}
	return "router"
}

// Model reports the model that actually answered last, falling back to the
// strong policy's before any call has been made — the same contract Fallback
// has, for the same reason.
func (r *Router) Model() string {
	r.mu.Lock()
	last := r.lastUsed
	r.mu.Unlock()
	if last != nil {
		return last.Model()
	}
	if r.strong != nil {
		return r.strong.Model()
	}
	return ""
}

// Complete dispatches on the request's tier.
func (r *Router) Complete(ctx context.Context, req Request) (*Response, error) {
	p := r.For(req.Tier)
	if p == nil {
		return nil, ErrNotConfigured
	}
	resp, err := p.Complete(ctx, req)
	if err == nil {
		r.mu.Lock()
		r.lastUsed = p
		r.mu.Unlock()
		if resp != nil {
			// Spend is attributed to the PHASE that produced it, here, at the one
			// seam every call passes through. "Is the review turn worth what it
			// costs" is a question the harness made askable and never answered,
			// because the only number kept was one total for the whole run.
			resp.Usage.attribute(req.Tier.Or(), req.Label)
		}
	}
	return resp, err
}

var _ Provider = (*Router)(nil)
