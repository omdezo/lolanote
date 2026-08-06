package agent_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"qomranote/backend/internal/agent"
)

// Every ActionKind the code declares must be registered, and every registered
// kind must be compilable.
//
// This closes the failure that shipped three capabilities dead: create_table,
// connect and clone_here each had a constant, a validator and a tool that
// staged them, and no arm in the CompileOps dispatch. They compiled, passed
// every test, and could not produce a single op — because a missing `case` is
// not something a compiler can notice.
func TestActionSpec_EveryDeclaredKindIsRegistered(t *testing.T) {
	src, err := os.ReadFile("plan.go")
	if err != nil {
		t.Fatalf("read plan.go: %v", err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`Act[A-Za-z]+\s+ActionKind\s*=\s*"([a-z_]+)"`).
		FindAllStringSubmatch(string(src), -1) {
		declared[m[1]] = true
	}
	if len(declared) == 0 {
		t.Fatal("could not parse ActionKind constants; this test needs updating alongside plan.go")
	}

	registered := map[string]bool{}
	for _, k := range agent.KnownKinds() {
		registered[string(k)] = true
	}

	var missing []string
	for kind := range declared {
		if !registered[kind] {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d action kind(s) declared but not registered — they will fail to validate "+
			"and probably to compile: %s", len(missing), strings.Join(missing, ", "))
	}

	// And nothing registered that the code does not declare, which would mean a
	// spec for a kind no tool can ever produce.
	var orphan []string
	for kind := range registered {
		if !declared[kind] {
			orphan = append(orphan, kind)
		}
	}
	sort.Strings(orphan)
	if len(orphan) > 0 {
		t.Errorf("registered kind(s) with no constant: %s", strings.Join(orphan, ", "))
	}
}

// Every kind that CompileOps can be handed must actually produce ops. This is
// the assertion that would have caught create_table directly.
func TestActionSpec_EveryRegisteredKindCompiles(t *testing.T) {
	src, err := os.ReadFile("plan.go")
	if err != nil {
		t.Fatalf("read plan.go: %v", err)
	}
	body := string(src)

	// Constant identifiers are not derivable from wire names — ActDelete is
	// "delete_element", ActSetText is "set_note_text" — so read the mapping out
	// of the declarations rather than guessing a convention that does not hold.
	identOf := map[string]string{} // wire → identifier
	for _, m := range regexp.MustCompile(`(Act[A-Za-z]+)\s+ActionKind\s*=\s*"([a-z_]+)"`).
		FindAllStringSubmatch(body, -1) {
		identOf[m[2]] = m[1]
	}
	if len(identOf) == 0 {
		t.Fatal("could not parse ActionKind constants")
	}

	// Bound the scan to CompileOps ITSELF. Scanning to end-of-file made this a
	// false negative: createOp mentions the same constants further down, so a
	// kind dropped from the dispatch still appeared "handled" and the guard
	// reported success on exactly the bug it exists to catch.
	from := strings.Index(body, "func CompileOps(")
	if from < 0 {
		t.Fatal("CompileOps not found")
	}
	rest := body[from+1:]
	to := strings.Index(rest, "\nfunc ")
	if to < 0 {
		t.Fatal("could not find the end of CompileOps")
	}
	compileBody := rest[:to]

	handled := map[string]bool{}
	for _, m := range regexp.MustCompile(`Act[A-Za-z]+`).FindAllString(compileBody, -1) {
		handled[m] = true
	}

	var unreachable []string
	for _, k := range agent.KnownKinds() {
		ident, ok := identOf[string(k)]
		if !ok {
			t.Errorf("registered kind %q has no constant in plan.go", k)
			continue
		}
		if !handled[ident] {
			unreachable = append(unreachable, string(k))
		}
	}
	sort.Strings(unreachable)
	if len(unreachable) > 0 {
		t.Errorf("%d registered kind(s) are never handled by CompileOps — staging one would "+
			"stage an action that cannot become an op: %s",
			len(unreachable), strings.Join(unreachable, ", "))
	}
}
