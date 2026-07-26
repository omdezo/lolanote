package cognition

import "testing"

// A spend cap that never binds is worse than no cap. These assert the two ways
// that happens: an unknown model pricing at zero, and a suffixed model id
// falling through to the wrong family.
func TestPricing_ResolvesFamiliesAndRefusesToGuess(t *testing.T) {
	cases := []struct {
		model     string
		wantKnown bool
		wantIn    float64
	}{
		{"gemini-2.5-flash", true, 0.30},
		{"gemini-2.5-flash-002", true, 0.30},  // dated suffix → same family
		{"gemini-2.5-flash-lite", true, 0.10}, // longest prefix wins
		{"gemini-2.5-flash-lite-preview", true, 0.10},
		{"gemini-3.1-pro-preview", true, 4.00},
		{"claude-opus-5", true, 5},
		{"some-model-nobody-priced", false, 0},
	}
	for _, c := range cases {
		p, ok := lookup(c.model)
		if ok != c.wantKnown {
			t.Errorf("%s: known=%v, want %v", c.model, ok, c.wantKnown)
			continue
		}
		if ok && p.InputPer1M != c.wantIn {
			t.Errorf("%s: input rate %.2f, want %.2f", c.model, p.InputPer1M, c.wantIn)
		}
	}

	// An unpriced model must report (0, false) so the caller can warn, never a
	// fabricated figure that a cap would then trust.
	if cost, ok := CostUSD("some-model-nobody-priced", Usage{InputTokens: 1e6}); ok || cost != 0 {
		t.Errorf("unpriced model returned (%v, %v); want (0, false)", cost, ok)
	}
}
