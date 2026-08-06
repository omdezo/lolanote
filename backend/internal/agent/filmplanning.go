package agent

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Production arithmetic, done by the server.
//
// Three things a 1st AD does that the agent could not do at all, and all three
// are arithmetic the model is measurably bad at:
//
//   - BACKWARD PLANNING. "We shoot on 14 November" is the input; every other
//     date is derived. A colleague hears that and says "then your drone permit
//     application is already late." The prompt correctly forbids inventing
//     FACTS — but a derived date is not an invented fact, it is subtraction on
//     a stated one, and the two had been conflated.
//   - CONSTRAINT CHECKING. A schedule is not a list, it is a SOLUTION. The
//     value a person adds is finding the arrangement that satisfies every
//     constraint; the value software adds is catching the violation the human
//     missed. Everything here is read-only and every violation it reports is a
//     server-stated fact with its source attached, so the model REPORTS rather
//     than asserts.
//   - PAGE EIGHTHS. The trade converts script to time in eighths of a page, and
//     "2 6/8 + 1 5/8" is exactly the mixed-number arithmetic language models get
//     wrong. Without it the agent cannot answer the two questions a producer
//     asks most: can we shoot this in eighteen days, and how far behind are we.
//
// And one thing the ordering primitives got backwards: SHOOTING ORDER IS NOT
// STORY ORDER. A timeline lays items along their dates; a stripboard ASSIGNS
// the dates by solving a constraint problem, and the dates are the output. An
// arrange-by-date over scenes 1..64 returns the script, confidently, and
// answers a question nobody asked.

// ---- page eighths ----------------------------------------------------------

// eighthsPattern matches the page counts the trade writes: "4 2/8", "2/8",
// "1 5/8", and a bare page count like "3 pages" or "3 pgs".
//
// Deliberately not a general fraction parser. "1/2" on a card means a half of
// something that is probably not a script page, and treating it as 4/8 would
// silently corrupt the only number the daily report is judged on.
var eighthsPattern = regexp.MustCompile(`(?i)\b(?:(\d+)\s+)?(\d)/8\b|\b(\d+(?:\.\d+)?)\s*(?:pages?|pgs?)\b`)

// parseEighths totals every page count in a piece of text, in eighths.
//
// Returns the total and how many separate counts it found, because "I found no
// page counts" and "I found page counts totalling zero" are different answers
// and only one of them is a reason to say so.
func parseEighths(text string) (total int, found int) {
	for _, m := range eighthsPattern.FindAllStringSubmatch(text, -1) {
		switch {
		case m[2] != "":
			whole := 0
			if m[1] != "" {
				whole, _ = strconv.Atoi(m[1])
			}
			eighth, _ := strconv.Atoi(m[2])
			total += whole*8 + eighth
			found++
		case m[3] != "":
			pages, err := strconv.ParseFloat(m[3], 64)
			if err != nil {
				continue
			}
			total += int(math.Round(pages * 8))
			found++
		}
	}
	return total, found
}

// formatEighths renders a count of eighths the way a production report does.
func formatEighths(eighths int) string {
	if eighths == 0 {
		return "0"
	}
	whole, rem := eighths/8, eighths%8
	switch {
	case rem == 0:
		return fmt.Sprintf("%d", whole)
	case whole == 0:
		return fmt.Sprintf("%d/8", rem)
	default:
		return fmt.Sprintf("%d %d/8", whole, rem)
	}
}

// ---- sluglines -------------------------------------------------------------

// sluglinePattern reads a scene heading. The scene NUMBER is optional and
// captured separately because it is the production's join key — breakdown,
// stripboard, DOOD, call sheet and editorial all match on it — and an inserted
// scene carries a letter (an A-scene), which is why the letter is part of the
// identifier rather than noise after it.
var sluglinePattern = regexp.MustCompile(
	`(?i)^\s*(\d+[A-Z]?)?\s*[.:\-—]?\s*(INT\./EXT\.|INT/EXT|I/E|INT\.?|EXT\.?)\s+(.+?)\s*[-–—]\s*(DAY|NIGHT|DAWN|DUSK|MORNING|EVENING|CONTINUOUS|LATER)\b`)

// scene is one parsed slugline, with the id of the card it came off.
type scene struct {
	ElementID string
	Number    string
	IntExt    string
	Set       string
	DayNight  string
	Eighths   int
	Raw       string
}

