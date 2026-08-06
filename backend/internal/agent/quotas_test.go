package agent

import (
	"strings"
	"testing"
)

// Every limit a run is held to, checked as a set.
//
// These used to be seven loose counters compared against constants at whichever
// call site incremented them. The failure that shape invites is not a wrong
// number — it is a limit that is declared and never enforced, or enforced with
// a message the model cannot act on, and neither is visible from any one place.
func TestQuotas(t *testing.T) {
	q := newQuotas()
	all := map[string]*counter{
		"labels":      &q.labels,
		"images":      &q.images,
		"urls":        &q.urls,
		"connections": &q.connections,
		"comments":    &q.comments,
		"questions":   &q.questions,
	}

	for name, c := range all {
		if c.max <= 0 {
			t.Errorf("%s has a max of %d, which forbids the capability outright", name, c.max)
		}
		if c.refusal == "" {
			t.Errorf("%s refuses with an empty message", name)
			continue
		}
		// The refusal has to convey the limit, so the model learns the shape of
		// the rule rather than only that it was broken. A once-per-run limit
		// says so in words ("you have already…"); anything countable
		// substitutes the number.
		if c.max > 1 && !strings.Contains(c.refusal, "%d") {
			t.Errorf("%s allows %d but its refusal never says so: %q", name, c.max, c.refusal)
		}
		if c.max == 1 && !strings.Contains(c.refusal, "already") {
			t.Errorf("%s is once-per-run but its refusal does not say so: %q", name, c.refusal)
		}
		// And it points somewhere. A bare "no" gets retried; a model told what
		// to do instead moves on, which is the difference between a run that
		// finishes and one that burns its steps repeating itself.
		if !strings.ContainsAny(c.refusal, ";—") {
			t.Errorf("%s refusal gives no alternative: %q", name, c.refusal)
		}
	}
}

func TestCounter_TakeStopsAtTheLimit(t *testing.T) {
	c := counter{max: 2, refusal: "no more than %d"}
	if err := c.take(); err != nil {
		t.Fatalf("first take: %v", err)
	}
	if err := c.take(); err != nil {
		t.Fatalf("second take: %v", err)
	}
	err := c.take()
	if err == nil {
		t.Fatal("the third take succeeded against a limit of 2")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("refusal = %q, want it to name the limit", err)
	}
	// A refused take must not consume anything, or repeated refusals would
	// silently move the counter and the message would drift from the truth.
	if c.used != 2 {
		t.Errorf("used = %d after a refused take, want 2", c.used)
	}
}

// A run starts with nothing spent. Obvious, and worth pinning: the counters are
// value types inside a struct that is copied at construction, and a shared
// counter would silently apply one run's spend to the next.
func TestQuotas_StartEmpty(t *testing.T) {
	a, b := newQuotas(), newQuotas()
	for i := 0; i < a.labels.max; i++ {
		if err := a.labels.take(); err != nil {
			t.Fatalf("take %d: %v", i, err)
		}
	}
	if b.labels.used != 0 {
		t.Errorf("a second run began with %d labels already spent", b.labels.used)
	}
}
