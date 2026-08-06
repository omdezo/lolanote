package agent

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"qomranote/backend/internal/domain"
)

// House style, measured instead of asserted.
//
// Consistency is what makes agent output indistinguishable from the person's
// own work, and on a board it is a PURE FUNCTION over data the scope walk
// already loaded. Nothing computed it. The only rule anywhere was one line of
// prose in the system prompt — "rename, matching the style already dominant" —
// with nothing in the tree that could say what was dominant.
//
// So a board whose columns are all "01 · Concept", "02 · Casting" received a new
// column called "Editing", and a board written entirely in Arabic received
// English headings whenever the request happened to be phrased in English. The
// standing proposal for the bilingual half was a prompt sentence and two probes,
// which is the intervention that has already failed three times in this
// codebase's history.
//
// THE TEETH ARE IN THE VERIFIER, NOT THE PROMPT. The digest states the
// conventions so the model can follow them; ConformanceCritique measures whether
// the plan actually did and rides the same MEASURED block the review turn
// already uses — which is machinery that exists and already has the model's
// attention.

// conventionFloor is how many titled containers a board needs before its habits
// count as a convention.
//
// Three, because two is a coincidence: a board with one column called "01 ·
// Concept" and one called "Notes" has no house style, and asserting one from it
// would make the agent enforce an accident.
const conventionFloor = 3

// dominanceFraction is how much of the corpus a pattern must cover to be called
// the board's style. Deliberately high: a convention stated at 55% is a
// convention the person will disagree with, and the cost of saying nothing is
// exactly the status quo.
const dominanceFraction = 0.7

// numberedTitle matches a leading ordinal — "01 · Concept", "3. Casting",
// "2) Locations", "١ ـ التصوير". Arabic-Indic digits are in the class because
// the bilingual case is the one this exists for.
var numberedTitle = regexp.MustCompile(`^[0-9\x{0660}-\x{0669}\x{06F0}-\x{06F9}]{1,3}\s*[.·)\-–—:ـ]\s*\S`)

// separatorGlyphs are the joiners a board might use between a prefix and a name.
// Ordered so the check is deterministic rather than map-ranged.
var separatorGlyphs = []string{"·", "—", "–", "|", "»", ":", " - "}

// Conventions is what this board's existing names have in common.
//
// Every field is optional and empty means "no dominant habit" — which is the
// common case on a young board and must read as silence rather than as a rule.
type Conventions struct {
	// Corpus is how many titled containers the measurement is over, so the
	// digest can say what the claim rests on.
	Corpus int
	// Numbered is true when most container titles carry a leading ordinal.
	Numbered bool
	// Separator is the glyph most titles use between parts, or "".
	Separator string
	// Casing is "Title Case", "lower case", "UPPER CASE" or "".
	Casing string
	// EmojiLeading is true when most titles begin with a pictograph.
	EmojiLeading bool
	// MeanTitleWords is the average length of a container title, rounded. A new
	// column called "Everything we still have to do before the shoot" on a board
	// of two-word names is off-style even when every other axis matches.
	MeanTitleWords int
	// Script is the dominant writing system of the board's CONTENT — "arabic",
	// "latin", or "" when neither dominates.
	//
	// The bilingual signal nothing measured. It is the axis with the largest
	// visible failure and the cheapest computation in the struct.
	Script string
	// ColorMeaning maps a swatch name to what it correlates with on this board —
	// a label name, or a column title — where the correlation is strong enough to
	// be a taxonomy rather than a coincidence.
	ColorMeaning map[string]string
}

// Empty reports whether anything was learned. A board with no habits renders no
// block, because a HOUSE STYLE section saying nothing costs tokens on every turn
// and teaches the model to skip the heading.
func (c Conventions) Empty() bool {
	return c.Corpus < conventionFloor && c.Script == "" && len(c.ColorMeaning) == 0
}