// parseScene reads a slugline out of a card's text, or reports that there is
// not one.
func parseScene(id, text string) (scene, bool) {
	first := text
	if i := strings.IndexAny(text, "\n"); i >= 0 {
		first = text[:i]
	}
	m := sluglinePattern.FindStringSubmatch(strings.TrimSpace(first))
	if m == nil {
		return scene{}, false
	}
	eighths, _ := parseEighths(text)
	ie := strings.ToUpper(strings.TrimRight(m[2], "."))
	if ie == "I/E" || ie == "INT./EXT." || ie == "INT/EXT" {
		ie = "INT/EXT"
	}
	return scene{
		ElementID: id,
		Number:    strings.ToUpper(m[1]),
		IntExt:    ie,
		Set:       strings.ToUpper(strings.TrimSpace(m[3])),
		DayNight:  strings.ToUpper(m[4]),
		Eighths:   eighths,
		Raw:       strings.TrimSpace(first),
	}, true
}

// scenesIn collects every slugline the run can see, in reading order.
func scenesIn(s *BoardScope, only []string) []scene {
	want := map[string]bool{}
	for _, id := range only {
		want[id] = true
	}
	var out []scene
	for _, it := range s.Items {
		if len(want) > 0 && !want[it.ID] {
			continue
		}
		if sc, ok := parseScene(it.ID, it.Text); ok {
			out = append(out, sc)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ElementID < out[j].ElementID })
	return out
}

// ---- regroup: shooting order --------------------------------------------

// regroupAxes are the ways a shoot is actually ordered. Story order is not one
// of them, and that is the entire point of this verb existing beside arrange.
var regroupAxes = []string{"location", "dayNight", "cast", "set"}

// regroupScenes buckets scene-shaped material and says WHY.
//
// The reasoning sentence is not decoration: "grouped by location to remove two
// company moves" is the sentence a producer checks the plan against, and a
// grouping delivered without it is indistinguishable from a sort.
func regroupScenes(scenes []scene, by string) (string, bool) {
	if len(scenes) == 0 {
		return "", false
	}
	key := func(sc scene) string {
		switch strings.ToLower(by) {
		case "daynight":
			return sc.DayNight
		case "cast":
			// Cast is not on the slugline, so the honest grouping key is the set
			// — the scenes sharing a set are the scenes sharing a company — and
			// the caller is told that rather than being handed a fabricated
			// cast list.
			return sc.Set
		default: // location, set
			return sc.Set
		}
	}
	buckets := map[string][]scene{}
	var order []string
	for _, sc := range scenes {
		k := key(sc)
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], sc)
	}
	sort.Strings(order)

	// Company moves, counted honestly: how many times the set changes if the
	// scenes are shot in the order they are written, against how many times it
	// changes after grouping. That difference is the whole argument.
	movesBefore := 0
	for i := 1; i < len(scenes); i++ {
		if scenes[i].Set != scenes[i-1].Set {
			movesBefore++
		}
	}
	movesAfter := len(order) - 1

	var b strings.Builder
	fmt.Fprintf(&b, "REGROUPED %d scene(s) by %s — this is SHOOTING order, not story order.\n",
		len(scenes), by)
	totalEighths := 0
	for _, k := range order {
		group := buckets[k]
		e := 0
		for _, sc := range group {
			e += sc.Eighths
		}
		totalEighths += e
		names := make([]string, 0, len(group))
		for _, sc := range group {
			label := sc.Number
			if label == "" {
				label = sc.ElementID
			}
			names = append(names, fmt.Sprintf("%s (%s/%s)", label, sc.IntExt, sc.DayNight))
		}
		fmt.Fprintf(&b, "  %s — %d scene(s)", k, len(group))
		if e > 0 {
			fmt.Fprintf(&b, ", %s pages", formatEighths(e))
		}
		fmt.Fprintf(&b, ": %s\n", strings.Join(names, ", "))
	}
	if totalEighths > 0 {
		fmt.Fprintf(&b, "TOTAL: %s pages across %d group(s).\n", formatEighths(totalEighths), len(order))
	}
	fmt.Fprintf(&b, "COMPANY MOVES: shooting in the written order changes location %d time(s); "+
		"this grouping changes it %d time(s).\n", movesBefore, movesAfter)
	b.WriteString("Stage the moves yourself in this order — reorder or move_element — and say " +
		"in your summary why the order changed. Do NOT present this as the story order: " +
		"the script order is what the writer reads and this is what the unit shoots.\n")
	return b.String(), true
}

