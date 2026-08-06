package cli

import (
	"fmt"
	"sort"
	"strings"

	"qomranote/backend/internal/agent"
)

// The domain grade.
//
// The corpus is film-flavoured — fourteen of its probes ask for permits in
// Muscat, a documentary scoped to delivery, a crew structure, a two-day shoot,
// department budgets, casting cards, a whole production plan — and every one of
// them routed to authoringGrade, which asserts only that containers are not
// empty, that there is more content than containers, that there are at least
// ten actions, and that no two containers share a title. Nothing about
// correctness.
//
// So a run producing ten beautifully-shaped columns of invented Omani permit
// procedure scored exactly as well as one naming the Ministry of Information,
// the Civil Aviation Authority and the lead time — and the corpus's existence
// created the impression the domain was covered. This is the fourth axis of the
// corpus's blindness, beside flatland, single-player and the small: it is not
// measuring the right variable at all on the probes that matter most.
//
// The corpus header concedes that "specificity and taste … are left to a
// person". For a domain with published standards and named authorities, much of
// that is checkable by machine, and this checks the checkable part: did the
// answer NAME the things a person who knows this work would have named?

// rubric is what a right answer in this domain has to say out loud.
type rubric struct {
	// Requires are groups of alternatives. Each group must be satisfied by at
	// least one of its members appearing somewhere in the plan's own words —
	// so "the permitting authority" can be named in more than one legitimate
	// way without the rubric pretending only one is correct.
	Requires [][]string
	// AtLeast is how many of the Requires groups must be hit. Below the count,
	// the run is generic. Zero means all of them.
	AtLeast int
	// Forbids are the tells of a run that produced structure and no substance:
	// placeholder tokens where a figure or a name belongs.
	Forbids []string
}

// domainGrade checks a plan's own words against its probe's rubric.
//
// Case-insensitive substring matching over everything the plan says — titles,
// bodies, summaries, table cells and its own explanation. Crude on purpose: the
// alternative is a model grading a model, which measures agreement rather than
// correctness, and this has to be reproducible offline and free.
func domainGrade(p probe, res evalResult) string {
	if p.Domain == nil || res.Plan == nil {
		return ""
	}
	said := strings.ToLower(planText(res.Plan))
	var missing []string
	hits := 0
	for _, group := range p.Domain.Requires {
		if anyIn(said, group) {
			hits++
			continue
		}
		missing = append(missing, group[0])
	}
	need := p.Domain.AtLeast
	if need <= 0 || need > len(p.Domain.Requires) {
		need = len(p.Domain.Requires)
	}
	var faults []string
	if hits < need {
		sort.Strings(missing)
		faults = append(faults, fmt.Sprintf("named %d of %d required domain specifics (needs %d); missing: %s",
			hits, len(p.Domain.Requires), need, strings.Join(missing, ", ")))
	}
	for _, bad := range p.Domain.Forbids {
		if strings.Contains(said, strings.ToLower(bad)) {
			faults = append(faults, fmt.Sprintf("left the placeholder %q in the answer", bad))
		}
	}
	return strings.Join(faults, "; ")
}

