package mongo

import (
	"os"
	"strings"
	"testing"
)

// The journal adapter cannot be exercised without a live server, so this reads
// the source the way boundary_test.go reads handler signatures: the property is
// structural, and a structural property is worth pinning even when the round
// trip is not available to assert on.
//
// What it is pinning: Append used to derive the next sequence from
// `CountDocuments({runId})` inside a five-attempt retry loop. Two round trips
// per event, a count over a growing range — quadratic in run length — and,
// worse, check-then-act on a document several goroutines append to at once. A
// lost race DROPPED the event, which punched exactly the gap the unique
// (runId, sequence) index exists to forbid, and the client's `sequence > since`
// cursor had no way to notice. Every wave planned for this product adds an
// event type, so this had to stop being true before any of them landed.
func TestJournalAppend_DoesNotDeriveTheSequenceFromACount(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("read adapter: %v", err)
	}
	body := appendBody(t, string(src))

	if strings.Contains(body, "CountDocuments") {
		t.Error("Append still counts the collection to pick a sequence — that is quadratic and it is check-then-act")
	}
	if !strings.Contains(body, "$inc") {
		t.Error("Append does not allocate its sequence with $inc, so two concurrent emitters can still pick the same number")
	}
	if !strings.Contains(body, "SetUpsert(true)") {
		t.Error("the counter is not upserted, so the first event of every run has nothing to increment")
	}
}

// appendBody returns the text of Append, so a $inc somewhere else in the file
// cannot make this pass.
func appendBody(t *testing.T, src string) string {
	t.Helper()
	const sig = "func (r *AgentEventRepo) Append("
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("no %s in the adapter — this test is pinning a function that moved", sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j > 0 {
		return rest[:j]
	}
	return rest
}