// ---- backward planning -----------------------------------------------------

// knownLeadDays are the lead times the domain pack already knows, so a run does
// not have to be told that a drone permit takes eight weeks in order to plan
// around it. Worst realistic case rather than the published minimum: a plan
// built on the stated 10 business days is a plan that is already late, which is
// exactly the failure the pack's own permit fact describes.
var knownLeadDays = []struct {
	Match []string
	Days  int
	Why   string
}{
	{[]string{"drone", "uav"}, 56, "CAA + Defence approval, 6–8 weeks in practice"},
	{[]string{"aerial", "helicopter"}, 21, "aerial permit, 15–21 days"},
	{[]string{"filming permit", "general filming", "gfp", "shooting permit", "permit"}, 21,
		"General Filming Permit, Ministry of Information — 10 business days stated, 2–3 weeks normal"},
	{[]string{"customs", "carnet", "temporary import", "kit list"}, 21,
		"temporary import rides on the filming permit, with a 10% deposit on kit value"},
	{[]string{"visa"}, 14, "crew visas via evisa.rop.gov.om"},
	{[]string{"heritage", "protected area", "archaeological"}, 21,
		"Ministry of Heritage & Tourism approval"},
	{[]string{"insurance", "e&o"}, 14, "cover has to be bound before the first day"},
	{[]string{"tech scout", "recce"}, 10, "tech scout with department heads, 1–2 weeks out"},
	{[]string{"call sheet"}, 1, "issued the day before"},
}

// leadFor resolves a step's lead time: what the model stated, or what the
// domain pack knows, or nothing.
func leadFor(name string, stated int) (int, string) {
	if stated > 0 {
		return stated, ""
	}
	low := strings.ToLower(name)
	for _, k := range knownLeadDays {
		for _, m := range k.Match {
			if strings.Contains(low, m) {
				return k.Days, k.Why
			}
		}
	}
	return 0, ""
}

// backwardStep is one item in a plan built from a fixed date.
type backwardStep struct {
	Name     string
	LeadDays int
}

// scheduleBackward computes the date each step has to start by, against a fixed
// anchor and against today.
//
// The "already late" line is the reason this is worth a tool. Anybody can
// subtract; the sentence a production manager says is "your drone permit
// application was due three weeks ago", and that requires knowing today.
func scheduleBackward(anchor time.Time, steps []backwardStep, now time.Time, zone string) string {
	loc := workspaceLocation(zone)
	anchor = anchor.In(loc)
	type row struct {
		name  string
		by    time.Time
		lead  int
		why   string
		late  bool
		close bool
	}
	rows := make([]row, 0, len(steps))
	unknown := make([]string, 0, 2)
	for _, st := range steps {
		lead, why := leadFor(st.Name, st.LeadDays)
		if lead <= 0 {
			unknown = append(unknown, st.Name)
			continue
		}
		by := anchor.AddDate(0, 0, -lead)
		rows = append(rows, row{
			name: st.Name, by: by, lead: lead, why: why,
			late:  by.Before(now.In(loc)),
			close: !by.Before(now.In(loc)) && by.Sub(now.In(loc)) < 7*24*time.Hour,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].by.Before(rows[j].by) })

	var b strings.Builder
	fmt.Fprintf(&b, "BACKWARD PLAN from %s (%s). Today is %s there.\n",
		anchor.Format("Mon 2 Jan 2006"), zoneName(zone), now.In(loc).Format("Mon 2 Jan 2006"))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s — start by %s (%d days before)", r.name, r.by.Format("2006-01-02"), r.lead)
		if r.why != "" {
			fmt.Fprintf(&b, " [%s]", r.why)
		}
		switch {
		case r.late:
			fmt.Fprintf(&b, " ⚠ ALREADY LATE by %d day(s)",
				int(now.In(loc).Sub(r.by).Hours()/24))
		case r.close:
			b.WriteString(" ⚠ due within the week")
		}
		b.WriteString("\n")
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "NO LEAD TIME KNOWN for: %s — put these in unmet or ask, rather than "+
			"inventing a duration.\n", strings.Join(unknown, ", "))
	}
	b.WriteString("These dates are ARITHMETIC on the anchor, not facts about the world: state " +
		"the anchor when you write them down so the person can re-derive them if the date " +
		"moves. Stage the tasks with these dates in their text.\n")
	return b.String()
}

