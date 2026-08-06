package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"qomranote/backend/internal/domain"
)

// The domain corner: film production as it is actually practised, and Oman.
//
// Every test here is written against the failure the item describes rather than
// against the implementation, because most of what is being asserted is CONTENT
// — and content is exactly the kind of thing that gets refactored out of a
// prompt by somebody tidying up who does not know a shot list has a lens column.

// filmScope builds a board whose own words say production, which is the only
// state in which any of this is supposed to cost anything.
func filmScope(items ...Item) *BoardScope {
	s := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Wadi Shab — Shooting Schedule"}},
		Elements: map[string]*domain.Element{},
		Items:    items,
	}
	for _, it := range items {
		if _, ok := s.Elements[it.ID]; ok {
			continue
		}
		s.Elements[it.ID] = &domain.Element{ID: it.ID, Type: it.Type,
			Content:  domain.Content{"textPreview": it.Text},
			Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	}
	return s
}

// emptyLabels is a label store holding nothing, which is the only state that
// matters for "did the colour survive being staged".
type emptyLabels struct{}

func (emptyLabels) Insert(context.Context, *domain.Label) error { return nil }
func (emptyLabels) Get(context.Context, string) (*domain.Label, error) {
	return nil, domain.ErrNotFound
}
func (emptyLabels) ListByOwner(context.Context, string) ([]*domain.Label, error) { return nil, nil }
func (emptyLabels) Update(context.Context, *domain.Label) error                  { return nil }
func (emptyLabels) Delete(context.Context, string) error                         { return nil }
func (emptyLabels) DeleteByOwner(context.Context, string) error                  { return nil }
func (emptyLabels) IncrementUsage(context.Context, string, int64) error          { return nil }

func card(id, text string) Item {
	return Item{ID: id, Type: domain.TypeCard, Trust: trustUser, Text: text, ParentID: "b1"}
}

// DF1 — the only domain content in the whole system was twelve lines of nouns.
// The agent knew the WORD "call sheet" and produced a sticky note saying "Call
// sheets", which is the moment a filmmaker decides the tool is a toy.
func TestDomain_TheArtefactsHaveStructureNotJustNames(t *testing.T) {
	for _, key := range []string{"call-sheet", "shot-list", "breakdown", "dood",
		"budget", "deliverables", "recce", "crew", "media-log", "post"} {
		spec, ok := artefactFor(key)
		if !ok {
			t.Fatalf("%s is offered in the menu and has no spec", key)
		}
		if len(spec.Columns) == 0 && len(spec.Fields) == 0 {
			t.Errorf("%s has a name and no structure — which is exactly the state this "+
				"pack exists to end", key)
		}
		if spec.Note == "" {
			t.Errorf("%s carries nothing a practitioner knows that a generic answer misses", key)
		}
	}
}

// DF1's hard clause is the DIGEST, and specifically its CONDITIONALITY: a
// domain pack that rides on every board is a tax on everybody who is not a
// filmmaker.
func TestDomain_ThePackCostsNothingOnANonFilmBoard(t *testing.T) {
	sprint := &BoardScope{
		Board: &domain.Element{ID: "b1", Type: domain.TypeBoard,
			Content: domain.Content{"title": "Q3 Roadmap"}},
		Elements: map[string]*domain.Element{},
		Items: []Item{
			card("c1", "Ship the billing migration"),
			card("c2", "Interview three customers about onboarding"),
		},
	}
	if block := sprint.domainBlock(); block != "" {
		t.Errorf("a roadmap board is paying for the film pack:\n%s", block)
	}

	film := filmScope(
		card("c1", "3 INT. HARBOUR OFFICE – NIGHT"),
		card("c2", "Call sheet for day 4"),
	)
	block := film.domainBlock()
	if block == "" {
		t.Fatal("a board holding sluglines and a call sheet did not trigger the pack")
	}
	for _, want := range []string{"film_spec", "Ministerial Resolution No. 286",
		"Ministry of Information", "Principal Photography", "as of "} {
		if !strings.Contains(block, want) {
			t.Errorf("the DOMAIN block never mentions %q:\n%s", want, block)
		}
	}
}

