package domain

import (
	"time"

	// The zone database is compiled in for the same reason internal/agent does
	// it: LoadLocation reading from the host means a container without tzdata
	// silently resolves every zone to UTC, and settings validation that passes
	// on a developer laptop and rejects everything in production is worse than
	// no validation.
	_ "time/tzdata"
)

// UserSettings is every user-tunable knob, persisted on the user document and
// returned with /me so the client can hydrate before first paint. Each group
// mirrors one tab of the in-app settings dialog. Zero values are never
// meaningful — DefaultSettings() is the single source of defaults and
// Normalize() coerces any persisted/patched value back into the valid set.
type UserSettings struct {
	Appearance    AppearanceSettings   `bson:"appearance" json:"appearance"`
	Preferences   PreferenceSettings   `bson:"preferences" json:"preferences"`
	Localization  LocalizationSettings `bson:"localization" json:"localization"`
	Toolbar       ToolbarSettings      `bson:"toolbar" json:"toolbar"`
	Notifications NotificationSettings `bson:"notifications" json:"notifications"`
	Privacy       PrivacySettings      `bson:"privacy" json:"privacy"`
	Agent         AgentSettings        `bson:"agent" json:"agent"`
}

// AgentSettings are the person's own standing notes for the AI agent.
//
// Board rules already existed and are the right default — most conventions are
// a property of the board, not the person. But a rule that is true everywhere
// ("never invent columns", "always tag by owner") had to be retyped on every
// board, so in practice people wrote it nowhere.
type AgentSettings struct {
	// Instructions apply to every board this person runs the agent on. The
	// board's own rules are layered on top and win where the two disagree.
	Instructions string `bson:"instructions,omitempty" json:"instructions"`
	// ProcessingAcknowledgedAt records that this PERSON was told their board
	// content is sent to a model provider — not that this browser was.
	//
	// The client already PATCHed it and the typed decode dropped it on the
	// floor, so the normalized response overwrote the optimistic value and the
	// consent card returned about 600ms after being dismissed. The client's
	// localStorage stopgap makes the UI correct on one machine; consent is a
	// fact about a person and has to survive a new device and cleared site data.
	ProcessingAcknowledgedAt string `bson:"processingAcknowledgedAt,omitempty" json:"processingAcknowledgedAt"`
}

// AppearanceSettings controls the visual shell.
type AppearanceSettings struct {
	Theme       string `bson:"theme" json:"theme"`             // light | dark | system
	AccentColor string `bson:"accentColor" json:"accentColor"` // hex
	DotGrid     bool   `bson:"dotGrid" json:"dotGrid"`         // canvas dot grid
	CardShadows bool   `bson:"cardShadows" json:"cardShadows"`
	UIDensity   string `bson:"uiDensity" json:"uiDensity"` // comfortable | compact
}

// PreferenceSettings controls editor/canvas behavior.
type PreferenceSettings struct {
	DoubleClickCreates string `bson:"doubleClickCreates" json:"doubleClickCreates"` // note | board | none
	WheelMode          string `bson:"wheelMode" json:"wheelMode"`                   // pan | zoom  (plain wheel; Ctrl always zooms)
	SnapToGrid         bool   `bson:"snapToGrid" json:"snapToGrid"`
	SpellCheck         bool   `bson:"spellCheck" json:"spellCheck"`
	OpenBoardsWith     string `bson:"openBoardsWith" json:"openBoardsWith"` // doubleClick | singleClick
	ShowHints          bool   `bson:"showHints" json:"showHints"`           // empty-canvas hint pill
}

// LocalizationSettings controls language and formats. The UI language is a
// client concern; the API only stores and validates the choice.
type LocalizationSettings struct {
	Language       string `bson:"language" json:"language"`             // en | ar
	FirstDayOfWeek int    `bson:"firstDayOfWeek" json:"firstDayOfWeek"` // 0=Sunday, 1=Monday, 6=Saturday
	DateFormat     string `bson:"dateFormat" json:"dateFormat"`         // auto | dmy | mdy | ymd
	TimeFormat     string `bson:"timeFormat" json:"timeFormat"`         // 12h | 24h
	// TimeZone is an IANA name ("Asia/Muscat"). Empty means UTC, unset.
	//
	// Everything dated in this product was written and read as UTC while the
	// people using it were four hours ahead, so a reminder asked for at 05:30 on
	// a shoot morning was stored as 05:30Z and fired at 09:30 local — after the
	// call time it existed to precede.
	TimeZone string `bson:"timeZone" json:"timeZone"`
}

// ToolbarSettings hides tools from the left rail. Keys are stable tool ids
// (note, link, todo, line, board, column, comment, table, sketch, color,
// document, audio, map, video, heading, image, upload, draw).
type ToolbarSettings struct {
	HiddenTools []string `bson:"hiddenTools" json:"hiddenTools"`
}

// NotificationSettings gates which events create notifications for the user.
// Email delivery is stored now and honored once SMTP is wired (PLAN §7).
type NotificationSettings struct {
	Mentions     bool `bson:"mentions" json:"mentions"`
	Comments     bool `bson:"comments" json:"comments"`
	Shares       bool `bson:"shares" json:"shares"`
	Assignments  bool `bson:"assignments" json:"assignments"`
	BoardChanges bool `bson:"boardChanges" json:"boardChanges"`
	Reminders    bool `bson:"reminders" json:"reminders"`
	// AgentRuns gates the outcome of an assistant run — yours, and a
	// collaborator's on a board you share.
	//
	// Default ON, unlike BoardChanges: a run is one notification per RUN, not
	// per change, and it is the largest single edit anybody makes on a shared
	// board. Muting it by default would have shipped the producer and the
	// silence together.
	AgentRuns    *bool  `bson:"agentRuns,omitempty" json:"agentRuns,omitempty"`
	EmailEnabled bool   `bson:"emailEnabled" json:"emailEnabled"`
	EmailDigest  string `bson:"emailDigest" json:"emailDigest"` // off | daily | weekly
}

