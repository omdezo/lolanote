package agent

import (
	"strings"
	"testing"
	"time"
)

// DF26 — sunrise and sunset are load-bearing production facts, and every EXT/DAY
// scene is a race against one number. The arithmetic shipped computing that
// number for Muscat and only for Muscat, which is right for most of this
// workspace's work and quietly wrong for the rest.

func TestPlaces_SalalahIsNotMuscat(t *testing.T) {
	// Salalah is 4.3° of longitude west of Muscat — about 17 minutes of clock on
	// its own — and 6.6° south of it, which lengthens or shortens the half-day
	// depending on the season. The two effects add at midsummer SUNRISE and at
	// midwinter SUNSET, and that is where half an hour lives.
	for _, c := range []struct {
		when time.Time
		what string
	}{
		{time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), "rise"},
		{time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), "set"},
	} {
		mRise, mSet, ok1 := sunTimes(muscatLat, muscatLng, c.when)
		sRise, sSet, ok2 := sunTimes(17.019, 54.089, c.when)
		if !ok1 || !ok2 {
			t.Fatalf("the ephemeris refused %s in Oman", c.when.Format("Jan 2"))
		}
		gap := sRise.Sub(mRise)
		if c.what == "set" {
			gap = sSet.Sub(mSet)
		}
		if gap < 0 {
			gap = -gap
		}
		if gap < 25*time.Minute {
			t.Errorf("on %s Salalah and Muscat %s only %v apart — the location is not "+
				"reaching the arithmetic, and half an hour of light is a setup",
				c.when.Format("2 Jan"), c.what, gap)
		}
	}
}

// The absolute answer, not just the difference: an ephemeris that is internally
// consistent and thirty minutes out is worse than none, because a call sheet is
// trusted on sight.
func TestPlaces_MuscatMidsummerMatchesTheAlmanac(t *testing.T) {
	loc := workspaceLocation("Asia/Muscat")
	rise, set, ok := sunTimes(muscatLat, muscatLng, time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("no sunrise on the longest day of the year in Muscat")
	}
	// Published Muscat values for the June solstice are about 05:21 and 19:00.
	if got := rise.In(loc).Format("15:04"); got < "05:16" || got > "05:26" {
		t.Errorf("midsummer sunrise in Muscat computed as %s, which is not the almanac's 05:21", got)
	}
	if got := set.In(loc).Format("15:04"); got < "18:52" || got > "19:05" {
		t.Errorf("midsummer sunset in Muscat computed as %s, which is not the almanac's 19:00", got)
	}
}

func TestPlaces_TheCardsOwnLocationIsUsed(t *testing.T) {
	s := filmScope(
		card("c1", "42 EXT. MUGHSAIL BEACH – DAY — Salalah, 2026-08-12, 1 4/8"),
		card("c2", "Call sheet day 9"),
	)
	out := checkConstraints(s, nil, 0, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "DAYLIGHT c1") {
		t.Fatalf("a dated EXT/DAY scene got no daylight window at all:\n%s", out)
	}
	if !strings.Contains(out, "Salalah") {
		t.Errorf("the scene says Salalah and the sun was computed somewhere else:\n%s", out)
	}
	if strings.Contains(out, "that is MUSCAT") {
		t.Errorf("a located scene was still reported as an assumption:\n%s", out)
	}
}

// The fallback has to be audible. A sunset computed for the wrong place looks
// exactly like one computed for the right place, and the only defence is saying
// which it was.
func TestPlaces_AnUnlocatedSceneSaysItAssumedMuscat(t *testing.T) {
	scope := filmScope(
		card("c1", "7 EXT. COURTYARD – DAY — 2026-08-12"),
		card("c2", "Shot list for the courtyard"),
	)
	// The board title must not accidentally supply a location for this case.
	scope.Board.Content["title"] = "Untitled feature — schedule"
	out := checkConstraints(scope, nil, 0, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(out, "that is MUSCAT") {
		t.Errorf("the daylight window was defaulted to Muscat silently:\n%s", out)
	}
}

// Pasted coordinates beat a name, because somebody who pasted them meant that
// exact spot.
func TestPlaces_StatedCoordinatesWin(t *testing.T) {
	name, lat, _, ok := placeFor("Recce: Muscat office, actual site 17.0190, 54.0890")
	if !ok {
		t.Fatal("a decimal coordinate pair was not read")
	}
	if name != "stated coordinates" || lat < 16.9 || lat > 17.1 {
		t.Errorf("the gazetteer overrode a stated coordinate: got %s / %.4f", name, lat)
	}
}

// Not a geocoder. An unknown place must not resolve to something plausible.
func TestPlaces_AnUnknownPlaceIsNotInvented(t *testing.T) {
	if name, _, _, ok := placeFor("EXT. HELSINKI HARBOUR – DAY"); ok {
		t.Errorf("the gazetteer resolved a place it does not have, as %q", name)
	}
}
