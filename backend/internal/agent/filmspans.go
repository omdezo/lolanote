package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spans: production plans are made of periods, and everything the product knows
// about time is an instant.
//
// reminderAt is an instant. dueDate is an instant. set_reminder takes an
// instant. So the agent could say when something STARTS and never how long it
// lasts or when it collides with something else — and a production is made of
// nothing but periods: a shoot block, an actor's contracted dates, a location's
// availability window, a permit's validity, a hold, a festival submission
// window, the turnaround between wrap and tomorrow's call. That is the quiet gap
// under half of this corner: the constraint checker had constraints to state and
// nothing to check them AGAINST.
//
// The honest reach today is the READ half. A span is not yet a field on an
// element — that is a schema change on a type this package does not own, and a
// {start,end} written into content that no renderer paints is the write-without-
// read failure this whole corner keeps finding. What a person DOES write is the
// range, in the card, in words: "Layla available 2026-08-03 → 2026-08-14". So the
// server reads it, does the arithmetic the model gets wrong, and states the
// result. When the field arrives, this parser becomes its fallback rather than
// its replacement.

// spanPattern reads a date range as a person writes one.
//
// ISO dates on both ends, deliberately: "3/8" is a page count in this trade and
// "05/06" is ambiguous in a country that writes dates both ways round, so a
// looser parser would invent periods out of scene notes.
var spanPattern = regexp.MustCompile(
	`(\d{4}-\d{2}-\d{2})\s*(?:→|->|–|—|-|to|until|till|through|حتى)\s*(\d{4}-\d{2}-\d{2})`)

// windowWords mark a span that DECLARES a constraint rather than merely
// describing a period.
//
// The difference decides whether anything is checked against it: "shoot block
// 3–14 Aug" is a statement about the plan, while "Layla available 3–14 Aug" is a
// fence, and a date outside a fence is a violation somebody has to hear about.
var windowWords = []string{
	"avail", "available", "window", "valid", "hold", "held", "access",
	"permit", "licence", "license", "contract", "contracted", "booked",
	"hire", "rental", "on loan", "blackout", "متاح", "تصريح",
}

// dateSpan is one period the board states, with the element that stated it.
type dateSpan struct {
	ElementID string
	Start     time.Time
	End       time.Time
	Days      int
	Window    bool
	Label     string
}