// zoneName is the zone as a person would say it, or "UTC" for an unset one.
func zoneName(zone string) string {
	if strings.TrimSpace(zone) == "" {
		return "UTC"
	}
	return zone
}

// ---- sun times -------------------------------------------------------------

// muscatLat / muscatLng are the default coordinates for sun arithmetic, because
// this workspace's shoots happen here and asking a model for a latitude is
// asking it to guess.
const (
	muscatLat = 23.588
	muscatLng = 58.408
)

// sunTimes computes sunrise and sunset for a date and place, closed-form.
//
// Deliberately computed rather than fetched: sunrise for a lat/long and a date
// is arithmetic, so it rides no quota, needs no external service and carries no
// ⟨web⟩ label. WEATHER is the opposite case and must stay one — it cannot be
// derived from anything and an agent that states tomorrow's temperature has
// invented it.
//
// The standard sunrise equation, with the −0.833° solar-disc-and-refraction
// correction that makes "sunrise" mean what a call sheet means by it.
func sunTimes(lat, lng float64, day time.Time) (rise, set time.Time, ok bool) {
	const rad = math.Pi / 180
	// Julian day for midnight UTC of the requested civil date.
	y, m, d := day.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	jDate := float64(midnight.Unix())/86400.0 + 2440587.5

	// Mean solar noon. The sign here is the whole thing: at an EAST longitude the
	// sun crosses earlier in UT, so the offset is minus lng/360 of a day — Muscat
	// at 58.4°E transits at about 08:08 UT, which is 12:08 on a Muscat clock.
	// Getting it backwards produces times that look like times and are seven
	// hours out, which is exactly the class of error this whole corner is about.
	// Days since J2000. Named for what it is rather than `d`, which is already
	// the day-of-month from day.Date() a few lines up — the collision is what
	// left this file unbuildable.
	sinceJ2000 := jDate - 2451545.0
	n := math.Round(sinceJ2000 - 0.0009 + lng/360)
	jStar := n + 0.0009 - lng/360
	mAnom := math.Mod(357.5291+0.98560028*jStar, 360)
	c := 1.9148*math.Sin(mAnom*rad) + 0.0200*math.Sin(2*mAnom*rad) + 0.0003*math.Sin(3*mAnom*rad)
	lambda := math.Mod(mAnom+c+180+102.9372, 360)
	jTransit := 2451545.0 + jStar + 0.0053*math.Sin(mAnom*rad) - 0.0069*math.Sin(2*lambda*rad)
	sinDecl := math.Sin(lambda*rad) * math.Sin(23.44*rad)
	decl := math.Asin(sinDecl)
	cosOmega := (math.Sin(-0.833*rad) - math.Sin(lat*rad)*sinDecl) /
		(math.Cos(lat*rad) * math.Cos(decl))
	if cosOmega < -1 || cosOmega > 1 {
		// Midnight sun or polar night. Not a case Oman ever meets, and the
		// honest answer is "there is no sunrise on this date here" rather than
		// a NaN that reads as a time.
		return time.Time{}, time.Time{}, false
	}
	omega := math.Acos(cosOmega) / rad
	jSet := jTransit + omega/360
	jRise := jTransit - omega/360
	toTime := func(j float64) time.Time {
		return time.Unix(int64(math.Round((j-2440587.5)*86400)), 0).UTC()
	}
	return toTime(jRise), toTime(jSet), true
}

// ---- the constraint checker ------------------------------------------------

// omanBanStart / omanBanEnd are Ministerial Resolution 286's prohibited window,
// as minutes past midnight local.
const (
	omanBanStart = 12*60 + 30
	omanBanEnd   = 15*60 + 30
)

// ramadanWindows are the Ramadan and Eid periods this pack knows about.
//
// Approximate by nature — the month begins on a moon sighting — so they are
// stated as approximate and dated. An agent that says "Ramadan begins on the
// 18th" with certainty is wrong about the one property of the Hijri calendar
// everybody in the region already knows.
var ramadanWindows = []struct {
	Label      string
	Start, End string // inclusive, ISO dates
}{
	{"Ramadan 1447", "2026-02-17", "2026-03-19"},
	{"Eid al-Fitr 1447", "2026-03-20", "2026-03-23"},
	{"Ramadan 1448", "2027-02-07", "2027-03-08"},
	{"Eid al-Fitr 1448", "2027-03-09", "2027-03-12"},
}