// MeasureConventions derives the board's habits from the compiled scope.
//
// Zero extra reads and zero model calls: it walks Items, which the scope walk
// has already paid for. That is the whole argument for computing this rather
// than asking the model to notice it — noticing costs a turn and is unreliable;
// measuring costs nothing and is exact.
func MeasureConventions(scope *BoardScope) Conventions {
	var c Conventions
	if scope == nil {
		return c
	}
	var titles []string
	for _, it := range scope.Items {
		switch it.Type {
		case domain.TypeColumn, domain.TypeBoard, domain.TypeTaskList:
			if t := strings.TrimSpace(it.Text); t != "" && t != "(no text)" {
				titles = append(titles, t)
			}
		}
	}
	// Existing column names the walk recorded but may have elided as items. The
	// corpus is the point, so both sources feed it.
	for _, t := range scope.ExistingColumns {
		if t = strings.TrimSpace(t); t != "" {
			titles = append(titles, t)
		}
	}
	titles = dedupeStrings(titles)
	c.Corpus = len(titles)

	if c.Corpus >= conventionFloor {
		c.Numbered = fractionOf(titles, func(t string) bool { return numberedTitle.MatchString(t) }) >= dominanceFraction
		c.EmojiLeading = fractionOf(titles, leadsWithPictograph) >= dominanceFraction
		c.Separator = dominantSeparator(titles)
		c.Casing = dominantCasing(titles)
		total := 0
		for _, t := range titles {
			total += len(strings.Fields(t))
		}
		c.MeanTitleWords = int(math.Round(float64(total) / float64(len(titles))))
	}

	c.Script = dominantScript(scope)
	c.ColorMeaning = colourMeanings(scope)
	return c
}

// dominantScript reports which writing system the board's CONTENT is in.
//
// Over item text rather than over titles, because a board can have English
// column headers and Arabic cards and the cards are what the person writes in.
// Codepoint fractions, not language detection: the question is which script a
// new card should be written in, and that is answerable from the alphabet.
func dominantScript(scope *BoardScope) string {
	var arabic, latin int
	for _, it := range scope.Items {
		for _, r := range it.Text {
			switch {
			case r >= 0x0600 && r <= 0x06FF, r >= 0x0750 && r <= 0x077F, r >= 0xFB50 && r <= 0xFEFF:
				arabic++
			case unicode.IsLetter(r) && r < 0x0250:
				latin++
			}
		}
	}
	total := arabic + latin
	// A handful of characters is not a board's language. The floor stops a board
	// holding two cards from declaring itself Arabic on one stray word.
	if total < 40 {
		return ""
	}
	switch {
	case float64(arabic)/float64(total) >= dominanceFraction:
		return "arabic"
	case float64(latin)/float64(total) >= dominanceFraction:
		return "latin"
	}
	return ""
}