// spansIn collects every period the run can see.
func spansIn(s *BoardScope, only []string) []dateSpan {
	var out []dateSpan
	for _, it := range s.Items {
		if len(only) > 0 && !containsStr(only, it.ID) {
			continue
		}
		for _, line := range strings.Split(it.Text, "\n") {
			m := spanPattern.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, err1 := time.Parse("2006-01-02", m[1])
			end, err2 := time.Parse("2006-01-02", m[2])
			if err1 != nil || err2 != nil || end.Before(start) {
				continue
			}
			// Inclusive, because a person who writes "3 → 14 August" means both
			// ends: an exclusive count would report an eleven-day availability as
			// eleven days of work and lose the last day of it.
			days := int(end.Sub(start).Hours()/24) + 1
			out = append(out, dateSpan{
				ElementID: it.ID, Start: start, End: end, Days: days,
				Window: anyContains(strings.ToLower(line), windowWords),
				Label:  strings.TrimSpace(line),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].ElementID < out[j].ElementID
	})
	return out
}

// maxSpanDays stops a typo becoming a loop. A ten-year "span" is a mistyped
// year, not a shoot, and walking it day by day would spend the run's time
// proving it.
const maxSpanDays = 800

// contains reports whether a date sits inside the period, inclusive at both
// ends.
func (sp dateSpan) contains(d time.Time) bool {
	return !d.Before(sp.Start) && !d.After(sp.End)
}

// seasonLoad counts the days in a span that carry a statutory constraint.
func (sp dateSpan) seasonLoad() (ramadan, ban int) {
	if sp.Days > maxSpanDays {
		return 0, 0
	}
	for d := sp.Start; !d.After(sp.End); d = d.AddDate(0, 0, 1) {
		if strings.HasPrefix(ramadanLabelFor(d), "Ramadan") {
			ramadan++
		}
		if inOmanBanSeason(d) {
			ban++
		}
	}
	return ramadan, ban
}

// spanFindings reports what the periods on this board actually imply.
func spanFindings(b *strings.Builder, s *BoardScope, only []string) int {
	// Turnaround first, and unconditionally: it is a period nobody writes as a
	// range — it exists between two cards that each state one date — so an early
	// return on "this board declares no spans" would have skipped the one span
	// finding that needs no span written down at all.
	found := turnaroundFindings(b, s, only)
	spans := spansIn(s, only)
	if len(spans) == 0 {
		return found
	}
	for _, sp := range spans {
		fmt.Fprintf(b, "SPAN %s: %s → %s — %d day(s)", sp.ElementID,
			sp.Start.Format("2006-01-02"), sp.End.Format("2006-01-02"), sp.Days)
		if sp.Window {
			b.WriteString(", stated as a WINDOW")
		}
		b.WriteString("\n")
		found++

		ramadan, ban := sp.seasonLoad()
		if ramadan > 0 {
			// The arithmetic is the point. "Some of it is in Ramadan" is a fact
			// anybody can state; "6 of your 12 days are, at half a working day
			// each, so this block needs about 3 more days" is the sentence that
			// changes a schedule.
			fmt.Fprintf(b, "  %d of those %d day(s) fall in Ramadan — Muslim employees are "+
				"capped at 6 hours a day against a 12-hour Omani set day, so those days "+
				"carry about half their normal work and this block needs roughly %d more "+
				"day(s) [Oman Labour Law, as of %s; the month begins on a moon sighting, "+
				"so the window is approximate]\n", ramadan, sp.Days, (ramadan+1)/2, factsAsOf)
			found++
		}
		if ban > 0 {
			fmt.Fprintf(b, "  %d of those %d day(s) fall between 1 June and 31 August, when "+
				"outdoor work in open spaces is prohibited 12:30–15:30 — every EXT day in "+
				"there is a split day or a night [Ministerial Resolution No. 286, as of %s]\n",
				ban, sp.Days, factsAsOf)
			found++
		}
	}

	// A window with something outside it is the collision the checker existed for
	// and could never see, because it had one date and no period to measure it
	// against.
	for _, sp := range spans {
		if !sp.Window {
			continue
		}
		var outside []string
		for _, it := range s.Items {
			if it.ID == sp.ElementID {
				continue
			}
			if len(only) > 0 && !containsStr(only, it.ID) {
				continue
			}
			for _, ds := range datePattern.FindAllString(it.Text, -1) {
				d, err := time.Parse("2006-01-02", ds)
				if err != nil || sp.contains(d) {
					continue
				}
				// A date inside another span on the same card is part of that
				// card's own period, not a stray day sitting outside this window.
				if spanPattern.MatchString(it.Text) {
					continue
				}
				outside = append(outside, fmt.Sprintf("%s (%s)", it.ID, ds))
			}
		}
		if len(outside) == 0 {
			continue
		}
		if len(outside) > 6 {
			outside = append(outside[:6], fmt.Sprintf("and %d more", len(outside)-6))
		}
		fmt.Fprintf(b, "OUTSIDE THE WINDOW on %s (%q): %s. If those dated items depend on this "+
			"window, they cannot happen — say which ones do rather than assuming all of them "+
			"do.\n", sp.ElementID, truncate(sp.Label, 60), strings.Join(outside, ", "))
		found++
	}
	return found
}

// standardTurnaroundHours is the rest between wrap and the next day's call that
// the trade treats as the floor.
//
// Stated as PRACTICE and not as law, on purpose. Oman's labour law has no
// turnaround provision to cite, so this is the only rule in the pack the agent
// holds on the trade's authority rather than a ministry's, and saying which is
// which is the whole provenance discipline.
const standardTurnaroundHours = 11

// callWords and wrapWords are how a call sheet names the two ends of a day.
var (
	callWords = []string{"crew call", "call time", "call:", "call —", "call -", "general call"}
	wrapWords = []string{"wrap"}
)

// dayEdges is the call and wrap time read off one dated card.
type dayEdges struct {
	Date    time.Time
	Call    int // minutes past midnight, -1 when unstated
	Wrap    int
	Element string
}

// turnaroundFindings reports a following day that starts before the crew has
// rested.
//
// This is the span nobody writes down and everybody feels: a late wrap does not
// cost the day it happened on, it costs the day after, and by the time anybody
// notices, tomorrow's call sheet has already gone out.
func turnaroundFindings(b *strings.Builder, s *BoardScope, only []string) int {
	byDate := map[string]*dayEdges{}
	for _, it := range s.Items {
		if len(only) > 0 && !containsStr(only, it.ID) {
			continue
		}
		dates := datePattern.FindAllString(it.Text, -1)
		if len(dates) != 1 {
			// Two dates on one card is a range or a list, and neither says "this
			// is what happened on one day". Guessing would produce turnarounds
			// between days that were never adjacent.
			continue
		}
		d, err := time.Parse("2006-01-02", dates[0])
		if err != nil {
			continue
		}
		edges := byDate[dates[0]]
		if edges == nil {
			edges = &dayEdges{Date: d, Call: -1, Wrap: -1, Element: it.ID}
			byDate[dates[0]] = edges
		}
		for _, line := range strings.Split(strings.ToLower(it.Text), "\n") {
			mins := firstTimeIn(line)
			if mins < 0 {
				continue
			}
			if anyContains(line, wrapWords) && edges.Wrap < 0 {
				edges.Wrap = mins
			} else if anyContains(line, callWords) && edges.Call < 0 {
				edges.Call = mins
			}
		}
	}
	keys := make([]string, 0, len(byDate))
	for k := range byDate {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	found := 0
	for i := 1; i < len(keys); i++ {
		prev, next := byDate[keys[i-1]], byDate[keys[i]]
		if prev.Wrap < 0 || next.Call < 0 {
			continue
		}
		if next.Date.Sub(prev.Date) != 24*time.Hour {
			continue
		}
		// Wrap after midnight is written as the previous day's wrap, so a wrap
		// earlier than the call it follows means the unit went past 00:00.
		rest := (24*60 - prev.Wrap) + next.Call
		if rest >= standardTurnaroundHours*60 {
			continue
		}
		fmt.Fprintf(b, "TURNAROUND %s → %s: wrap %s and a call of %s the next morning is %s of "+
			"rest — the trade's floor is %d hours, and a short turnaround is how a late wrap "+
			"becomes a late day tomorrow [industry practice, not Omani statute]. Move the call "+
			"or wrap earlier.\n", prev.Element, next.Element, minsClock(prev.Wrap),
			minsClock(next.Call), hoursMins(rest), standardTurnaroundHours)
		found++
	}
	return found
}

// firstTimeIn returns the first wall-clock time on a line as minutes past
// midnight, or -1.
func firstTimeIn(line string) int {
	m := timePattern.FindStringSubmatch(line)
	if m == nil {
		return -1
	}
	h, _ := strconv.Atoi(m[1])
	mn, _ := strconv.Atoi(m[2])
	return h*60 + mn
}

// minsClock renders minutes past midnight as a clock a call sheet would print.
func minsClock(mins int) string {
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

// hoursMins renders a duration the way a person says it.
func hoursMins(mins int) string {
	if mins%60 == 0 {
		return fmt.Sprintf("%d hours", mins/60)
	}
	return fmt.Sprintf("%dh%02d", mins/60, mins%60)
}
