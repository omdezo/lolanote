package agent

import (
	"fmt"
	"sort"
	"strings"

	"qomranote/backend/internal/domain"
)

// A board tile's identity, in the vocabulary a tile actually takes.
//
// The standing proposal was to serve the icon vocabulary "the way cardSwatches
// already is". Three things are wrong with that:
//
//   - cardSwatches is a list of eight NOTE-PAPER pastels, and a board tile takes
//     a GRADIENT. Handing the agent a flat pastel produces a tile that looks
//     broken beside its siblings — the same class of failure as writing a
//     content key nothing renders.
//   - the icon surface has three mutually exclusive forms — a Lucide glyph, a
//     letter or number, and an uploaded image — and only the first two are
//     expressible by an agent at all.
//   - and cardSwatches itself was the vocabulary that shipped writing a key the
//     card did not paint.
//
// So two vocabularies, named separately, mirroring
// frontend/src/components/ui/BoardStylePopover.tsx (COLORS) and
// frontend/src/lib/iconCatalog.ts (LETTER_ICONS plus the catalogue names). The
// digest reads what siblings already carry, because without that "give them
// distinct colours" is a coin flip.

// boardColors is the tile palette, by name.
//
// The eleven gradients first — those are what a board tile is drawn with, and
// what the hash-picked default produces — then the six flat pastels the popover
// also offers. Named rather than raw, for exactly the reason cardSwatches is:
// "linear-gradient(135deg,#6e6cf0,#4a48c4)" means nothing to a language model
// and "indigo" means something to everyone.
var boardColors = map[string]string{
	"indigo":     "linear-gradient(135deg,#6e6cf0,#4a48c4)",
	"blue":       "linear-gradient(135deg,#5fb0f5,#1c7ed6)",
	"teal":       "linear-gradient(135deg,#63e6be,#0c8599)",
	"green":      "linear-gradient(135deg,#4dd0a6,#0ca678)",
	"amber":      "linear-gradient(135deg,#ffc94d,#f08c00)",
	"orange":     "linear-gradient(135deg,#ff8a65,#e8590c)",
	"pink":       "linear-gradient(135deg,#f78fb3,#e64980)",
	"violet":     "linear-gradient(135deg,#9775fa,#7048e8)",
	"slate":      "linear-gradient(135deg,#a8b2bd,#5f6b76)",
	"charcoal":   "linear-gradient(135deg,#495057,#212529)",
	"pale-blue":  "#a3c7f0",
	"pale-pink":  "#f0b6c5",
	"pale-sand":  "#f6d9a0",
	"pale-mint":  "#b8e6c9",
	"pale-lilac": "#d6c9f0",
	"pale-stone": "#e8e2d5",
}

// boardColorNames lists the palette in a stable order, gradients first.
func boardColorNames() []string {
	out := make([]string, 0, len(boardColors))
	for name := range boardColors {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		gi := strings.HasPrefix(boardColors[out[i]], "linear")
		gj := strings.HasPrefix(boardColors[out[j]], "linear")
		if gi != gj {
			return gi
		}
		return out[i] < out[j]
	})
	return out
}

// boardColorName is the inverse: what a stored value is called.
func boardColorName(value string) string {
	for name, v := range boardColors {
		if v == value {
			return name
		}
	}
	if value != "" {
		return "custom"
	}
	return ""
}

// boardIcons is the subset of the icon catalogue an agent is offered.
//
// Deliberately a SHORTLIST rather than the whole three-hundred-entry catalogue:
// the vocabulary rides in every digest that mentions a board, and a wall of icon
// names is context spent on a decoration. These are the ones a workspace
// actually reaches for; a letter from LETTER_ICONS covers everything else, which
// is what the popover's second tab exists for.
var boardIcons = []string{
	"calendar", "clock", "timer", "target", "flag", "star", "bookmark",
	"folder", "archive", "box", "briefcase", "book", "file-text", "clipboard",
	"camera", "film", "image", "music", "mic", "palette", "brush",
	"users", "user", "message-square", "mail", "phone",
	"map-pin", "globe", "plane", "car", "home", "building",
	"dollar-sign", "trending-up", "bar-chart", "pie-chart",
	"check-circle", "alert-triangle", "zap", "lightbulb", "rocket", "wrench",
}

// boardIconAllowed accepts a catalogue name or a single LETTER_ICONS glyph.
//
// The letter case is not a fallback — it is the second tab of the product's own
// picker, and it is how a board called "Q3" gets a tile that says Q3.
func boardIconAllowed(icon string) bool {
	if icon == "" {
		return true // clearing is legitimate
	}
	for _, name := range boardIcons {
		if name == icon {
			return true
		}
	}
	if runes := []rune(icon); len(runes) == 1 {
		r := runes[0]
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return true
		}
		switch icon {
		case "&", "#", "@", "!", "?", "★", "№":
			return true
		}
	}
	return false
}

// siblingStyles renders what the boards already on this canvas look like.
//
// THE HARD CLAUSE. Without reading what siblings currently carry, "give these
// boards distinct colours" is a coin flip against a palette of sixteen — and the
// default is itself a hash of the board id, so an agent that sets one colour at
// random is as likely to collide as to distinguish. This is the same argument
// that put Color and Labels on the Item struct: a write capability with no
// matching read produces a parallel taxonomy.
func (s *BoardScope) boardStyleBlock() string {
	type styled struct{ id, title, colour, icon string }
	var rows []styled
	for _, it := range s.Items {
		if it.Type != domain.TypeBoard {
			continue
		}
		el := s.Elements[it.ID]
		if el == nil {
			continue
		}
		colour, _ := el.Content["color"].(string)
		icon, _ := el.Content["icon"].(string)
		rows = append(rows, styled{
			id: it.ID, title: truncate(it.Text, 40),
			colour: boardColorName(colour), icon: icon,
		})
	}
	// One board is not a set to be distinct within.
	if len(rows) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nBOARD TILES (style_board sets these; a tile takes a GRADIENT name, not a " +
		"card swatch — an unset one is a colour picked from the board's id, so it is " +
		"as likely to collide as not):\n")
	unset := 0
	for _, r := range rows {
		line := fmt.Sprintf("  %s %q — ", r.id, r.title)
		switch {
		case r.colour == "" && r.icon == "":
			line += "no colour, no icon"
			unset++
		case r.icon == "":
			line += r.colour + ", no icon"
		case r.colour == "":
			line += "no colour, icon " + r.icon
			unset++
		default:
			line += r.colour + ", icon " + r.icon
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "  colours: %s\n  icons: %s (or a single letter or digit)\n",
		strings.Join(boardColorNames(), ", "), strings.Join(boardIcons, ", "))
	if unset > 0 {
		fmt.Fprintf(&b, "  %d of these carry no deliberate style, so they are the ones worth "+
			"distinguishing.\n", unset)
	}
	return b.String()
}
