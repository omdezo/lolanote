package agent

// Exposed to the external agent_test package so the tool surface can be checked
// against itself at runtime instead of by pattern-matching the source. The
// dispatch used to be a switch, and the only way to enumerate it was a regex —
// which meant the guard broke the moment the switch became a table.

// ImplementedToolNames returns every tool name the dispatch can answer.
func ImplementedToolNames() []string {
	out := make([]string, 0, len(toolHandlers))
	for name := range toolHandlers {
		out = append(out, name)
	}
	return out
}

// StyleForTest exposes how a relation is drawn, so a test can assert that two
// relations look different without hard-coding the palette here.
func StyleForTest(r Relation) struct {
	Color  string
	Weight int
} {
	s := styleFor(r)
	return struct {
		Color  string
		Weight int
	}{Color: s.Color, Weight: s.Weight}
}
