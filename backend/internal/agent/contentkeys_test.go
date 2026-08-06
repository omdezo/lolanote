package agent

import (
	"sort"
	"strings"
	"testing"
)

// The delegation's content guard is a two-item DENYLIST — `isHome` and
// `isTemplate`, plus `acl` — over a schemaless map. A denylist maintained by
// everyone who adds a feature will be wrong, and it already is: `content.locked`
// is not on it, `content.cloneSourceId` is not on it, and `content.
// agentInstructions` is not on it, so the only thing stopping a run from
// rewriting the standing rules that CONSTRAIN it is that no tool happens to emit
// that key today.
//
// The invariant is the other way round: an agent op may write only the keys its
// own action kind declares. This is the table-driven probe that fails loudly the
// next time a capability lands without updating its contract.

// privilegedKeys are the content keys an agent write must never produce, ever.
// Enumerated HERE — in a test — rather than in the guard, because under the
// allowlist they are excluded by not being produced, and the point of listing
// them is to prove that. The denylist's own two entries are the first two.
var privilegedKeys = []string{
	"isHome", "isTemplate", "acl", "locked", "agentInstructions",
	"agentExclude", "ownerId",
}

func TestContentKeys_EveryKindDeclaresWhatItWrites(t *testing.T) {
	for kind := range actionSpecs {
		if _, declared := ContentKeysFor(kind); !declared && kind != ActDuplicate {
			t.Errorf("%s declares no content-key contract, so an allowlist derived from "+
				"the compiler would refuse everything it writes — or trust everything", kind)
		}
	}
	// The one kind that genuinely cannot be described by its key set: a copy
	// carries its SOURCE's content verbatim. It must say so rather than declare
	// an empty set, because "we cannot tell" and "it writes nothing" are
	// different answers and only the second is safe to wave through.
	if _, declared := ContentKeysFor(ActDuplicate); declared {
		t.Error("duplicate claims a fixed key set, which its own compiler branch contradicts")
	}
}

// A create's declared keys must be EXACTLY what the compiler emits. Derived by
// running the compiler rather than restated beside it: a contract kept in
// parallel with the code it describes drifts within one wave.
func TestContentKeys_MatchWhatTheCompilerActuallyEmits(t *testing.T) {
	scope := correctionScope()
	for kind, spec := range actionSpecs {
		if !spec.Creates || kind == ActDuplicate {
			continue
		}
		a := Action{
			Seq: 0, Kind: kind, ElementID: "probe", ParentID: "b1",
			Title: "t", Text: "x", URL: "https://example.invalid", Color: "#000",
			FromID: "f", ToID: "to", Rows: [][]string{{"a"}}, AssigneeID: "att",
			MimeType: "application/pdf", Size: 1, Tasks: []string{"a task"},
			Preview: &LinkPreview{Description: "d", ThumbnailURL: "u", SiteName: "s", EmbedType: "e"},
		}
		ops, err := CompileOps(&Plan{Actions: []Action{a}}, scope)
		if err != nil || len(ops) == 0 {
			continue // a kind whose compile needs live scope is covered elsewhere
		}
		declared := map[string]bool{}
		keys, _ := ContentKeysFor(kind)
		for _, k := range keys {
			declared[k] = true
		}
		// EVERY op, not ops[0]. Reading only the first one is how create_todo
		// shipped a contract of {title, authoredBy} while its TASK children
		// wrote {text, done} — and the allowlist, correctly, refused the agent's
		// own perfectly ordinary to-do list.
		undeclaredSet := map[string]bool{}
		for _, op := range ops {
			content, _ := op.Changes["content"].(map[string]any)
			for k := range content {
				if !declared[k] {
					undeclaredSet[k] = true
				}
			}
		}
		var undeclared []string
		for k := range undeclaredSet {
			undeclared = append(undeclared, k)
		}
		sort.Strings(undeclared)
		if len(undeclared) > 0 {
			t.Errorf("%s writes %v, which its own contract does not declare — an allowlist "+
				"would refuse the op and the capability would ship broken", kind, undeclared)
		}
	}
}

// The whole reason for flipping the guard: no privileged key can appear in any
// kind's declared set, so none has to be enumerated in the guard at all.
func TestContentKeys_NoKindMayEverWriteAPrivilegedKey(t *testing.T) {
	for kind := range actionSpecs {
		keys, _ := ContentKeysFor(kind)
		for _, k := range keys {
			for _, forbidden := range privilegedKeys {
				if strings.EqualFold(k, forbidden) {
					t.Errorf("%s declares the privileged key %q — under an allowlist that is "+
						"a grant, and this one lets a run rewrite its own constraints", kind, k)
				}
			}
		}
	}
}

// cloneSourceId is the case that shows why a denylist was the wrong shape.
// It is dangerous everywhere and legitimate in exactly one place, which a global
// denylist cannot express and an allowlist expresses for free.
func TestContentKeys_CloneSourceIsWritableByExactlyOneKind(t *testing.T) {
	var writers []ActionKind
	for kind := range actionSpecs {
		keys, _ := ContentKeysFor(kind)
		for _, k := range keys {
			if k == "cloneSourceId" {
				writers = append(writers, kind)
			}
		}
	}
	if len(writers) != 1 || writers[0] != ActCloneHere {
		t.Errorf("cloneSourceId is writable by %v, want only clone_here", writers)
	}
}

// Every create is stamped with its author on a separate pass over the compiled
// ops, so authoredBy is a key the kind produces even though its own switch arm
// never mentions it. A contract that missed it would refuse every create.
func TestContentKeys_TheAuthorshipStampIsDeclared(t *testing.T) {
	for kind, spec := range actionSpecs {
		// duplicate is excluded for the reason it is excluded everywhere: its
		// content is the source's, so it has no declarable set to add a stamp to.
		if !spec.Creates || kind == ActDuplicate {
			continue
		}
		found := false
		keys, _ := ContentKeysFor(kind)
		for _, k := range keys {
			if k == authoredByKey {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not declare %q, which stampAuthorship writes onto every create",
				kind, authoredByKey)
		}
	}
}
