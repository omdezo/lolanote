package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// The settings screen has a text box titled "Standing notes for the agent",
// subtitled "Applies to every board you own. A board can add its own rules,
// which win where the two disagree." It is shipped, translated, and saved.
//
// Its contents reached exactly one reader in the whole tree — the injection
// detector's lifted-id whitelist. The model never saw a word of it, and the
// precedence the subtitle promises could not be honoured by a run that was
// handed neither rule set. These tests pin both halves of that sentence.

func standingScope(runner, owner, account, board string) *BoardScope {
	return &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Film"},
			ACL:     &domain.ACL{OwnerID: owner}},
		Elements:            map[string]*domain.Element{},
		Runner:              runner,
		AccountInstructions: account,
		Instructions:        board,
	}
}

func TestStandingNotes_TheAccountBlockReachesTheModel(t *testing.T) {
	out := standingScope("omar", "omar", "Never invent new columns. Keep titles under four words.", "").Render("")

	if !strings.Contains(out, "Never invent new columns") {
		t.Errorf("the person's account-wide standing notes never reached the digest:\n%s", out)
	}
	if !strings.Contains(out, "YOUR STANDING NOTES") {
		t.Errorf("the account notes arrived without a block heading, so nothing "+
			"tells the model they apply beyond this board:\n%s", out)
	}
	if !strings.Contains(out, "⟨user⟩") {
		t.Errorf("the account block is unlabelled, which breaks the digest's own "+
			"invariant that nothing enters the context without its provenance:\n%s", out)
	}
}

// Two fields exist because one overrules the other. A model cannot apply a
// precedence rule it was not told, so the rule is on the line rather than in
// the ordering.
func TestStandingNotes_ThePrecedenceIsStatedNotImplied(t *testing.T) {
	out := standingScope("omar", "omar",
		"Tag everything by owner.",
		"On this board, tag by scene instead.").Render("")

	if !strings.Contains(out, "wins where the two disagree") {
		t.Errorf("both rule sets arrived and neither says which one wins — the "+
			"settings screen promises a precedence the digest does not state:\n%s", out)
	}
	// Never concatenated: two voices merged into one paragraph is exactly the
	// shape that makes a precedence unstatable.
	if strings.Contains(out, "Tag everything by owner. On this board") {
		t.Errorf("the two rule sets were concatenated into one block:\n%s", out)
	}
}

// "Applies to every board you own." A shared board belongs to somebody else,
// and a private house style must not be exported into their workspace by a run
// they never asked for.
func TestStandingNotes_AreDroppedOnSomebodyElsesBoard(t *testing.T) {
	out := standingScope("omar", "sara", "Never invent new columns.", "Keep the cast list first.").Render("")

	if strings.Contains(out, "Never invent new columns") {
		t.Errorf("a guest's private account preferences were exported into the "+
			"board owner's workspace:\n%s", out)
	}
	if !strings.Contains(out, "Keep the cast list first") {
		t.Errorf("the board's own rules must still apply on a board you are a "+
			"guest on — they are the owner's, not yours:\n%s", out)
	}
	// With only one rule set delivered, a precedence clause would describe a
	// disagreement that cannot arise.
	if strings.Contains(out, "wins where the two disagree") {
		t.Errorf("the digest claims a precedence between two rule sets while "+
			"carrying one:\n%s", out)
	}
}

// A board with no ACL is not proof of ownership. "We cannot tell" resolves the
// way that keeps the preference private.
func TestStandingNotes_AnUnknownOwnerKeepsThemPrivate(t *testing.T) {
	s := standingScope("omar", "omar", "Never invent new columns.", "")
	s.Board.ACL = nil
	if out := s.Render(""); strings.Contains(out, "Never invent new columns") {
		t.Errorf("account notes leaked onto a board whose ownership could not be "+
			"established:\n%s", out)
	}
}