// colourMeanings finds the swatches that mean something on this board.
//
// A colour used on six cards, five of which carry the label "blocked", is a
// taxonomy the person built by hand — and the agent recolouring one of them, or
// coining a seventh colour for the same idea, is the failure. Correlation at
// 80%, which is the threshold at which the pattern is a rule rather than a run
// of luck.
func colourMeanings(scope *BoardScope) map[string]string {
	if scope == nil {
		return nil
	}
	labelName := map[string]string{}
	for _, l := range scope.Labels {
		labelName[l.ID] = l.Name
	}
	byColour := map[string]map[string]int{}
	total := map[string]int{}
	for _, it := range scope.Items {
		if it.Color == "" {
			continue
		}
		total[it.Color]++
		for _, id := range it.Labels {
			name := labelName[id]
			if name == "" {
				name = id
			}
			if byColour[it.Color] == nil {
				byColour[it.Color] = map[string]int{}
			}
			byColour[it.Color][name]++
		}
	}
	out := map[string]string{}
	for colour, n := range total {
		// Two coloured cards agreeing is not a taxonomy.
		if n < 3 {
			continue
		}
		for name, hits := range byColour[colour] {
			if float64(hits)/float64(n) >= 0.8 {
				out[colour] = name
				break
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Render is the HOUSE STYLE block, or "" when the board has no measurable
// habits.
func (c Conventions) Render() string {
	if c.Empty() {
		return ""
	}
	var lines []string
	if c.Corpus >= conventionFloor {
		if c.Numbered {
			lines = append(lines, "container names carry a leading number — a new one must too, "+
				"continuing the sequence")
		}
		if c.Separator != "" {
			lines = append(lines, fmt.Sprintf("names are joined with %q, not with another glyph", c.Separator))
		}
		if c.Casing != "" {
			lines = append(lines, "names are written in "+c.Casing)
		}
		if c.EmojiLeading {
			lines = append(lines, "names start with an emoji")
		}
		if c.MeanTitleWords > 0 {
			lines = append(lines, fmt.Sprintf("names run about %d word(s) — a sentence-length title is off-style here",
				c.MeanTitleWords))
		}
	}
	switch c.Script {
	case "arabic":
		lines = append(lines, "THIS BOARD IS WRITTEN IN ARABIC. Write everything you create in Arabic — "+
			"titles included — whatever language the request happens to be phrased in")
	case "latin":
		lines = append(lines, "this board is written in the Latin script; keep new content in the "+
			"language already used here")
	}
	for _, colour := range sortedColourKeys(c.ColorMeaning) {
		lines = append(lines, fmt.Sprintf("the %s swatch already means %q on this board — "+
			"reuse it for that and nothing else", colour, c.ColorMeaning[colour]))
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nHOUSE STYLE (measured from %d existing names on this board — match it, "+
		"do not invent a second convention):\n", c.Corpus)
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
	return b.String()
}

// ConformanceCritique measures a finished plan against the board's habits.
//
// This is the half with teeth. The prompt half can be ignored and historically
// was; a MEASURED line appended to the review turn is a fact the model has to
// answer, and it uses the same channel the size and hollow checks already use.
//
// It names violations and never blocks: a deliberate departure is sometimes
// right — an English "Budget" column on an Arabic board because the finance team
// reads English — and a hard gate here would one day refuse the correct answer.
func ConformanceCritique(p *Plan, scope *BoardScope) []string {
	if p == nil || scope == nil || len(p.Actions) == 0 {
		return nil
	}
	c := MeasureConventions(scope)
	if c.Empty() {
		return nil
	}
	var newTitles []string
	var newBodies []string
	newColours := map[string]bool{}
	for _, a := range p.Actions {
		switch a.Kind {
		case ActCreateColumn, ActCreateBoard, ActCreateTodo, ActRename:
			if t := strings.TrimSpace(a.Title); t != "" {
				newTitles = append(newTitles, t)
			}
		case ActCreateNote, ActCreateHeading, ActSetText, ActWriteDocument:
			if t := strings.TrimSpace(a.Text); t != "" {
				newBodies = append(newBodies, t)
			}
			if t := strings.TrimSpace(a.Title); t != "" {
				newTitles = append(newTitles, t)
			}
		case ActSetColor, ActAddColor:
			if a.Color != "" {
				newColours[a.Color] = true
			}
		}
	}

	var out []string
	if c.Corpus >= conventionFloor && len(newTitles) > 0 {
		if c.Numbered {
			if bad := countWhere(newTitles, func(t string) bool { return !numberedTitle.MatchString(t) }); bad > 0 {
				out = append(out, fmt.Sprintf(
					"every container name on this board carries a leading number and %d of the "+
						"names in this plan do not (%s) — they will read as somebody else's work",
					bad, quoteSome(newTitles, func(t string) bool { return !numberedTitle.MatchString(t) })))
			}
		}
		if c.MeanTitleWords > 0 {
			long := func(t string) bool { return len(strings.Fields(t)) > c.MeanTitleWords*3 }
			if bad := countWhere(newTitles, long); bad > 0 {
				out = append(out, fmt.Sprintf(
					"this board's names run about %d word(s) and %d of yours are more than three "+
						"times that (%s) — headers are narrow and clip",
					c.MeanTitleWords, bad, quoteSome(newTitles, long)))
			}
		}
	}
	// The bilingual check, over the plan's own words rather than the request's.
	//
	// A run answering an English-phrased request on an Arabic board writes
	// English, silently, and every prompt-only attempt at this has failed. Here
	// it is arithmetic over what was actually staged.
	if c.Script != "" {
		written := append(append([]string{}, newTitles...), newBodies...)
		if off := offScriptCount(written, c.Script); off > 0 && off*2 > len(written) {
			out = append(out, fmt.Sprintf(
				"this board is written in %s and %d of the %d things this plan writes are not — "+
					"the request's language is not the board's language, and the board wins",
				c.Script, off, len(written)))
		}
	}
	if palette := offPalette(newColours, scope); palette != "" {
		out = append(out, palette)
	}
	return out
}

// offPalette names colours the plan introduces that the board does not already
// use.
func offPalette(introduced map[string]bool, scope *BoardScope) string {
	if len(introduced) == 0 {
		return ""
	}
	inUse := map[string]bool{}
	for _, it := range scope.Items {
		if it.Color != "" {
			inUse[it.Color] = true
		}
	}
	// A board with no palette at all cannot be departed from.
	if len(inUse) < 2 {
		return ""
	}
	var novel []string
	for hex := range introduced {
		// Named through the same table set_color writes from, so the comparison is
		// between two swatch NAMES rather than between a hex the plan carries and
		// a name the digest rendered — the mismatch that made colour invisible
		// once already.
		name := colorOf(&domain.Element{Content: domain.Content{"backgroundColor": hex}})
		if name != "" && name != "custom" && !inUse[name] {
			novel = append(novel, name)
		}
	}
	if len(novel) == 0 {
		return ""
	}
	sort.Strings(novel)
	return fmt.Sprintf("this plan introduces %d colour(s) outside the board's existing %d-colour "+
		"palette (%s) — reuse what is here unless the new colour means something new",
		len(novel), len(inUse), strings.Join(novel, ", "))
}

// offScriptCount counts strings written in something other than the board's
// dominant script.
func offScriptCount(texts []string, script string) int {
	n := 0
	for _, t := range texts {
		var arabic, latin int
		for _, r := range t {
			switch {
			case r >= 0x0600 && r <= 0x06FF, r >= 0x0750 && r <= 0x077F, r >= 0xFB50 && r <= 0xFEFF:
				arabic++
			case unicode.IsLetter(r) && r < 0x0250:
				latin++
			}
		}
		if arabic+latin < 4 {
			continue // a number, a glyph, a name — no script to be wrong about
		}
		if script == "arabic" && latin > arabic {
			n++
		}
		if script == "latin" && arabic > latin {
			n++
		}
	}
	return n
}

// --- small shared helpers -------------------------------------------------

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func fractionOf(in []string, pred func(string) bool) float64 {
	if len(in) == 0 {
		return 0
	}
	return float64(countWhere(in, pred)) / float64(len(in))
}

func countWhere(in []string, pred func(string) bool) int {
	n := 0
	for _, s := range in {
		if pred(s) {
			n++
		}
	}
	return n
}

// quoteSome names up to three offenders. Naming one is a complaint; naming all
// forty is a wall the model skims.
func quoteSome(in []string, pred func(string) bool) string {
	var out []string
	for _, s := range in {
		if !pred(s) {
			continue
		}
		out = append(out, fmt.Sprintf("%q", truncate(s, 40)))
		if len(out) == 3 {
			break
		}
	}
	return strings.Join(out, ", ")
}

func leadsWithPictograph(t string) bool {
	for _, r := range t {
		if unicode.IsSpace(r) {
			continue
		}
		return r > 0x2100 && !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}
	return false
}

func dominantSeparator(titles []string) string {
	best, bestN := "", 0
	for _, g := range separatorGlyphs {
		n := countWhere(titles, func(t string) bool { return strings.Contains(t, g) })
		if n > bestN {
			best, bestN = g, n
		}
	}
	if float64(bestN)/float64(len(titles)) >= dominanceFraction {
		return strings.TrimSpace(best)
	}
	return ""
}

// dominantCasing reports how this board writes its names.
//
// Only over titles that HAVE case — an Arabic or numeric title is neither upper
// nor lower, and counting it as a miss is how the check would tell an Arabic
// board it was inconsistent with itself.
func dominantCasing(titles []string) string {
	var cased []string
	for _, t := range titles {
		if strings.ToUpper(t) != strings.ToLower(t) {
			cased = append(cased, t)
		}
	}
	if len(cased) < conventionFloor {
		return ""
	}
	upper := fractionOf(cased, func(t string) bool { return t == strings.ToUpper(t) })
	lower := fractionOf(cased, func(t string) bool { return t == strings.ToLower(t) })
	title := fractionOf(cased, isTitleCase)
	switch {
	case upper >= dominanceFraction:
		return "UPPER CASE"
	case lower >= dominanceFraction:
		return "lower case"
	case title >= dominanceFraction:
		return "Title Case"
	}
	return ""
}

func isTitleCase(t string) bool {
	words := strings.Fields(t)
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		r := []rune(w)[0]
		if unicode.IsLetter(r) && !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

func sortedColourKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
