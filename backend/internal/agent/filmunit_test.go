package agent

import (
	"strings"
	"testing"

	"qomranote/backend/internal/domain"
)

// DF19 — a production's people are not this app's users, and set_assignee only
// reaches users. The digest's PEOPLE block is the board's COLLABORATORS, so a
// twelve-person crew in Oman — twelve phone numbers, no accounts — was invisible
// to the agent, and "who is called at what time, who is confirmed, who has
// signed a release" had no answer at all.

// unitTable builds a scope carrying a real table, because a call sheet keeps its
// cast in one and the roll-up has to reach into cells rather than only lines.
func unitTable(id string, cells [][]string) (Item, *domain.Element) {
	rows := make([]any, 0, len(cells))
	for _, r := range cells {
		row := make([]any, 0, len(r))
		for _, c := range r {
			row = append(row, c)
		}
		rows = append(rows, row)
	}
	return Item{ID: id, Type: domain.TypeTable, Trust: trustUser, ParentID: "b1",
			Text: "Cast — day 4"},
		&domain.Element{ID: id, Type: domain.TypeTable,
			Content:  domain.Content{"cells": rows, "title": "Cast — day 4"},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
}

func TestUnit_TheBoardsOwnPeopleAreReadOffTheCards(t *testing.T) {
	s := filmScope(
		card("c1", "Crew for the Wadi Shab block\nGaffer — Ahmed Al Balushi\n"+
			"1st AC: Fatma Al Riyami\nSound Mixer — TBC"),
		card("c2", "3 INT. HARBOUR OFFICE – NIGHT"),
	)
	block := s.domainBlock()
	if block == "" {
		t.Fatal("a crew card and a slugline did not trigger the pack")
	}
	for _, want := range []string{"UNIT", "Gaffer — Ahmed Al Balushi", "1st AC — Fatma Al Riyami"} {
		if !strings.Contains(block, want) {
			t.Errorf("the roll-up never says %q — the name is on the card and the agent still "+
				"cannot see it:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "NOBODY AGAINST THESE ROLES YET") ||
		!strings.Contains(block, "Sound Mixer") {
		t.Errorf("a role written with TBC against it is an OPEN position and the single most "+
			"useful thing on this block — it is not reported:\n%s", block)
	}
	// The whole point of the separation: nothing here may claim reach it has not
	// got.
	if !strings.Contains(block, "set_assignee cannot") {
		t.Errorf("the block does not say these people are unreachable by set_assignee, so the "+
			"model will try:\n%s", block)
	}
}

// The cast table is where a call sheet actually keeps its people, and the CAST
// ID is the join key the DOOD, the camera report and the second AD all match on.
func TestUnit_TheCastTableKeepsItsCastIDs(t *testing.T) {
	it, el := unitTable("t1", [][]string{
		{"Cast ID", "Character", "Artist", "Makeup", "On set"},
		{"1", "LAYLA", "Maryam Al Habsi", "05:30", "07:00"},
		{"2", "SAID", "Khalid Al Amri", "06:00", "07:30"},
	})
	s := filmScope(card("c1", "Call sheet — day 4, principal photography"))
	s.Items = append(s.Items, it)
	s.Elements["t1"] = el

	block := s.domainBlock()
	for _, want := range []string{"cast 1 LAYLA — Maryam Al Habsi", "cast 2 SAID — Khalid Al Amri"} {
		if !strings.Contains(block, want) {
			t.Errorf("the cast table's %q never reached the digest — the cast ID is the join key "+
				"every downstream document matches on:\n%s", want, block)
		}
	}
}

// MP14's separation, which is the reason this block is built out of CONTENT and
// not out of the scope's People: a name typed on a card must never be presented
// as somebody holding an account, and no subject id or per-run alias may leak
// into it.
func TestUnit_ContentNamesNeverMixWithCollaborators(t *testing.T) {
	s := filmScope(card("c1", "Shooting schedule\nDirector — Ahmed Al Balushi\n"+
		"Script Supervisor: Noor"))
	s.People = []PersonRef{{ID: "auth0|9f3e-secret-sub", Name: "Sara Khan", Alias: "person1"}}

	block := unitBlock(s)
	if block == "" {
		t.Fatal("the roll-up found nobody on a board naming two roles and two people")
	}
	for _, leak := range []string{"auth0|9f3e-secret-sub", "person1", "Sara Khan"} {
		if strings.Contains(block, leak) {
			t.Errorf("the UNIT block published %q — collaborators and card-derived names are "+
				"two different populations and merging them is how \"assign it to Ahmed\" "+
				"reaches the wrong account:\n%s", leak, block)
		}
	}
}

// The pack is conditional and so is this. A board about anything else must not
// start reporting a unit.
func TestUnit_ANonFilmBoardHasNoUnit(t *testing.T) {
	s := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Hiring"}},
		Elements: map[string]*domain.Element{},
		Items: []Item{
			card("c1", "Editor — Jane Doe"),
			card("c2", "Writer: Sam"),
		},
	}
	if s.domainBlock() != "" {
		t.Error("a hiring board is paying for the film pack and being told it has a unit")
	}
}

// DF1's conditionality had a hole in exactly the place it mattered most: the
// pack triggers on the BOARD's vocabulary, and the flagship request — "make
// tomorrow's call sheet" — arrives on an empty board. Nine of the corpus's film
// probes seed an empty board, so every one of them was measuring the trigger
// rather than the answer.
func TestUnit_TheRequestAloneCanTriggerThePack(t *testing.T) {
	empty := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Untitled"}},
		Elements: map[string]*domain.Element{},
	}
	if empty.domainBlock() != "" {
		t.Fatal("an empty untitled board triggered the pack on its own")
	}
	empty.Intent = "make tomorrow's call sheet"
	block := empty.domainBlock()
	if block == "" {
		t.Fatal("\"make tomorrow's call sheet\" on an empty board got none of the pack — " +
			"the agent was asked for the artefact this whole corner is built around and " +
			"handed no structure for it")
	}
	if !strings.Contains(block, "film_spec") {
		t.Errorf("the pack arrived without the route to the call sheet's own spec:\n%s", block)
	}
	// And an ordinary request must still cost nothing.
	empty.Intent = "tidy up this board and group the cards"
	if empty.domainBlock() != "" {
		t.Error("an ordinary tidy request now pays for the film pack")
	}
}

// A role word followed by a sentence is a NOTE. A "crew member" called "check
// the watch on Layla's left wrist" is how a block stops being read.
func TestUnit_AContinuityNoteIsNotACrewMember(t *testing.T) {
	s := filmScope(card("c1", "Shooting schedule notes\n"+
		"Continuity — check the watch on Layla's left wrist matches scene 12\n"+
		"Gaffer — Ahmed"))
	block := unitBlock(s)
	if strings.Contains(block, "left wrist") {
		t.Errorf("a continuity note was filed as a person:\n%s", block)
	}
	if !strings.Contains(block, "Gaffer — Ahmed") {
		t.Errorf("the guard threw away a real crew member with the note:\n%s", block)
	}
}

// A role has to win on its longest match or the crew list is filed wrong: a
// focus puller reported as "grip" is a crew list nobody trusts.
func TestUnit_TheLongestRoleWins(t *testing.T) {
	s := filmScope(card("c1", "Camera and grip, shooting schedule\n"+
		"Key Grip — Sultan\nBest Boy Grip — Yousef\n2nd AC: Aisha"))
	block := unitBlock(s)
	for _, want := range []string{"Key Grip — Sultan", "Best Boy Grip — Yousef", "2nd AC — Aisha"} {
		if !strings.Contains(block, want) {
			t.Errorf("%q was not read as its own role:\n%s", want, block)
		}
	}
}