// planText is everything the plan actually says, so a rubric is checked against
// what a person would read rather than against internal fields.
func planText(p *agent.Plan) string {
	var b strings.Builder
	b.WriteString(p.Summary)
	b.WriteByte('\n')
	for _, n := range p.Notes {
		b.WriteString(n)
		b.WriteByte('\n')
	}
	for _, u := range p.Unmet {
		b.WriteString(u.Request)
		b.WriteByte(' ')
		b.WriteString(u.Why)
		b.WriteByte('\n')
	}
	for _, a := range p.Actions {
		b.WriteString(a.Title)
		b.WriteByte('\n')
		b.WriteString(a.Text)
		b.WriteByte('\n')
		b.WriteString(a.Summary)
		b.WriteByte('\n')
		for _, row := range a.Rows {
			b.WriteString(strings.Join(row, " "))
			b.WriteByte('\n')
		}
		for _, task := range a.Tasks {
			b.WriteString(task)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func anyIn(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// ---- the rubrics ------------------------------------------------------------
//
// Deliberately conservative. Each group lists the ways a knowledgeable answer
// could legitimately name the same thing, and AtLeast is set below the group
// count so a good answer that takes a different-but-valid angle is not failed
// for it. The bar being measured is "did somebody who knows this work write
// this", not "did it match a keyword list".

// omanPermitsRubric — shooting in a public place in Muscat.
var omanPermitsRubric = &rubric{
	Requires: [][]string{
		{"ministry of information", "وزارة الإعلام", "ministry of heritage"},
		{"royal oman police", "rop", "municipality", "muscat municipality"},
		{"civil aviation", "caa", "drone permit", "drone"},
		{"insurance", "public liability", "indemnity"},
		{"lead time", "weeks", "working days", "days before"},
		{"location release", "location agreement", "permit application", "location permit"},
	},
	AtLeast: 3,
	Forbids: []string{"tbd", "lorem", "xxx", "placeholder"},
}

// crewRubric — the crew of a mid-size shoot. Departments, not job titles in
// general: a run that lists "people" has said nothing.
var crewRubric = &rubric{
	Requires: [][]string{
		{"camera", "dop", "director of photography", "cinematographer"},
		{"sound", "boom", "production sound", "mixer"},
		{"art department", "production design", "props"},
		{"assistant director", "1st ad", "first ad", "ad department"},
		{"production", "line producer", "production manager"},
		{"grip", "gaffer", "electric", "lighting"},
	},
	AtLeast: 4,
}

// scheduleRubric — a two-day shoot plan. The artefacts a shoot actually runs on.
var scheduleRubric = &rubric{
	Requires: [][]string{
		{"call time", "call sheet", "unit call"},
		{"wrap", "turnaround", "company move"},
		{"shot list", "scene", "setup"},
		{"golden hour", "magic hour", "daylight", "sunset", "sunrise", "blue hour"},
	},
	AtLeast: 2,
}

// deliveryRubric — documentary scoping through delivery.
var deliveryRubric = &rubric{
	Requires: [][]string{
		{"picture lock", "locked cut", "fine cut"},
		{"sound mix", "audio mix", "dub", "mix stage"},
		{"colour grade", "color grade", "grading"},
		{"deliverables", "masters", "dcp", "qc", "delivery spec"},
		{"rights", "clearance", "licensing", "archive"},
	},
	AtLeast: 3,
}

// budgetRubric — department budget numbers. Currency is the tell: a budget with
// no unit is a list of numbers.
var budgetRubric = &rubric{
	Requires: [][]string{
		{"omr", "rial", "ريال"},
		{"contingency", "overhead", "fringes"},
		{"above the line", "below the line", "department", "line item"},
	},
	AtLeast: 1,
	Forbids: []string{"tbd", "xxx", "placeholder"},
}

// callSheetRubric — the fifteenth probe, which asserts an ARTEFACT rather than
// a shape. A call sheet has a required field list; that list is the rubric, and
// there is nothing subjective left once it is written down.
var callSheetRubric = &rubric{
	Requires: [][]string{
		{"call time", "unit call", "crew call"},
		{"location", "unit base", "address"},
		{"weather", "sunrise", "sunset", "temperature"},
		{"cast", "artist", "talent"},
		{"scene", "shot list", "schedule"},
		{"contact", "phone", "emergency", "nearest hospital"},
		{"wrap", "estimated wrap", "lunch"},
	},
	AtLeast: 5,
	Forbids: []string{"tbd", "placeholder"},
}

// arabicFilmRubric — the same domain asked for in Arabic, which the product is
// built for and the corpus never once tested. The rubric is deliberately about
// SCRIPT rather than vocabulary: the failure worth catching is a run that
// answers an Arabic request in English.
var arabicFilmRubric = &rubric{
	Requires: [][]string{
		{"تصوير", "إنتاج", "فيلم", "مخرج"},
		{"تحضير", "ما قبل", "تمثيل", "موقع"},
		{"مونتاج", "ما بعد", "صوت", "تسليم"},
	},
	AtLeast: 2,
}
