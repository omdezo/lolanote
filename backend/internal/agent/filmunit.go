package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The unit: a production's people are not this app's users.
//
// set_assignee takes a userId and the digest's PEOPLE block is the board's
// COLLABORATORS, so the only people the agent could see were the ones holding an
// account. A twelve-person crew in Oman is twelve phone numbers. Cast carry a
// character name and a cast ID — the join key every call sheet, DOOD and camera
// report matches on — crew carry a department and a role, and vendors, location
// owners, drivers and the government minder on a permitted shoot carry neither.
// None of them will ever log in.
//
// So the playbook line "'who owns what?' — set_assignee where the text names
// somebody" failed silently on almost every real production card: the name was
// right there in the text and was not a user, so the agent either assigned the
// wrong collaborator or answered nothing. And the most common question on a
// shoot — who is called, who is confirmed, who has signed a release — was
// unanswerable because nothing read the names the board already carried.
//
// This is the READ half, and deliberately only the read half. A display-only
// assignee field would be a value written into content that no renderer paints,
// which is the exact class of bug this corner keeps finding; the honest reach
// today is that the name goes in the card text (where it renders) and the digest
// can finally SEE it.
//
// One hard separation, and it is the reason this block is built out of content
// rather than out of the scope's People: it must never mix the two. The PEOPLE
// block publishes per-run aliases for real collaborators and deliberately hides
// their subject ids; this block is names a person typed onto a card. Merging
// them would let a card-derived string be mistaken for somebody with access, and
// "assign it to Ahmed" would reach an account belonging to a different Ahmed.

// unitRoles are the roles a production actually writes down, in the trade's own
// words.
//
// The list is the vocabulary the crew spec already teaches, plus the Oman-local
// line items that no international crew list has. Longest-first at match time,
// because "2nd AC" has to win over "AC" and "best boy grip" over "grip" — a
// shorter match would file the focus puller under the wrong department and the
// error would be invisible in the output.
var unitRoles = []string{
	// Direction and production.
	"director", "producer", "line producer", "production manager",
	"production coordinator", "executive producer", "writer", "screenwriter",
	"1st ad", "first ad", "2nd ad", "second ad", "2nd 2nd ad", "pa",
	"fixer", "service producer", "government liaison", "minder",
	// Camera.
	"dp", "dop", "director of photography", "cinematographer", "camera operator",
	"operator", "1st ac", "focus puller", "2nd ac", "loader", "dit", "stills",
	// Electric and grip.
	"gaffer", "best boy electric", "best boy grip", "electrician", "key grip",
	"dolly grip", "grip", "genny op",
	// Sound.
	"sound mixer", "sound recordist", "boom operator", "boom op", "utility",
	// Art, wardrobe, make-up.
	"production designer", "art director", "set decorator", "props master",
	"set dresser", "costume designer", "wardrobe", "hmu", "hair and makeup",
	"makeup artist",
	// Script, locations, transport, catering.
	"script supervisor", "continuity", "location manager", "locations manager",
	"unit manager", "driver", "medic", "safety officer", "caterer", "runner",
	// Post.
	"editor", "assistant editor", "colourist", "colorist", "sound designer",
	"re-recording mixer", "composer", "vfx supervisor",
	// Cast side.
	"cast", "lead", "supporting", "extra", "stunt double", "stunt coordinator",
	// Arabic, because a Muscat board is as likely to say these.
	"مخرج", "منتج", "مصور", "مدير التصوير", "مهندس صوت", "ممثل", "مونتير",
}

// unitRolePattern matches a "<role><separator><name>" line, which is how a
// production writes a person down on a card: "Gaffer — Ahmed Al Balushi",
// "1st AC: Fatma", "Script Supervisor - TBC".
//
// Built once from unitRoles rather than written by hand, so adding a role to the
// vocabulary above is the only edit anybody has to make.
var unitRolePattern = buildUnitRolePattern()