// One coincidental word must not switch it on. "Scene" appears on design boards
// and "grade" on school ones.
func TestDomain_OneWordIsNotEnoughToTriggerThePack(t *testing.T) {
	s := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard, Content: domain.Content{"title": "Ideas"}},
		Elements: map[string]*domain.Element{},
		Items:    []Item{card("c1", "The opening scene of the pitch needs work")},
	}
	if s.domainBlock() != "" {
		t.Error("one occurrence of \"scene\" turned the whole film pack on")
	}
}

// DF24 — the permit chain is the corpus's own probe and the agent had nothing
// to answer it with. Every regulatory fact must arrive CITED and DATED, because
// an uncited rule cannot be checked and goes stale silently.
func TestDomain_EveryOmanFactCarriesItsSourceAndDate(t *testing.T) {
	for _, f := range append(append([]domainFact{}, omanFacts...), arabicDeliveryFacts...) {
		if f.Source == "" {
			t.Errorf("%q is asserted in the agent's own voice with no source — which is "+
				"indistinguishable from a fabrication to anybody reading it", f.Topic)
		}
		if f.AsOf == "" {
			t.Errorf("%q has no 'as of' date, so it will go stale invisibly", f.Topic)
		}
	}
	pack := DomainPackText()
	// The absence is a fact too, and the one every generic film-financing source
	// gets wrong about this country.
	if !strings.Contains(strings.ToLower(pack), "no film rebate") {
		t.Error("the pack never says Oman has no rebate, so a budget run will happily " +
			"plan around an incentive that does not exist")
	}
}

// DF2 — the naming rule forbade the trade's own identifier, and used a film
// example to do it. A guard, not a prompt line: a prompt sentence loses to a
// strong aesthetic instruction three paragraphs earlier.
func TestDomain_ARenameMayNotDropASceneNumber(t *testing.T) {
	s := reviseStaging()
	s.scope.Elements["scene-3"] = &domain.Element{ID: "scene-3", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "3 INT. HARBOUR OFFICE – NIGHT"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}

	out := s.runRename(context.Background(),
		&toolArgs{ElementID: "scene-3", Title: "Harbour Office"}, call(s, toolRename))
	if !out.IsError {
		t.Fatalf("the join key every downstream document matches on was renamed away: %s", out.Content)
	}
	if !strings.Contains(out.Content, "IDENTIFIER") {
		t.Errorf("the refusal does not say WHY, so the model will try the same edit through "+
			"a different verb: %s", out.Content)
	}
	if len(s.plan.Actions) != 0 {
		t.Error("the refused rename was staged anyway")
	}

	// Keeping the identifier is still allowed — the guard is about the number,
	// not about the words after it.
	out = s.runRename(context.Background(),
		&toolArgs{ElementID: "scene-3", Title: "3 INT. HARBOUR OFFICE – DAY"}, call(s, toolRename))
	if out.IsError {
		t.Fatalf("a rename that KEEPS the scene number was refused: %s", out.Content)
	}
}

// And an ordinary title is still an ordinary title. A guard that fires on
// "2026 planning" is a guard that gets deleted.
func TestDomain_TheIdentifierGuardLeavesOrdinaryTitlesAlone(t *testing.T) {
	s := reviseStaging()
	s.scope.Elements["c1"] = &domain.Element{ID: "c1", Type: domain.TypeCard,
		Content:  domain.Content{"textPreview": "Marketing ideas"},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	if out := s.runRename(context.Background(),
		&toolArgs{ElementID: "c1", Title: "Growth ideas"}, call(s, toolRename)); out.IsError {
		t.Fatalf("an ordinary rename was refused by the identifier guard: %s", out.Content)
	}
}

// DF6 — the prompt gets within an inch of competence and stops: it says a shot
// list wants a table and leaves the model to invent the headers. A shot list
// with no movement or lens column is a list of sentences.
func TestDomain_TheShotListCarriesItsRealColumns(t *testing.T) {
	s := reviseStaging()
	out := s.runFilmSpec(context.Background(), &toolArgs{Artefact: "shot-list"}, call(s, toolFilmSpec))
	if out.IsError {
		t.Fatalf("film_spec refused a shot list: %s", out.Content)
	}
	for _, col := range []string{"Size", "Angle", "Movement", "Lens"} {
		if !strings.Contains(out.Content, col) {
			t.Errorf("the shot-list spec has no %s column — that is the omission a DP "+
				"notices in two seconds:\n%s", col, out.Content)
		}
	}
}

// An unknown key hands back the menu. A bare rejection costs a whole turn to
// recover from when the model was one keystroke away.
func TestDomain_AnUnknownArtefactReturnsTheMenu(t *testing.T) {
	s := reviseStaging()
	out := s.runFilmSpec(context.Background(), &toolArgs{Artefact: "moodboard"}, call(s, toolFilmSpec))
	if !out.IsError {
		t.Fatal("an artefact nobody has a spec for was answered anyway")
	}
	if !strings.Contains(out.Content, "call-sheet") {
		t.Errorf("the refusal does not carry the list:\n%s", out.Content)
	}
}

// DF4 — the call sheet is the artefact that most obviously separates a
// colleague from a toy, and its required-field list is not negotiable.
func TestDomain_TheCallSheetNamesTheFieldsAndRefusesToInventThem(t *testing.T) {
	spec, _ := artefactFor("call-sheet")
	joined := strings.ToLower(spec.Render())
	for _, want := range []string{"hospital", "sunrise", "cast id", "basecamp",
		"advance schedule", "walkie"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the call sheet spec omits %q, which is on every one ever issued", want)
		}
	}
	if !strings.Contains(joined, "unmet") {
		t.Error("nothing tells the run to record an unsourced field rather than invent it — " +
			"and a wrong hospital address is the worst thing on this page")
	}
}

