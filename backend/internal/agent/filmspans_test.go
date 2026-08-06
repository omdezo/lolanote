package agent

import (
	"strings"
	"testing"
	"time"
)

// DF38 — the shoot has a physical rhythm and everything the product knows about
// time is an INSTANT. reminderAt, dueDate and set_reminder's `when` are all
// points, so the agent could say when something starts and never how long it
// lasts or when it collides — which is why the constraint checker had rules to
// state and nothing to check them against.

func TestSpans_APeriodIsMeasuredNotJustEchoed(t *testing.T) {
	s := filmScope(
		card("c1", "Wadi Shab block — shooting schedule\n2026-08-03 → 2026-08-14"),
		card("c2", "12 EXT. WADI SHAB – DAY"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "SPAN c1") || !strings.Contains(out, "12 day(s)") {
		t.Fatalf("a stated period was not measured — the arithmetic is the whole reason this "+
			"is server-side:\n%s", out)
	}
	// August is inside the 1 June – 31 August window, and every day of it counts.
	if !strings.Contains(out, "Ministerial Resolution No. 286") ||
		!strings.Contains(out, "12 of those 12 day(s)") {
		t.Errorf("a twelve-day August block never met the midday outdoor ban:\n%s", out)
	}
}

// The Ramadan half is the one with real money in it: half-length days mean the
// block needs more of them, and that number is arithmetic the model gets wrong.
func TestSpans_RamadanDaysAreCountedAndTurnedIntoDays(t *testing.T) {
	s := filmScope(
		card("c1", "Principal photography — 2026-02-15 → 2026-02-26"),
		card("c2", "Call sheet for day 1"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "fall in Ramadan") {
		t.Fatalf("a block crossing Ramadan 1447 was not flagged:\n%s", out)
	}
	// 17–26 Feb is ten days of the window, and at half a day each that is five
	// more shooting days.
	if !strings.Contains(out, "10 of those 12 day(s)") || !strings.Contains(out, "5 more") {
		t.Errorf("the Ramadan arithmetic is missing or wrong — 'some of it is in Ramadan' is "+
			"a fact anybody can state:\n%s", out)
	}
	if !strings.Contains(out, "moon sighting") {
		t.Errorf("the Hijri window was stated as certain, which is wrong about the one "+
			"property of the calendar everybody in the region knows:\n%s", out)
	}
}

// A WINDOW is a fence. Something dated outside it is the collision the checker
// existed for and had no way to see.
func TestSpans_SomethingDatedOutsideAWindowIsReported(t *testing.T) {
	s := filmScope(
		card("c1", "Layla available 2026-10-05 → 2026-10-12 (contracted)"),
		card("c2", "14 INT. HARBOUR OFFICE – NIGHT — scheduled 2026-10-20"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "OUTSIDE THE WINDOW on c1") || !strings.Contains(out, "c2 (2026-10-20)") {
		t.Fatalf("a scene scheduled eight days after the lead's contract ends was not "+
			"reported:\n%s", out)
	}
	// It must not claim to know that the scene DOES depend on the window.
	if !strings.Contains(out, "say which ones do") {
		t.Errorf("the finding overstates what the server knows — it can see the dates and not "+
			"the dependency:\n%s", out)
	}
}

// A plain range is not a fence: a shoot block with days outside it is not a
// violation, and reporting one would teach people to stop reading the checker.
func TestSpans_APlainRangeIsNotAFence(t *testing.T) {
	s := filmScope(
		card("c1", "Shooting schedule block 2026-10-05 → 2026-10-12"),
		card("c2", "Delivery 2026-12-01"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if strings.Contains(out, "OUTSIDE THE WINDOW") {
		t.Errorf("a range that declares no constraint was treated as one:\n%s", out)
	}
}

// Turnaround is the span nobody writes down and everybody feels: a late wrap
// costs the day AFTER it, and by then tomorrow's call sheet has gone out.
func TestSpans_AShortTurnaroundIsCaught(t *testing.T) {
	s := filmScope(
		card("c1", "Day 3 — 2026-10-05, shooting schedule\nCrew call 07:00\nEst. wrap 23:00"),
		card("c2", "Day 4 — 2026-10-06 call sheet\nCrew call 06:00"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "TURNAROUND c1 → c2") {
		t.Fatalf("a 23:00 wrap and an 06:00 call is seven hours of rest and went unreported:\n%s", out)
	}
	if !strings.Contains(out, "7 hours") {
		t.Errorf("the rest was not stated as a number, so nobody can check it:\n%s", out)
	}
	// The provenance discipline: this one is the trade's rule, not a ministry's,
	// and the difference has to be visible.
	if !strings.Contains(out, "not Omani statute") {
		t.Errorf("industry practice was presented with the same authority as a cited "+
			"regulation:\n%s", out)
	}
}

func TestSpans_AnHonestTurnaroundIsNotFlagged(t *testing.T) {
	s := filmScope(
		card("c1", "Day 3 — 2026-10-05, shooting schedule\nCrew call 07:00\nEst. wrap 19:00"),
		card("c2", "Day 4 — 2026-10-06 call sheet\nCrew call 07:00"),
	)
	if out := checkConstraints(s, nil, 0, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); strings.Contains(out, "TURNAROUND") {
		t.Errorf("twelve hours of turnaround is the normal shape of a shooting day and was "+
			"reported as a problem:\n%s", out)
	}
}

// The parser must not invent periods out of the trade's own notation: "3/8" is a
// page count and a scene number is not half a date range.
func TestSpans_PageCountsAreNotDateRanges(t *testing.T) {
	s := filmScope(
		card("c1", "12 EXT. WADI SHAB – DAY — 2 6/8 pages"),
		card("c2", "Shot list for scene 12"),
	)
	if got := spansIn(s, nil); len(got) != 0 {
		t.Errorf("the span parser invented %d period(s) out of page counts: %+v", len(got), got)
	}
}