func buildUnitRolePattern() *regexp.Regexp {
	roles := append([]string(nil), unitRoles...)
	// Longest first: the alternation is ordered and Go's regexp is leftmost, so
	// "grip" before "key grip" would swallow the key grip's title.
	sort.SliceStable(roles, func(i, j int) bool { return len(roles[i]) > len(roles[j]) })
	quoted := make([]string, 0, len(roles))
	for _, r := range roles {
		quoted = append(quoted, regexp.QuoteMeta(r))
	}
	return regexp.MustCompile(`(?i)^\s*(?:\d+[.)]\s*)?(` + strings.Join(quoted, "|") +
		`)\s*[:：—–-]\s*(.*)$`)
}

// castLinePattern matches a cast-table line: "1. LAYLA — Maryam Al Habsi".
//
// The leading number is the CAST ID and it is the join key: the call sheet lists
// cast by number, the DOOD's rows are cast ids, and the second AD calls people
// by them. Dropping it is the same class of loss as dropping a scene number.
var castLinePattern = regexp.MustCompile(`^\s*(\d{1,2})[.)]\s+([^—–:\-]{2,40}?)\s*[—–:-]\s*(.+)$`)

// unfilled are the ways a production writes "nobody yet". A role with one of
// these against it is an OPEN position, which is a different and more useful
// answer than a role with a name.
var unfilled = []string{"", "tbc", "tbd", "?", "—", "-", "n/a", "na", "none", "open"}

// unitPerson is one person the board's own content names.
type unitPerson struct {
	Role   string
	Name   string
	CastID string
	Where  string
}

// maxUnitNamed caps the roll-up. A feature's crew list is a hundred lines and
// the digest is not the place to print one; the count is stated so the model
// knows it is looking at a sample rather than the unit.
const maxUnitNamed = 24

// unitRoll reads every person the board names, and every role it names with
// nobody against it.
func unitRoll(s *BoardScope) (named []unitPerson, open []unitPerson) {
	if s == nil {
		return nil, nil
	}
	seen := map[string]bool{}
	add := func(p unitPerson) {
		key := strings.ToLower(p.Role + "|" + p.Name)
		if seen[key] {
			return
		}
		if isUnfilled(p.Name) {
			seen[key] = true
			p.Name = ""
			open = append(open, p)
			return
		}
		// A role word followed by a sentence is a NOTE, not a person. "Continuity
		// — check the watch on Layla's left wrist" would otherwise arrive in the
		// roll-up as a crew member called "check the watch on Layla's left
		// wrist", which is the kind of output that makes somebody stop reading
		// the block entirely.
		if !looksLikeAName(p.Name) {
			return
		}
		seen[key] = true
		named = append(named, p)
	}
	for _, it := range s.Items {
		for _, line := range strings.Split(it.Text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if m := unitRolePattern.FindStringSubmatch(line); m != nil {
				add(unitPerson{Role: tradeCase(m[1]), Name: cleanName(m[2]), Where: it.ID})
				continue
			}
			if m := castLinePattern.FindStringSubmatch(line); m != nil {
				add(unitPerson{Role: cleanName(m[2]), Name: cleanName(m[3]),
					CastID: m[1], Where: it.ID})
			}
		}
		scanTableUnit(add, s, it.ID)
	}
	return named, open
}

// scanTableUnit reads a cast or crew TABLE, which is where a call sheet actually
// keeps its people.
//
// The header decides: a table with a name-ish column and a role-ish one beside
// it is a cast or crew list, and anything else is left alone. Guessing at an
// unlabelled grid would fill the block with budget line items.
func scanTableUnit(add func(unitPerson), s *BoardScope, id string) {
	el := s.Elements[id]
	if el == nil {
		return
	}
	rows := toRows(el.Content["cells"])
	if len(rows) < 2 {
		return
	}
	nameCol, roleCol, castCol := -1, -1, -1
	for i, h := range rows[0] {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "artist", "actor", "performer", "name", "crew", "who":
			if nameCol < 0 {
				nameCol = i
			}
		case "character", "role", "department", "position", "part":
			if roleCol < 0 {
				roleCol = i
			}
		case "cast id", "cast", "#", "no", "no.":
			if castCol < 0 {
				castCol = i
			}
		}
	}
	if nameCol < 0 || roleCol < 0 {
		return
	}
	for _, row := range rows[1:] {
		if len(row) <= nameCol || len(row) <= roleCol {
			continue
		}
		p := unitPerson{Role: cleanName(row[roleCol]), Name: cleanName(row[nameCol]), Where: id}
		if castCol >= 0 && castCol < len(row) {
			p.CastID = strings.TrimSpace(row[castCol])
		}
		if p.Role == "" {
			continue
		}
		add(p)
	}
}

