package agent_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// The frontend keeps its own copy of "which action kinds create an element",
// because dropping a create from the review list has to cascade to everything
// parented to it. Two lists, no compiler between them.
//
// It had already drifted: place_file, create_heading and comment all create
// elements and none of the three were listed, so dropping one of them from a
// plan would have left its children behind as orphans pointing at an id the
// commit never wrote.
//
// This test reads the TypeScript. That is unusual and it is the only thing that
// works — every other guard we have proves the two halves are internally
// consistent, which is exactly what they were while disagreeing with each other.
func TestFrontendCreateKindsMatchTheRegistry(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "agent", "agentStore.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("frontend not present in this checkout: %v", err)
	}

	block := regexp.MustCompile(`(?s)const CREATE_KINDS = new Set\(\[(.*?)\]\)`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("could not find CREATE_KINDS in %s — if it was renamed, this guard needs "+
			"renaming with it rather than deleting", path)
	}
	inFrontend := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(string(block[1]), -1) {
		inFrontend[m[1]] = true
	}

	for _, kind := range agent.KnownKinds() {
		spec, _ := agent.SpecFor(kind)
		name := string(kind)
		// A duplicate creates, but through Copies rather than its own id, and
		// the frontend needs to know so a dropped copy cascades.
		switch {
		case spec.Creates && !inFrontend[name]:
			t.Errorf("%s creates an element and the frontend does not know: "+
				"dropping it from the review list would orphan its children.\n"+
				"Add '%s' to CREATE_KINDS in frontend/src/agent/agentStore.ts", name, name)
		case !spec.Creates && inFrontend[name]:
			t.Errorf("the frontend lists %s as a create and the server does not. "+
				"Remove '%s' from CREATE_KINDS in frontend/src/agent/agentStore.ts", name, name)
		}
		delete(inFrontend, name)
	}
	for name := range inFrontend {
		t.Errorf("frontend CREATE_KINDS names %q, which is not a kind the server has", name)
	}
}

// Every kind the server can put in a plan must have a label in the review list.
// An unlabelled action falls back to its raw wire name, so a person reviewing a
// plan reads "set_note_text" where every other row says "Edit" — which reads as
// a bug, and in an Arabic session reads as untranslated English.
func TestFrontendLabelsEveryKind(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "agent", "agentStore.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("frontend not present in this checkout: %v", err)
	}
	block := regexp.MustCompile(`(?s)const KIND_KEYS: Record<string, TKey> = \{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("could not find KIND_KEYS in agentStore.ts")
	}
	labelled := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:^|[\s,{])([a-z_]+):`).FindAllStringSubmatch(string(block[1]), -1) {
		labelled[m[1]] = true
	}

	for _, kind := range agent.KnownKinds() {
		if !labelled[string(kind)] {
			t.Errorf("%s has no label: a plan row would show the raw wire name. "+
				"Add it to KIND_KEYS in frontend/src/agent/agentStore.ts", kind)
		}
	}
}

// set_color wrote content.color for its whole shipped life. NoteCard paints
// content.backgroundColor. So the agent coloured nine cards, the review row
// said "→ amber", the transaction committed — and the board looked exactly the
// same. Worse, the digest's colorOf read the same wrong key, so the next run
// saw its own invisible taxonomy reflected back and reasoned as though the
// colours were there. Every capability test in the suite passed throughout,
// because each of them compared the compiler against a constant that was
// itself the wrong one.
//
// That is why this reads the component. A guard that compares the server to
// itself is precisely the guard that was already green.
func TestSetColorWritesTheKeyNoteCardPaints(t *testing.T) {
	path := filepath.Join("..", "..", "..", "frontend", "src", "components", "elements", "NoteCard.tsx")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("frontend not present in this checkout: %v", err)
	}
	m := regexp.MustCompile(`const bg = element\.content\?\.([A-Za-z0-9_]+)`).FindSubmatch(src)
	if m == nil {
		t.Fatalf("could not find the paper-colour read in %s — if NoteCard was "+
			"restructured, re-point this guard at whatever now paints the card "+
			"rather than deleting it", path)
	}
	rendered := string(m[1])

	scope := &agent.BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{
			"card-1": {ID: "card-1", Type: domain.TypeCard,
				Content:  domain.Content{"textPreview": "Harbour interview"},
				Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}},
		},
	}
	ops, err := agent.CompileOps(&agent.Plan{Actions: []agent.Action{{
		Seq: 1, Kind: agent.ActSetColor, ElementID: "card-1", Color: "#fff4e6",
	}}}, scope)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("set_color compiled to %d ops, want 1", len(ops))
	}
	content, _ := ops[0].Changes["content"].(map[string]any)
	if content[rendered] != "#fff4e6" {
		t.Errorf("set_color writes %v; NoteCard paints content.%s, so the swatch "+
			"is invisible on the board and the run reports a change nobody can see",
			content, rendered)
	}
	undo, _ := ops[0].UndoChanges["content"].(map[string]any)
	if _, ok := undo[rendered]; !ok {
		t.Errorf("the inverse writes %v, not content.%s — undo would leave the "+
			"agent's colour on the card", undo, rendered)
	}

	// The read side of the same key, in the same test, because splitting them is
	// how the pair drifted apart in the first place.
	coloured := &domain.Element{ID: "card-1", Type: domain.TypeCard,
		Content: domain.Content{rendered: "#fff4e6"}}
	if got := agent.ItemFor(coloured).Color; got != "orange" {
		t.Errorf("the digest reads content.%s as %q, want \"orange\": the agent "+
			"cannot see a colour a person set with the picker", rendered, got)
	}
}
