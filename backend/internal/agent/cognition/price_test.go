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

// SC2. The ledger had three terms and the invoice has four.
//
// `input_tokens` is reported EXCLUSIVE of both cache reads and cache writes, and
// `cache_creation_input_tokens` was never read anywhere in the codebase — so the
// first turn of every run wrote the ~10.8k-token cacheable prefix and booked it
// at zero. That number is what MaxCostUSD is checked against, what the daily cap
// sums, and what the audit view shows the person. All three were low, and low by
// more precisely when the cache was doing the most work.
func TestPricing_ACacheWriteIsBilledAboveTheInputRate(t *testing.T) {
	const million = 1_000_000

	// A cache write costs MORE than the same tokens sent uncached. A term that
	// priced at or below the input rate would be the bug wearing a hat.
	write, ok := CostUSD("claude-opus-5", Usage{CacheWriteTokens: million})
	if !ok {
		t.Fatal("claude-opus-5 is not priced")
	}
	plain, _ := CostUSD("claude-opus-5", Usage{InputTokens: million})
	if write <= plain {
		t.Fatalf("a cache write of 1M tokens costs %.4f, no more than the %.4f "+
			"the same tokens cost uncached — the fourth term is still zero", write, plain)
	}
	if read, _ := CostUSD("claude-opus-5", Usage{CachedTokens: million}); write <= read {
		t.Fatalf("a cache write (%.4f) is not dearer than a cache read (%.4f)", write, read)
	}

	// The whole point of the term: the first turn of a run must not be free.
	firstTurn, _ := CostUSD("claude-opus-5", Usage{InputTokens: 200, OutputTokens: 400, CacheWriteTokens: 10_800})
	noCacheAccounting, _ := CostUSD("claude-opus-5", Usage{InputTokens: 200, OutputTokens: 400})
	if firstTurn <= noCacheAccounting {
		t.Fatal("a turn that wrote a 10.8k-token prefix cost the same as one that wrote nothing")
	}

	// A model priced without an explicit cache-write rate must still charge for
	// one. Zero is the state this replaced, and it is indistinguishable from the
	// term not existing.
	RegisterPrice("test-model-no-cache-rate", Price{InputPer1M: 10, OutputPer1M: 20})
	if c, _ := CostUSD("test-model-no-cache-rate", Usage{CacheWriteTokens: million}); c <= 10 {
		t.Fatalf("an unstated cache-write rate priced at %.4f; want above the 10.00 input rate", c)
	}
}

// A total is the only figure anybody sees, so a term that survives one call and
// is dropped by the accumulator is the same bug one layer out.
func TestPricing_CacheWritesSurviveAccumulation(t *testing.T) {
	var total Usage
	for i := 0; i < 3; i++ {
		total.Add(Usage{InputTokens: 100, CacheWriteTokens: 1000, Calls: 1})
	}
	if total.CacheWriteTokens != 3000 {
		t.Fatalf("three turns of 1000 cache-write tokens accumulated to %d", total.CacheWriteTokens)
	}
}