// DF13 — page eighths are how the trade converts script into time, and mixed
// numbers are exactly the arithmetic a language model gets wrong.
func TestDomain_PageEighthsAreAddedByTheServer(t *testing.T) {
	total, found := parseEighths("2 6/8 pages today, then 1 5/8 tomorrow")
	if found != 2 {
		t.Fatalf("found %d page counts, want 2", found)
	}
	if got := formatEighths(total); got != "4 3/8" {
		t.Errorf("2 6/8 + 1 5/8 came out as %q, want \"4 3/8\"", got)
	}
	if got := formatEighths(8); got != "1" {
		t.Errorf("eight eighths rendered as %q, want a whole page", got)
	}
	// A half is not four eighths. "1/2" on a card is almost never a page count,
	// and silently converting it corrupts the one number the daily report is
	// judged on.
	if _, found := parseEighths("half the crew, 1/2 of the kit"); found != 0 {
		t.Error("a bare 1/2 was read as a page count")
	}
}

// DF12 — shooting order is not story order, and every ordering primitive the
// agent had assumed it was. The correction is a separate verb, and its output
// has to carry the REASON or it is indistinguishable from a sort.
func TestDomain_RegroupProducesShootingOrderAndSaysWhatItSaves(t *testing.T) {
	s := reviseStaging()
	s.scope = filmScope(
		card("s1", "1 EXT. WADI SHAB – DAY\n2 6/8"),
		card("s2", "2 INT. HARBOUR OFFICE – NIGHT\n1 2/8"),
		card("s3", "3 EXT. WADI SHAB – DAY\n1 4/8"),
		card("s4", "4 INT. HARBOUR OFFICE – NIGHT\n5/8"),
	)
	out := s.runRegroup(context.Background(), &toolArgs{By: "location"}, call(s, toolRegroup))
	if out.IsError {
		t.Fatalf("regroup refused four ordinary sluglines: %s", out.Content)
	}
	if !strings.Contains(out.Content, "COMPANY MOVES") {
		t.Errorf("the grouping does not say what it saves, so it is a sort wearing a "+
			"scheduler's clothes:\n%s", out.Content)
	}
	// Written order alternates location three times; grouped, it moves once.
	if !strings.Contains(out.Content, "changes location 3 time(s)") ||
		!strings.Contains(out.Content, "changes it 1 time(s)") {
		t.Errorf("the company-move arithmetic is wrong:\n%s", out.Content)
	}
	// 2 6/8 + 1 4/8 at Wadi Shab, and 6 1/8 over the day. Mixed-number addition
	// is precisely what a model gets wrong, which is why the server does it.
	if !strings.Contains(out.Content, "4 2/8") || !strings.Contains(out.Content, "6 1/8") {
		t.Errorf("the page load per group was not summed in eighths:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "not story order") {
		t.Errorf("nothing states the distinction the whole verb exists for:\n%s", out.Content)
	}
}

// DF20 — backward planning from a fixed date is the trade's basic move, and the
// sentence that matters is not the subtraction but "this is already late".
func TestDomain_BackwardPlanningKnowsThePermitLeadTimesAndWhatIsLate(t *testing.T) {
	s := reviseStaging()
	s.scope.Timezone = "Asia/Muscat"
	out := s.runScheduleBackward(context.Background(), &toolArgs{
		Anchor: "2026-11-14",
		Steps: []struct {
			Name     string `json:"name"`
			LeadDays int    `json:"leadDays"`
		}{
			{Name: "drone permit"},
			{Name: "general filming permit"},
			{Name: "book the crane", LeadDays: 5},
			{Name: "invent something"},
		},
	}, call(s, toolBackward))
	if out.IsError {
		t.Fatalf("schedule_backward refused a plain anchor and four steps: %s", out.Content)
	}
	// 14 Nov minus 56 days is 19 September. The whole value of the tool is that
	// the server did that and the model did not.
	if !strings.Contains(out.Content, "2026-09-19") {
		t.Errorf("the drone permit's start-by date is wrong or missing:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "2026-10-24") {
		t.Errorf("the filming permit's start-by date is wrong or missing:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "NO LEAD TIME KNOWN") {
		t.Errorf("a step with no known duration was given one anyway:\n%s", out.Content)
	}
	if !strings.Contains(out.Content, "invent something") {
		t.Errorf("the unknown step is not named, so nobody can act on it:\n%s", out.Content)
	}
}

// DF21 + DF23 — nothing checked a plan against a constraint, and the most
// binding constraint in this country is a dated, penalised, publicly numbered
// law that exactly overlaps the hours a DP plans around.
func TestDomain_TheMiddayBanIsReportedWithItsResolutionNumber(t *testing.T) {
	scope := filmScope(
		card("s1", "12 EXT. WADI SHAB – DAY\nshooting 2026-07-14, unit call 13:00\n2 4/8"),
	)
	out := checkConstraints(scope, nil, 0, time.Now())
	if !strings.Contains(out, "Ministerial Resolution No. 286") {
		t.Errorf("the ban is stated as the agent's opinion rather than as a cited rule:\n%s", out)
	}
	if !strings.Contains(out, "12:30–15:30") {
		t.Errorf("the prohibited window is not stated:\n%s", out)
	}
	if !strings.Contains(out, "VIOLATION") {
		t.Errorf("a 13:00 call on an EXT/DAY scene in July was not flagged:\n%s", out)
	}
	// And the honest half: weather is the one production fact that cannot be
	// derived, so it must be named as missing rather than invented.
	if !strings.Contains(out, "WEATHER IS NOT HERE") {
		t.Errorf("nothing says weather was not checked:\n%s", out)
	}
}

// DF22 — Ramadan halves the working day here, and it moves every year, so the
// question cannot even be asked without the windows.
func TestDomain_ARamadanShootDayIsFlaggedAsHalfADay(t *testing.T) {
	scope := filmScope(card("s1", "8 INT. STUDIO – DAY\n2026-03-02"))
	out := checkConstraints(scope, nil, 0, time.Now())
	if !strings.Contains(out, "Ramadan") {
		t.Errorf("a shoot day inside Ramadan was not noticed:\n%s", out)
	}
	if !strings.Contains(out, "6 hours") {
		t.Errorf("the statutory cap is not stated, so \"Ramadan\" is trivia:\n%s", out)
	}
	if !strings.Contains(out, "moon sighting") {
		t.Errorf("the window is stated as certain, which is wrong about the one property "+
			"of the Hijri calendar everybody in the region knows:\n%s", out)
	}
}

// DF26 — every EXT/DAY scene is a race against one number, and that number is
// closed-form arithmetic rather than a fetch.
func TestDomain_SunTimesAreComputedNotGuessed(t *testing.T) {
	rise, set, ok := sunTimes(muscatLat, muscatLng, time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("midsummer in Muscat has no sunrise")
	}
	loc := workspaceLocation("Asia/Muscat")
	// Muscat on the solstice: sunrise about 05:22, sunset about 19:00 local. A
	// generous window, because the point is that the arithmetic is real rather
	// than that it matches an almanac to the minute.
	if h := rise.In(loc).Hour(); h < 4 || h > 6 {
		t.Errorf("midsummer sunrise computed as %s local", rise.In(loc).Format("15:04"))
	}
	if h := set.In(loc).Hour(); h < 18 || h > 20 {
		t.Errorf("midsummer sunset computed as %s local", set.In(loc).Format("15:04"))
	}
	if !set.After(rise) {
		t.Error("the sun sets before it rises")
	}
}

// DF11 — a DOOD's whole value is the arithmetic: hold days are days an actor is
// paid for and not used, and that is the number nobody can see by looking.
func TestDomain_TheDayOutOfDaysTotalsAreComputedServerSide(t *testing.T) {
	scope := filmScope(card("t1", "Day Out of Days"))
	scope.Elements["t1"] = &domain.Element{ID: "t1", Type: domain.TypeTable,
		Content: domain.Content{"cells": [][]string{
			{"Cast", "D1", "D2", "D3", "D4", "D5"},
			{"Layla", "SW", "W", "H", "H", "WF"},
			{"Salim", "", "SWF", "", "", ""},
		}},
		Location: domain.Location{ParentID: "b1", Section: domain.SectionCanvas}}
	scope.Items[0].Type = domain.TypeTable

	out := checkConstraints(scope, nil, 0, time.Now())
	if !strings.Contains(out, "Layla — 3 work, 2 hold") {
		t.Errorf("the DOOD totals are wrong or absent:\n%s", out)
	}
	if !strings.Contains(out, "paid for 2 day(s) they are not used") {
		t.Errorf("hold days are counted and their meaning is not stated, which is the "+
			"whole reason the report exists:\n%s", out)
	}
}

// DF27 — Arabic is a DELIVERABLE with a spec sheet here, not a presentation
// concern, and the numbers are checkable by machine.
func TestDomain_ArabicSubtitleLinesAreCheckedAgainstTheSpec(t *testing.T) {
	long := "subtitle: " + strings.Repeat("م", 60)
	scope := filmScope(card("sub1", long))
	out := checkConstraints(scope, nil, 0, time.Now())
	if !strings.Contains(out, "ARABIC SPEC") {
		t.Errorf("a 60-character Arabic subtitle line passed the lens:\n%s", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("the limit is not stated, so the finding is not actionable:\n%s", out)
	}
	// Arabic prose that is not a subtitle must not be nagged: a lens that fires
	// on everything is a lens people switch off.
	prose := filmScope(card("n1", "ملاحظات المخرج: "+strings.Repeat("م", 80)))
	if strings.Contains(checkConstraints(prose, nil, 0, time.Now()), "ARABIC SPEC") {
		t.Error("an ordinary Arabic note was judged against the subtitle spec")
	}
}

// DF10 — the breakdown system is DEFINED by colour, and create_label took a
// name only, so twenty categories became twenty identical grey chips.
func TestDomain_ALabelCanCarryTheColourThatIsItsMeaning(t *testing.T) {
	s := reviseStaging()
	s.labels = emptyLabels{}
	s.task.Owner = "alice"
	out := s.runCreateLabel(context.Background(),
		&toolArgs{Name: "Props", Color: "purple"}, call(s, toolCreateLabel))
	if out.IsError {
		t.Fatalf("a coloured label was refused: %s", out.Content)
	}
	if len(s.plan.NewLabels) != 1 {
		t.Fatalf("staged %d labels, want 1", len(s.plan.NewLabels))
	}
	if got := s.plan.NewLabels[0].Color; got != cardSwatches["purple"] {
		t.Errorf("the label was created %s, not the purple the model asked for", got)
	}
	// Off-palette is refused rather than silently coerced, exactly as a card's
	// colour is.
	if out := s.runCreateLabel(context.Background(),
		&toolArgs{Name: "Stunts", Color: "chartreuse"}, call(s, toolCreateLabel)); !out.IsError {
		t.Error("an off-palette label colour was accepted")
	}
}

// And the read side, in the same test file, because splitting the pair is how a
// write-without-a-read gets born: the agent would colour a chip, see a name and
// no colour next run, and colour it again differently.
func TestDomain_TheDigestReadsALabelsColourBackAsAName(t *testing.T) {
	s := filmScope(card("c1", "3 INT. HARBOUR OFFICE – NIGHT"), card("c2", "call sheet day 4"))
	s.Labels = []LabelRef{{ID: "l3", Name: "Props", Colour: cardSwatches["purple"]}}
	out := s.Render("")
	if !strings.Contains(out, "l3=Props") {
		t.Fatalf("the label vocabulary is missing:\n%s", out)
	}
	if !strings.Contains(lineContaining(out, "l3=Props"), "purple") {
		t.Errorf("the chip's colour is written and never read, so the agent cannot see its "+
			"own taxonomy:\n%s", lineContaining(out, "l3=Props"))
	}
}

// DF5 — twenty named categories with traditional colours, rather than "Props",
// "People", "Stuff".
func TestDomain_TheTwentyBreakdownCategoriesAreSeededNotInvented(t *testing.T) {
	if len(breakdownCategories) != 20 {
		t.Fatalf("the breakdown vocabulary has %d categories, want the standard 20",
			len(breakdownCategories))
	}
	vocab := breakdownVocabulary()
	for _, want := range []string{"Props (purple)", "Extras (green)", "Stunts (orange)",
		"Animal Handlers", "Optical Effects (blue)"} {
		if !strings.Contains(vocab, want) {
			t.Errorf("the seeded vocabulary is missing %q", want)
		}
	}
}

// DF9 — the product's swatch palette already IS the stripboard code and the
// revision wheel, and nothing knew it.
func TestDomain_TheStripboardCodeAndRevisionWheelAreTaught(t *testing.T) {
	pack := DomainPackText()
	for _, want := range []string{"EXT/DAY = yellow", "EXT/NIGHT = blue", "INT/NIGHT = green"} {
		if !strings.Contains(pack, want) {
			t.Errorf("the stripboard colour key is missing %q", want)
		}
	}
	if !strings.Contains(pack, "White → Blue → Pink → Yellow → Green → Goldenrod") {
		t.Error("the revision wheel is not in the pack, so \"which colour are you holding?\" " +
			"has no representation at all")
	}
	// And every strip colour must be a swatch this product can actually paint,
	// or the code is a sentence about colours nobody can see.
	for _, name := range []string{"yellow", "blue", "green"} {
		if cardSwatches[name] == "" {
			t.Errorf("%q is taught as a strip colour and is not in the palette", name)
		}
	}
}

// DF18 — the product's taste actively fights the domain: "past about seven
// columns you are building a wall" is right for a board of ideas and wrong for
// a shooting schedule, which legitimately has one column per shooting day.
func TestDomain_AScheduleIsNotAWall(t *testing.T) {
	schedule := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Items:    []Item{card("c1", "unit call 05:30")},
	}
	for i := 1; i <= 12; i++ {
		schedule.ExistingColumns = append(schedule.ExistingColumns, fmt.Sprintf("Day %d", i))
	}
	if d := DetectDrift(schedule); d != nil && d.Kind == "wall" {
		t.Errorf("the product offered to \"group these columns into nested boards\" on a "+
			"twelve-day shooting schedule — one click from destroying it: %q", d.Intent)
	}

	// An ordinary wall is still a wall. The exemption is about the artefact, not
	// about the count.
	wall := &BoardScope{
		Board:    &domain.Element{ID: "b1", Type: domain.TypeBoard},
		Elements: map[string]*domain.Element{},
		Items:    []Item{card("c1", "anything")},
		ExistingColumns: []string{"Ideas", "Doing", "Done", "Blocked", "Later",
			"Someday", "Archive", "Misc", "Notes"},
	}
	d := DetectDrift(wall)
	if d == nil || d.Kind != "wall" {
		t.Errorf("nine unrelated columns no longer read as a wall: %+v", d)
	}
}

// The same exemption on the critique path, which is the one the model actually
// reads and acts on mid-run.
func TestDomain_TheCritiqueDoesNotPunishAFixedShapeDocument(t *testing.T) {
	p := &Plan{}
	for i := 1; i <= 10; i++ {
		p.Actions = append(p.Actions, Action{Seq: i, Kind: ActCreateColumn,
			ParentID: "b1", Title: fmt.Sprintf("Day %d", i)})
	}
	for i := 1; i <= 6; i++ {
		p.Actions = append(p.Actions, Action{Seq: 10 + i, Kind: ActCreateNote,
			ParentID: "b1", Text: fmt.Sprintf("%d EXT. WADI SHAB – DAY", i)})
	}
	q := MeasurePlan(p, nil, Budget{MaxActions: 60})
	if q.FixedShape == "" {
		t.Fatal("ten day-columns were not recognised as a shooting schedule")
	}
	for _, line := range q.CritiqueFor(expectation{}) {
		if strings.Contains(line, "close to empty") {
			t.Errorf("a correct ten-day schedule was told to use fewer groups: %q", line)
		}
	}
	if !strings.Contains(q.Report(), "RECOGNISED") {
		t.Error("nothing tells the model WHY it was not criticised, so the next turn " +
			"helpfully rebuilds the schedule into three acts")
	}
}

// DF31 — the prompt distinguishes what to do and never who for, and in this
// craft the same material has five correct shapes.
func TestDomain_TheBoardSaysWhoseItIs(t *testing.T) {
	ad := filmScope(
		card("c1", "call sheet — day 4, unit call 05:30"),
		card("c2", "company move to the harbour after lunch"),
		card("c3", "turnaround is 11 hours, so tomorrow's call is 07:00"),
	)
	block := ad.domainBlock()
	if !strings.Contains(block, "1st AD's schedule") {
		t.Errorf("a board of call times and company moves did not read as the AD's:\n%s", block)
	}

	writer := filmScope(
		card("c1", "3 INT. HARBOUR OFFICE – NIGHT"),
		card("c2", "Act II — the pressure builds"),
		card("c3", "logline: a diver finds a wreck"),
		card("c4", "treatment draft two"),
	)
	if !strings.Contains(writer.domainBlock(), "writer's script") {
		t.Errorf("a board of sluglines, acts and a logline did not read as the writer's:\n%s",
			writer.domainBlock())
	}
}

// DF35 — the register is the whole question in this trade, and "Filming" where
// the trade says "Principal Photography" reads as a tourist.
func TestDomain_TheRegisterIsTaughtInBothDirections(t *testing.T) {
	block := registerBlock()
	if !strings.Contains(block, "Principal Photography") {
		t.Error("the register table does not carry the term the whole item is named after")
	}
	if !strings.Contains(block, "do NOT invent an Arabic form") {
		t.Error("nothing stops the run inventing an Arabic phrase for \"call sheet\" — " +
			"which produces something no Omani crew member recognises")
	}
	if !strings.Contains(block, "destroys the writer's voice") {
		t.Error("nothing stops the reverse failure: translating Arabic character names " +
			"into English on a rewrite")
	}
	// Every English-only term must be marked as such rather than left blank and
	// silently translated by a well-meaning run.
	for _, term := range productionRegister {
		if term.Trade == "" {
			t.Errorf("%q maps to nothing", term.Generic)
		}
	}
}

// DF3 + DF18 — the two prompt clauses this corner turns on. Asserted as text
// because they are content, and content is what gets tidied away by somebody
// who does not know a slugline is 29 characters.
func TestDomain_ThePromptCarriesTheShapeAndIdentifierRules(t *testing.T) {
	for _, want := range []string{
		"A container title is a BUCKET NAME",
		"canonical IDENTIFIER",
		"its shape wins over this guidance",
		"SHOOTING ORDER IS NOT STORY ORDER",
		"film_spec",
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("the system prompt no longer says %q", want)
		}
	}
}

// The four production reads are READS. A domain capability that wrote to the
// board would be a second planner with none of the review.
func TestDomain_TheProductionToolsStageNothing(t *testing.T) {
	s := reviseStaging()
	s.scope = filmScope(card("s1", "1 EXT. WADI SHAB – DAY\n2 6/8"))
	ctx := context.Background()
	s.runFilmSpec(ctx, &toolArgs{Artefact: "call-sheet"}, call(s, toolFilmSpec))
	s.runRegroup(ctx, &toolArgs{By: "location"}, call(s, toolRegroup))
	s.runCheckConstraints(ctx, &toolArgs{}, call(s, toolCheck))
	if len(s.plan.Actions) != 0 {
		t.Errorf("a read tool staged %d change(s)", len(s.plan.Actions))
	}
}

// A checker that always finds something is a checker people stop reading, so
// the empty case has to be honest about what it did and did not look at.
func TestDomain_TheCheckerSaysWhenItHasNothingToSay(t *testing.T) {
	out := checkConstraints(filmScope(card("c1", "buy gaffer tape")), nil, 0, time.Now())
	if !strings.Contains(out, "NOTHING TO REPORT") {
		t.Errorf("the checker invented a finding on a board with no dates:\n%s", out)
	}
	if !strings.Contains(out, "not the same as") {
		t.Errorf("silence is being reported as approval:\n%s", out)
	}
}