// datePattern finds ISO dates in card text. ISO only, on purpose: "3/8" is a
// page count in this trade and "05/06" is ambiguous in a country that writes
// both ways.
var datePattern = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)

// timePattern finds wall-clock times. Anchored on the colon so a page count, a
// scene number and a lens length are not read as times.
var timePattern = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)\b`)

// checkConstraints reports every violation it can prove, as a server-stated
// fact with its source.
//
// Read-only by construction, which is what makes it the safest thing in this
// corner: it changes nothing, it cites everything, and where it has nothing to
// say it says that too — a checker that always finds something is a checker
// people stop reading.
func checkConstraints(s *BoardScope, only []string, days int, now time.Time) string {
	var b strings.Builder
	scenes := scenesIn(s, only)
	findings := 0

	// Page load, and what it implies about the number of days.
	totalEighths := 0
	counted := 0
	for _, sc := range scenes {
		if sc.Eighths > 0 {
			totalEighths += sc.Eighths
			counted++
		}
	}
	if counted > 0 {
		fmt.Fprintf(&b, "PAGES: %s across %d scene(s) that carry a count",
			formatEighths(totalEighths), counted)
		if days > 0 {
			per := float64(totalEighths) / float64(days) / 8
			fmt.Fprintf(&b, "; over %d shooting day(s) that is %.1f pages a day", days, per)
			// 2–5 pages is the normal band for a feature day. Outside it the
			// schedule is a claim, not a plan, and saying so is the single most
			// useful sentence a scheduler can hear.
			switch {
			case per > 5:
				fmt.Fprintf(&b, " ⚠ above the 2–5 pages/day a feature day normally carries")
			case per < 2:
				fmt.Fprintf(&b, " (light — under the usual 2–5 pages/day)")
			}
		}
		b.WriteString("\n")
		findings++
	}

	// The three rules that are law or contract rather than taste.
	for _, it := range s.Items {
		if len(only) > 0 && !containsStr(only, it.ID) {
			continue
		}
		text := it.Text
		dates := datePattern.FindAllString(text, -1)
		if len(dates) == 0 {
			continue
		}
		sc, isScene := parseScene(it.ID, text)
		for _, ds := range dates {
			d, err := time.Parse("2006-01-02", ds)
			if err != nil {
				continue
			}
			// Ministerial Resolution 286: the legal stop.
			if inOmanBanSeason(d) {
				if isScene && strings.HasPrefix(sc.IntExt, "EXT") && sc.DayNight == "DAY" {
					fmt.Fprintf(&b, "VIOLATION %s: %s is EXT/DAY on %s — outdoor work in open "+
						"spaces is prohibited 12:30–15:30 from 1 June to 31 August "+
						"[Ministerial Resolution No. 286, as of %s]. This is a split day or a "+
						"night, not a full day.\n", it.ID, sc.Raw, ds, factsAsOf)
					findings++
				}
				for _, tm := range timePattern.FindAllStringSubmatch(text, -1) {
					h, _ := strconv.Atoi(tm[1])
					mn, _ := strconv.Atoi(tm[2])
					if mins := h*60 + mn; mins >= omanBanStart && mins <= omanBanEnd {
						fmt.Fprintf(&b, "VIOLATION %s: a call or working time of %s:%s on %s "+
							"falls inside the prohibited 12:30–15:30 outdoor window "+
							"[Ministerial Resolution No. 286, as of %s]. Fines are OMR 500–1,000.\n",
							it.ID, tm[1], tm[2], ds, factsAsOf)
						findings++
						break
					}
				}
			}
			// Ramadan and Eid.
			if label := ramadanLabelFor(d); label != "" {
				if strings.HasPrefix(label, "Ramadan") {
					fmt.Fprintf(&b, "CONSTRAINT %s: %s falls in %s (approximate — the month "+
						"begins on a moon sighting). Muslim employees are capped at 6 hours a "+
						"day and 36 a week against a 12-hour Omani set day, so this day carries "+
						"about half its normal work [Oman Labour Law, as of %s].\n",
						it.ID, ds, label, factsAsOf)
				} else {
					fmt.Fprintf(&b, "CONSTRAINT %s: %s falls in %s — the country closes for "+
						"several days [as of %s].\n", it.ID, ds, label, factsAsOf)
				}
				findings++
			}
			// Daylight, for the scenes that race it.
			if isScene && strings.HasPrefix(sc.IntExt, "EXT") && sc.DayNight == "DAY" {
				// WHERE, not just when. The ephemeris used to be Muscat's for
				// every scene on every board, which is right for most of this
				// workspace's work and half an hour wrong in Salalah — and half
				// an hour is a setup. The place is resolved off the card, then
				// the board, then defaulted, and which of the three it was is
				// printed: a sunset computed for the wrong place looks exactly
				// like one computed for the right place.
				place, lat, lng, assumed := placeForRun(s, text)
				if rise, set, ok := sunTimes(lat, lng, d); ok {
					loc := workspaceLocation(s.Timezone)
					fmt.Fprintf(&b, "DAYLIGHT %s: on %s the sun is up %s–%s local at %s "+
						"(%.3f, %.3f, computed) — that is the whole window for this EXT/DAY "+
						"scene, less the 12:30–15:30 stop in summer.", it.ID, ds,
						rise.In(loc).Format("15:04"), set.In(loc).Format("15:04"),
						place, lat, lng)
					if assumed {
						b.WriteString(" Nothing on this card names a location, so that is " +
							"MUSCAT — say so, or give me the place and I will recompute.")
					}
					b.WriteString("\n")
					findings++
				}
			}
		}
	}

	findings += doodTotals(&b, s, only)
	findings += arabicSubtitleLens(&b, s, only)
	// Periods, last, because everything above is about a single day and this is
	// what a day sits inside: an availability window, a permit's validity, the
	// rest between one wrap and the next call.
	findings += spanFindings(&b, s, only)

	if findings == 0 {
		b.WriteString("NOTHING TO REPORT. No dated scene, page count, call time or date range on " +
			"this board trips a rule I can check. That is not the same as 'the schedule is " +
			"fine': I check the outdoor-work window, Ramadan hours, daylight for EXT/DAY " +
			"scenes, the page load, availability windows and turnaround, and I check them " +
			"only where a date is written as YYYY-MM-DD.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "CHECKED: %s. WEATHER IS NOT HERE — it cannot be derived and I will not "+
		"invent it; put it in unmet or fetch it and label it.\n",
		strings.Join(sortedTopics(omanFacts), ", "))
	return b.String()
}

// doodCodes is the Day Out of Days alphabet. Seven tokens, fixed, and the whole
// value of the report is what they add up to.
var doodCodes = map[string]string{
	"SW":  "start work",
	"W":   "work",
	"WF":  "work finish",
	"SWF": "start-work-finish",
	"H":   "hold",
	"T":   "travel",
	"R":   "rehearsal",
}

// doodHeaders are the first-column headings that say a table is a DOOD.
var doodHeaders = []string{"cast", "performer", "artist", "actor", "character"}

// doodLegend spells the alphabet out beside the totals, because a report of
// "4 work, 6 hold" is only checkable if the reader can see which cells produced
// it.
func doodLegend() string {
	keys := make([]string, 0, len(doodCodes))
	for k := range doodCodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+doodCodes[k])
	}
	return strings.Join(parts, ", ")
}

// doodTotals reads a Day Out of Days matrix and states what it costs.
//
// This is where scheduling meets money and it is the one number nobody can see
// by looking: a DOOD tells you an actor is being paid for eleven HOLD days
// because two of their scenes sit at opposite ends of the schedule, and it is
// the report that justifies reordering a whole shoot. Its value is entirely in
// the arithmetic, which is exactly the thing to do server-side — the server
// states "Layla: 4 work, 6 hold" and the model decides what that means.
func doodTotals(b *strings.Builder, s *BoardScope, only []string) int {
	found := 0
	for _, it := range s.Items {
		if len(only) > 0 && !containsStr(only, it.ID) {
			continue
		}
		el := s.Elements[it.ID]
		if el == nil {
			continue
		}
		rows := toRows(el.Content["cells"])
		if len(rows) < 2 || len(rows[0]) < 3 {
			continue
		}
		if !containsStr(doodHeaders, strings.ToLower(strings.TrimSpace(rows[0][0]))) {
			continue
		}
		reported := false
		for _, row := range rows[1:] {
			if len(row) < 2 || strings.TrimSpace(row[0]) == "" {
				continue
			}
			counts := map[string]int{}
			cells := 0
			for _, cell := range row[1:] {
				code := strings.ToUpper(strings.TrimSpace(cell))
				if _, ok := doodCodes[code]; !ok {
					continue
				}
				counts[code]++
				cells++
			}
			if cells == 0 {
				continue
			}
			if !reported {
				fmt.Fprintf(b, "DAY OUT OF DAYS %s (%s):\n", it.ID, doodLegend())
				reported = true
			}
			work := counts["W"] + counts["SW"] + counts["WF"] + counts["SWF"]
			// Hold days are the point. An actor on hold is not used and is still
			// paid, so this is the line a producer acts on.
			fmt.Fprintf(b, "  %s — %d work, %d hold, %d travel, %d rehearsal",
				strings.TrimSpace(row[0]), work, counts["H"], counts["T"], counts["R"])
			if counts["H"] > 0 && work > 0 {
				fmt.Fprintf(b, " ⚠ paid for %d day(s) they are not used", counts["H"])
			}
			b.WriteString("\n")
			found++
		}
	}
	return found
}

// subtitleWords mark material the Arabic delivery spec actually applies to.
//
// Narrow on purpose. The 42-character rule is a SUBTITLE spec, and applying it
// to every Arabic note on the board would produce a wall of complaints about
// ordinary prose — which is how a genuinely differentiating check becomes noise
// people switch off.
var subtitleWords = []string{"subtitle", "caption", "sdh", "srt", "ترجمة", "الترجمة"}

// arabicMaxLineChars is the per-line ceiling in the Arabic timed-text spec.
const arabicMaxLineChars = 42

// arabicSubtitleLens checks Arabic subtitle text against the numbers it is QC'd
// on.
//
// The one place where being bilingual is a commercial advantage rather than a
// UI problem: for a filmmaker here Arabic is a DELIVERABLE with a spec sheet,
// and a line over 42 characters or a third line is a QC bounce. Counted in
// RUNES, because Arabic is multi-byte and a byte count would pass every line
// that fails.
func arabicSubtitleLens(b *strings.Builder, s *BoardScope, only []string) int {
	found := 0
	for _, it := range s.Items {
		if len(only) > 0 && !containsStr(only, it.ID) {
			continue
		}
		low := strings.ToLower(it.Text)
		if !anyContains(low, subtitleWords) {
			continue
		}
		if !hasArabic(it.Text) {
			continue
		}
		lines := strings.Split(it.Text, "\n")
		block := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				block = 0
				continue
			}
			block++
			if n := len([]rune(line)); n > arabicMaxLineChars {
				fmt.Fprintf(b, "ARABIC SPEC %s: a line is %d characters — the limit is %d "+
					"per line, two lines maximum, 20 CPS adult reading speed [regional "+
					"Arabic subtitling style guides, as of %s]\n", it.ID, n, arabicMaxLineChars, factsAsOf)
				found++
			}
			if block == 3 {
				fmt.Fprintf(b, "ARABIC SPEC %s: three or more lines in one subtitle block — "+
					"the spec allows two [as of %s]\n", it.ID, factsAsOf)
				found++
			}
		}
		if strings.Contains(it.Text, "*") || strings.Contains(it.Text, "_") {
			fmt.Fprintf(b, "ARABIC SPEC %s: italics are NOT used in Arabic subtitles — if that "+
				"emphasis is deliberate, it has to be carried another way [as of %s]\n",
				it.ID, factsAsOf)
			found++
		}
	}
	return found
}

// anyContains is the substring test the lens and the trigger share.
func anyContains(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// hasArabic reports whether any rune sits in the Arabic blocks.
func hasArabic(s string) bool {
	for _, r := range s {
		if (r >= 0x0600 && r <= 0x06FF) || (r >= 0x0750 && r <= 0x077F) ||
			(r >= 0xFB50 && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF) {
			return true
		}
	}
	return false
}

// inOmanBanSeason reports whether a date sits in the 1 June – 31 August window.
func inOmanBanSeason(d time.Time) bool {
	m := d.Month()
	return m == time.June || m == time.July || m == time.August
}

// ramadanLabelFor names the window a date falls in, or "".
func ramadanLabelFor(d time.Time) string {
	iso := d.Format("2006-01-02")
	for _, w := range ramadanWindows {
		if iso >= w.Start && iso <= w.End {
			return w.Label
		}
	}
	return ""
}

// ---- fixed-shape artefacts -------------------------------------------------

// dayColumnPattern matches a container title that is a SHOOTING DAY: "Day 1",
// "DAY 12", "D3", or a bare ISO date.
var dayColumnPattern = regexp.MustCompile(`(?i)^\s*(day\s*\d+|d\s?\d+|\d{4}-\d{2}-\d{2})\b`)

// fixedShapeColumns is how many day-shaped containers it takes before a board
// is a schedule rather than a wall.
//
// Five, because three or four day columns could be a kanban that happens to be
// named oddly, and five in sequence is a shooting schedule. The threshold has to
// sit BELOW the wall warning's own (eight) or the exemption never fires on the
// board it exists for.
const fixedShapeColumns = 5

// fixedShapeNames reports the trade document a set of container titles is,
// or "" when they are just columns.
//
// This is the escape hatch for a genuine conflict between the product's taste
// and the domain: "three to six columns reads well, past seven you are building
// a wall" is correct for a board of ideas and WRONG for a shooting schedule,
// which legitimately has one column per day for eighteen to thirty days. Without
// the exemption the product's own quality machinery flags the right answer, and
// the ambient hint offers to destroy a schedule.
func fixedShapeNames(titles []string) string {
	dayish := 0
	for _, t := range titles {
		if dayColumnPattern.MatchString(t) {
			dayish++
		}
	}
	if dayish >= fixedShapeColumns {
		return fmt.Sprintf("a shooting schedule (%d day columns) — a document whose shape the "+
			"trade decided, not a wall", dayish)
	}
	return ""
}

// boardFixedShape reports whether the BOARD is already one of those documents.
func boardFixedShape(s *BoardScope) string {
	if s == nil {
		return ""
	}
	return fixedShapeNames(s.ExistingColumns)
}

// plannedFixedShape reports whether the PLAN is building one.
//
// Measured over the plan's own container titles rather than over the board,
// because the case that matters is the run that is right now creating a
// thirty-day schedule and is about to be told off for it.
func plannedFixedShape(p *Plan) string {
	if p == nil {
		return ""
	}
	titles := make([]string, 0, len(p.Actions))
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind.Creates() && a.Kind.Container() && a.Title != "" {
			titles = append(titles, a.Title)
		}
	}
	return fixedShapeNames(titles)
}

// ---- the identifier guard --------------------------------------------------

// sceneIDPattern is a leading production identifier: a scene number with an
// optional insert letter, followed by a separator. "3 INT. HARBOUR OFFICE" and
// "12A — Chase" both match; "2026 budget" does not, because a four-digit year
// is not a scene number.
var sceneIDPattern = regexp.MustCompile(`^(\d{1,3}[A-Za-z]?)\s*[\s.:\-—]`)

// accountCodePattern is a budget account code — "2100 Camera", "21.03 Lenses".
var accountCodePattern = regexp.MustCompile(`^(\d{2,4}(?:\.\d{1,2})?)\s`)

// leadingIdentifier returns the canonical identifier a title starts with, or "".
func leadingIdentifier(title string) string {
	t := strings.TrimSpace(title)
	if m := accountCodePattern.FindStringSubmatch(t); m != nil {
		if strings.Contains(m[1], ".") {
			return m[1]
		}
	}
	if m := sceneIDPattern.FindStringSubmatch(t); m != nil {
		return m[1]
	}
	return ""
}

// dropsIdentifier reports whether a rename would strip the join key off a title.
//
// The rule this guards against was written as a style instruction — "name the
// category, not the item: \"Data Chip\", not \"Scene 3: The Data Chip\"" — and
// it is a live wrong aimed at exactly this user. Scene numbers are LOCKED at
// the shooting script and never renumbered, precisely because breakdown,
// stripboard, DOOD, call sheet, lined script, camera and sound reports and
// editorial all key off them. Strip "3" off a card and the only join the
// production has is severed: the cards become unschedulable and unmatchable to
// the script, and nothing anywhere says why.
//
// A guard rather than another prompt sentence, because a prompt line loses to a
// strong aesthetic instruction three paragraphs earlier — which is how this got
// written in the first place.
func dropsIdentifier(oldTitle, newTitle string) (string, bool) {
	id := leadingIdentifier(oldTitle)
	if id == "" {
		return "", false
	}
	if leadingIdentifier(newTitle) == id {
		return "", false
	}
	// Present anywhere in the new title is not good enough for the SCHEDULE —
	// the sort is on the leading token — but it is honest enough not to refuse:
	// somebody who wrote "Harbour Office (Scene 3)" kept the fact. Only a title
	// that has lost the identifier entirely is refused.
	if strings.Contains(newTitle, id) {
		return "", false
	}
	return id, true
}
