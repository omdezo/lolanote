package domain

// UserSettings is every user-tunable knob, persisted on the user document and
// returned with /me so the client can hydrate before first paint. Each group
// mirrors one tab of the in-app settings dialog. Zero values are never
// meaningful — DefaultSettings() is the single source of defaults and
// Normalize() coerces any persisted/patched value back into the valid set.
type UserSettings struct {
	Appearance    AppearanceSettings    `bson:"appearance" json:"appearance"`
	Preferences   PreferenceSettings    `bson:"preferences" json:"preferences"`
	Localization  LocalizationSettings  `bson:"localization" json:"localization"`
	Toolbar       ToolbarSettings       `bson:"toolbar" json:"toolbar"`
	Notifications NotificationSettings  `bson:"notifications" json:"notifications"`
	Privacy       PrivacySettings       `bson:"privacy" json:"privacy"`
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
	Mentions     bool   `bson:"mentions" json:"mentions"`
	Comments     bool   `bson:"comments" json:"comments"`
	Shares       bool   `bson:"shares" json:"shares"`
	Assignments  bool   `bson:"assignments" json:"assignments"`
	BoardChanges bool   `bson:"boardChanges" json:"boardChanges"`
	Reminders    bool   `bson:"reminders" json:"reminders"`
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
			Assignments: true, BoardChanges: false, Reminders: true,
			EmailEnabled: false, EmailDigest: "off",
		},
		Privacy: PrivacySettings{ShowPresence: true, ShowEmailToOthers: true},
	}
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
