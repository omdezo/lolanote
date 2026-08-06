package agent

import (
	"fmt"
	"strings"
	"time"

	// The zone database, compiled in.
	//
	// Without it time.LoadLocation depends on files being present on the host,
	// which is exactly the kind of thing that is fine on a developer's laptop
	// and absent in a scratch container — and the failure mode is a silent
	// fallback to UTC, which is the bug this whole file exists to close.
	_ "time/tzdata"
)

// Times, and the four hours nobody noticed.
//
// A grep for "timezone" across the backend and the frontend returned nothing at
// all. set_reminder's schema handed the model a UTC instant — "RFC3339
// timestamp, e.g. 2026-09-01T09:00:00Z" — and this product's users write call
// sheets. "Crew call 05:30" written as 05:30Z is 09:30 in Muscat: the shoot is
// over. Because Oman has no DST the error is a constant four hours, which is
// worse than a variable one — consistent enough to look like a deliberate
// product decision and wrong on every row.
//
// So a time the agent writes is a LOCAL WALL CLOCK, and the conversion happens
// here, once. Doing it at three call sites is how two of them end up agreeing
// and the third does not.

// workspaceLocation resolves the workspace's zone, falling back to UTC.
//
// A named zone rather than an offset because an offset is only true until the
// clocks change — and while Oman never moves, the same field has to be right for
// somewhere that does.
func workspaceLocation(zone string) *time.Location {
	if strings.TrimSpace(zone) == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// localWallClockLayouts are the shapes a wall-clock time arrives in. All of
// them are zone-LESS on purpose: the whole point is that the model states the
// time a person would read off a call sheet and the server decides what instant
// that is.
var localWallClockLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseWorkspaceTime turns what the model wrote into a UTC instant.
//
// An explicit zone wins, because a model that has gone to the trouble of
// writing an offset means it. Everything else is read as local time in the
// workspace's own zone — which is what "09:00" means to every person who will
// ever read the result.
func parseWorkspaceTime(s, zone string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("give me a time")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	loc := workspaceLocation(zone)
	for _, layout := range localWallClockLayouts {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time I can read — write it as local wall-clock "+
		"time, e.g. 2026-09-01T05:30", s)
}

// zoneLine tells the model what clock this workspace runs on.
//
// Stated with the offset as well as the name, because "Asia/Muscat" means
// nothing to a reader who has to decide whether 05:30 is before or after a
// deadline expressed in UTC, and the offset alone loses the rule.
func zoneLine(zone string, now time.Time) string {
	if strings.TrimSpace(zone) == "" {
		return ""
	}
	loc := workspaceLocation(zone)
	if loc == time.UTC {
		return ""
	}
	_, offset := now.In(loc).Zone()
	sign, mins := "+", offset/60
	if mins < 0 {
		sign, mins = "-", -mins
	}
	return fmt.Sprintf("TIMES: this workspace runs on %s (UTC%s%02d:%02d), and it is %s there now. "+
		"Write every time as the local wall clock a person would read off a call sheet — "+
		"\"2026-09-01T05:30\" means half past five in the morning where they are.\n",
		zone, sign, mins/60, mins%60, now.In(loc).Format("Mon 2 Jan 15:04"))
}
