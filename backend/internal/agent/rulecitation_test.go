package agent

import (
	"context"
	"strings"
	"testing"
)

// A standing-rules list is the one part of the system that only ever grows, and
// nothing recorded which rule had ever fired. A rule written for a board that has
// since changed shape was indistinguishable from one that fires every run, so the
// list became noise, the model started skimming it, and the rules that mattered
// stopped being obeyed — the exact failure the digest's budget discipline
// prevents everywhere else.

func citingStaging(rules string) *staging {
	s := capStaging()
	s.scope.Runner = "omar"
	s.scope.Instructions = rules
	return s
}

func TestRuleCitation_FinishRecordsWhichRulesTheRunFollowed(t *testing.T) {
	s := citingStaging("Columns are pipeline stages — never add one.\nKeep the cast list first.")
	out := s.runFinish(context.Background(), &toolArgs{
		Summary: "Filed the loose cards.", Applied: []string{"M2"},
	}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("finish failed: %s", out.Content)
	}
	if len(s.plan.AppliedMemoryIDs) != 1 {
		t.Fatalf("the citation was discarded: %v", s.plan.AppliedMemoryIDs)
	}
	shown, _ := s.scope.StandingRules()
	var want string
	for _, m := range shown {
		if m.Ref == "M2" {
			want = m.ID
		}
	}
	if want == "" || s.plan.AppliedMemoryIDs[0] != want {
		t.Errorf("cited M2 resolved to %q, want the id the digest actually rendered (%q)",
			s.plan.AppliedMemoryIDs[0], want)
	}
}

// The clause that keeps a self-report from becoming a write primitive.
func TestRuleCitation_AnInventedRuleIDIsDroppedSilently(t *testing.T) {
	s := citingStaging("Never add a column.")
	out := s.runFinish(context.Background(), &toolArgs{
		Summary: "Done.", Applied: []string{"M7", "M1"},
	}, &reply{staging: s})
	if out.IsError {
		t.Fatalf("a hallucinated rule id failed the run instead of being ignored: %s", out.Content)
	}
	if len(s.plan.AppliedMemoryIDs) != 1 {
		t.Errorf("a rule that does not exist was recorded as having fired: %v",
			s.plan.AppliedMemoryIDs)
	}
}

// The model cannot cite what it was never shown, so the ids have to be in the
// catalogue description as well as in the digest.
func TestRuleCitation_TheFinishToolAsksForThem(t *testing.T) {
	for _, def := range ToolCatalogue(true, true) {
		if def.Name != toolFinish {
			continue
		}
		props, _ := def.Schema["properties"].(map[string]any)
		if _, ok := props["applied"]; !ok {
			t.Fatal("finish has no `applied` argument, so a rule can never be confirmed " +
				"and the list can only grow")
		}
		desc, _ := def.Schema["properties"].(map[string]any)["applied"].(map[string]any)["description"].(string)
		if !strings.Contains(desc, "STANDING RULES") {
			t.Errorf("the argument does not say what it wants: %q", desc)
		}
		return
	}
	t.Fatal("finish is not in the catalogue")
}
