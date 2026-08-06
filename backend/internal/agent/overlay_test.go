package agent

import (
	"context"
	"testing"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// The staged plan was write-once. resolveExisting read only the compiled scope,
// so roughly eighteen revise verbs refused every id the same run had just
// created: create twelve cards and tag them all "Q3" was impossible, and so was
// creating a table and correcting one cell.
//
// connect was the lone exception, and its own comment says create-then-relate
// "is the whole activity" — which is exactly as true of create-then-revise.
func TestOverlay_AToolCanReviseWhatThisRunJustCreated(t *testing.T) {
	s := capStaging()
	ctx := context.Background()

	mk := &reply{staging: s, call: cognition.ToolCall{ID: "c1", Name: toolCreateNote}}
	out := s.runCreateBoard(ctx, &toolArgs{
		ParentID: "b1", Text: "Establishing wide",
	}, mk)
	if out.IsError {
		t.Fatalf("create: %s", out.Content)
	}
	newID := s.plan.Actions[0].ElementID

	// The colour verb is the archetype: it goes through resolveExisting, and
	// before the overlay it answered "there is no element <id> on this board"
	// about an id it had handed back one call earlier.
	if out := s.runSetColor(ctx, &toolArgs{ElementID: newID, Color: "yellow"},
		&reply{staging: s}); out.IsError {
		t.Fatalf("colouring a card this run created was refused: %s", out.Content)
	}
	if len(s.plan.Actions) != 2 {
		t.Fatalf("staged %d actions, want the create and the colour", len(s.plan.Actions))
	}
}

// The compiler half. A staged target has no prior value, so the inverse must be
// the ABSENT state — reverting the create deletes the element outright, and an
// inverse claiming to restore an earlier colour would describe a state that
// never existed.
func TestOverlay_ReviseOfAStagedElementCompilesWithAnHonestInverse(t *testing.T) {
	s := capStaging()
	p := &Plan{Actions: []Action{
		{Seq: 0, Kind: ActCreateNote, ElementID: "fresh-1", ParentID: "b1", Text: "Establishing wide"},
		{Seq: 1, Kind: ActSetColor, ElementID: "fresh-1", Color: "#fff9db"},
	}}

	ops, err := CompileOps(p, s.scope)
	if err != nil {
		t.Fatalf("a plan that revises its own creation did not compile: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("compiled %d ops, want the create and the update", len(ops))
	}
	if ops[0].Action != domain.ActionCreate {
		t.Errorf("first op = %s", ops[0].Action)
	}

	content, _ := ops[1].Changes["content"].(map[string]any)
	if content["backgroundColor"] != "#fff9db" {
		t.Errorf("the revise did not reach the key the card paints: %v", content)
	}
	// Honest inverse: nothing to restore, because there was nothing there.
	undo, _ := ops[1].UndoChanges["content"].(map[string]any)
	if prev, present := undo["backgroundColor"]; present && prev != "" && prev != nil {
		t.Errorf("the inverse claims a prior colour of %v on an element that "+
			"did not exist before this transaction", prev)
	}
}

// An id that is neither on the board nor staged is still a mistake, and the
// refusal must still name what IS available.
func TestOverlay_AnInventedIDIsStillRefused(t *testing.T) {
	s := capStaging()
	out := s.runSetColor(context.Background(),
		&toolArgs{ElementID: "not-a-real-id", Color: "yellow"}, &reply{staging: s})
	if !out.IsError {
		t.Fatal("an invented id was accepted")
	}
	if len(s.plan.Actions) != 0 {
		t.Error("something was staged against an id that does not exist")
	}
}