// PrivacySettings controls what others can see about the user.
type PrivacySettings struct {
	ShowPresence      bool `bson:"showPresence" json:"showPresence"`           // appear in board presence + live cursors
	ShowEmailToOthers bool `bson:"showEmailToOthers" json:"showEmailToOthers"` // reveal email to collaborators
}

// DefaultSettings is the canonical starting point for every account.
func DefaultSettings() UserSettings {
	return UserSettings{
		Appearance: AppearanceSettings{
			Theme:       "system",
			AccentColor: "#5e5ce6",
			DotGrid:     true,
			CardShadows: true,
			UIDensity:   "comfortable",
		},
		Preferences: PreferenceSettings{
			DoubleClickCreates: "note",
			WheelMode:          "pan",
			SnapToGrid:         false,
			SpellCheck:         true,
			OpenBoardsWith:     "doubleClick",
			ShowHints:          true,
		},
		Localization: LocalizationSettings{
			Language:       "en",
			FirstDayOfWeek: 1,
			DateFormat:     "auto",
			TimeFormat:     "12h",
		},
		Toolbar: ToolbarSettings{HiddenTools: []string{}},
		Notifications: NotificationSettings{
			Mentions: true, Comments: true, Shares: true,
			Assignments: true, BoardChanges: false, Reminders: true, AgentRuns: on(),
			EmailEnabled: false, EmailDigest: "off",
		},
		Privacy: PrivacySettings{ShowPresence: true, ShowEmailToOthers: true},
	}
}

// on is a pointer to true, for the default-ON preferences.
func on() *bool { v := true; return &v }

// WantsAgentRuns answers the preference for accounts that predate the field.
//
// A plain bool would have decoded to FALSE for every account created before
// this shipped, so the producer would have landed muted for exactly the people
// who already had settings stored — a feature that ships and does nothing,
// indistinguishable from a bug. Nil means "never chosen", which is the
// documented default: on.
func (n NotificationSettings) WantsAgentRuns() bool {
	return n.AgentRuns == nil || *n.AgentRuns
}

// oneOf returns val when it is in allowed, otherwise fallback — settings
// survive malformed patches and forward/backward version skew.
func oneOf(val, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if val == a {
			return a
		}
	}
	return fallback
}

// validToolIDs is the closed set the toolbar accepts; unknown ids are dropped.
var validToolIDs = map[string]bool{
	"note": true, "link": true, "todo": true, "line": true, "board": true,
	"column": true, "comment": true, "table": true, "sketch": true,
	"color": true, "document": true, "audio": true, "map": true,
	"video": true, "heading": true, "image": true, "upload": true, "draw": true,
}

// Normalize coerces every enum-ish field into its valid set (falling back to
// defaults) and prunes unknown toolbar ids. Safe to call on any input.
func (s *UserSettings) Normalize() {
	d := DefaultSettings()

	s.Appearance.Theme = oneOf(s.Appearance.Theme, d.Appearance.Theme, "light", "dark", "system")
	if !isHexColor(s.Appearance.AccentColor) {
		s.Appearance.AccentColor = d.Appearance.AccentColor
	}
	s.Appearance.UIDensity = oneOf(s.Appearance.UIDensity, d.Appearance.UIDensity, "comfortable", "compact")

	s.Preferences.DoubleClickCreates = oneOf(s.Preferences.DoubleClickCreates, d.Preferences.DoubleClickCreates, "note", "board", "none")
	s.Preferences.WheelMode = oneOf(s.Preferences.WheelMode, d.Preferences.WheelMode, "pan", "zoom")
	s.Preferences.OpenBoardsWith = oneOf(s.Preferences.OpenBoardsWith, d.Preferences.OpenBoardsWith, "doubleClick", "singleClick")

	s.Localization.Language = oneOf(s.Localization.Language, d.Localization.Language, "en", "ar")
	if s.Localization.FirstDayOfWeek != 0 && s.Localization.FirstDayOfWeek != 1 && s.Localization.FirstDayOfWeek != 6 {
		s.Localization.FirstDayOfWeek = d.Localization.FirstDayOfWeek
	}
	s.Localization.DateFormat = oneOf(s.Localization.DateFormat, d.Localization.DateFormat, "auto", "dmy", "mdy", "ymd")
	s.Localization.TimeFormat = oneOf(s.Localization.TimeFormat, d.Localization.TimeFormat, "12h", "24h")
	// A zone that cannot be loaded must not be stored. LoadLocation failing at
	// READ time falls back to UTC silently, which turns a typo into every future
	// reminder firing hours off with nothing anywhere saying why; refusing it
	// here means the setting is either true or visibly absent.
	if s.Localization.TimeZone != "" {
		if _, err := time.LoadLocation(s.Localization.TimeZone); err != nil {
			s.Localization.TimeZone = ""
		}
	}

	s.Notifications.EmailDigest = oneOf(s.Notifications.EmailDigest, d.Notifications.EmailDigest, "off", "daily", "weekly")

	tools := make([]string, 0, len(s.Toolbar.HiddenTools))
	for _, id := range s.Toolbar.HiddenTools {
		if validToolIDs[id] {
			tools = append(tools, id)
		}
	}
	s.Toolbar.HiddenTools = tools
}

func isHexColor(v string) bool {
	if len(v) != 7 && len(v) != 4 {
		return false
	}
	if v[0] != '#' {
		return false
	}
	for _, r := range v[1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