// isUnfilled reports whether what stands against a role is a person or a
// placeholder.
func isUnfilled(name string) bool {
	low := strings.ToLower(strings.TrimSpace(name))
	for _, u := range unfilled {
		if low == u {
			return true
		}
	}
	return false
}

// looksLikeAName reports whether what stands against a role could be a person.
//
// Length and word count, and nothing cleverer: a name is short, and the thing
// this has to reject — a continuity note, a department instruction, a line of
// prose that happens to start with a role word — is long. A cleverer test would
// reject Arabic names, hyphenated names, or the three-part names normal in this
// country, and rejecting a real crew member is the worse error.
func looksLikeAName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > 40 {
		return false
	}
	return len(strings.Fields(s)) <= 4
}

// cleanName trims the punctuation a person types around a name and drops the
// parenthetical a call sheet hangs off it.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "(["); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return strings.Trim(s, " \t.,;:—–-")
}

// tradeCase renders a role the way the trade writes it: acronyms upper, the rest
// title-ish. "dp" typed on a card is "DP" on a call sheet, and a crew list that
// says "Dp" reads as a tourist wrote it.
func tradeCase(role string) string {
	acronyms := map[string]string{
		"dp": "DP", "dop": "DoP", "dit": "DIT", "hmu": "HMU", "pa": "PA",
		"1st ad": "1st AD", "2nd ad": "2nd AD", "2nd 2nd ad": "2nd 2nd AD",
		"1st ac": "1st AC", "2nd ac": "2nd AC", "vfx supervisor": "VFX Supervisor",
	}
	low := strings.ToLower(strings.TrimSpace(role))
	if a, ok := acronyms[low]; ok {
		return a
	}
	words := strings.Fields(low)
	for i, w := range words {
		r := []rune(w)
		if len(r) == 0 {
			continue
		}
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

// unitBlock renders the roll-up, or "" when the board names nobody.
//
// Stated as content-derived every single time, because the one thing that must
// never happen is a name off a card being mistaken for somebody with access to
// the board.
func unitBlock(s *BoardScope) string {
	named, open := unitRoll(s)
	if len(named) == 0 && len(open) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("UNIT — the people this board's own CONTENT names. These are read out of the " +
		"cards and tables, they are NOT collaborators on this app, and set_assignee cannot " +
		"reach them: a production's cast, crew, vendors, location owners and drivers are " +
		"phone numbers, not accounts. Write the person's name INTO the card or the table " +
		"row, and never guess a collaborator from the PEOPLE list for a name you saw here.\n")
	shown := named
	if len(shown) > maxUnitNamed {
		shown = shown[:maxUnitNamed]
	}
	for _, p := range shown {
		if p.CastID != "" {
			fmt.Fprintf(&b, "  cast %s %s — %s [%s]\n", p.CastID, p.Role, p.Name, p.Where)
			continue
		}
		fmt.Fprintf(&b, "  %s — %s [%s]\n", p.Role, p.Name, p.Where)
	}
	if len(named) > len(shown) {
		fmt.Fprintf(&b, "  … %d more named on this board\n", len(named)-len(shown))
	}
	if len(open) > 0 {
		parts := make([]string, 0, len(open))
		for _, p := range open {
			parts = append(parts, fmt.Sprintf("%s [%s]", p.Role, p.Where))
		}
		fmt.Fprintf(&b, "NOBODY AGAINST THESE ROLES YET: %s — that is the answer to \"who are we "+
			"still waiting on\" and \"who is not on the call sheet\". Say it; do not fill it in "+
			"with a plausible name.\n", strings.Join(parts, ", "))
	}
	return b.String()
}
