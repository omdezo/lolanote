package domain

import (
	"encoding/json"
	"testing"
)

func TestNormalizeSnapsInvalidValuesToDefaults(t *testing.T) {
	s := UserSettings{}
	s.Appearance.Theme = "neon"
	s.Appearance.AccentColor = "not-a-color"
	s.Preferences.WheelMode = "sideways"
	s.Localization.Language = "klingon"
	s.Localization.FirstDayOfWeek = 3
	s.Notifications.EmailDigest = "hourly"
	s.Toolbar.HiddenTools = []string{"note", "bogus-tool", "draw"}

	s.Normalize()

	d := DefaultSettings()
	if s.Appearance.Theme != d.Appearance.Theme {
		t.Errorf("theme = %q, want default %q", s.Appearance.Theme, d.Appearance.Theme)
	}
	if s.Appearance.AccentColor != d.Appearance.AccentColor {
		t.Errorf("accent = %q, want default", s.Appearance.AccentColor)
	}
	if s.Preferences.WheelMode != d.Preferences.WheelMode {
		t.Errorf("wheelMode = %q, want default", s.Preferences.WheelMode)
	}
	if s.Localization.Language != "en" || s.Localization.FirstDayOfWeek != 1 {
		t.Errorf("localization not defaulted: %+v", s.Localization)
	}
	if s.Notifications.EmailDigest != "off" {
		t.Errorf("emailDigest = %q, want off", s.Notifications.EmailDigest)
	}
	if len(s.Toolbar.HiddenTools) != 2 || s.Toolbar.HiddenTools[0] != "note" || s.Toolbar.HiddenTools[1] != "draw" {
		t.Errorf("hiddenTools = %v, want [note draw]", s.Toolbar.HiddenTools)
	}
}

func TestNormalizeKeepsValidValues(t *testing.T) {
	s := DefaultSettings()
	s.Appearance.Theme = "dark"
	s.Appearance.AccentColor = "#e8590c"
	s.Localization.Language = "ar"
	s.Localization.FirstDayOfWeek = 6
	s.Normalize()
	if s.Appearance.Theme != "dark" || s.Appearance.AccentColor != "#e8590c" ||
		s.Localization.Language != "ar" || s.Localization.FirstDayOfWeek != 6 {
		t.Errorf("valid values were clobbered: %+v", s)
	}
}

// The PATCH endpoint unmarshals the client patch over the current settings —
// absent fields must keep their value, present fields must overwrite.
func TestJSONMergeSemantics(t *testing.T) {
	current := DefaultSettings()
	current.Appearance.Theme = "dark"
	current.Preferences.SnapToGrid = true

	patch := []byte(`{"localization":{"language":"ar"},"appearance":{"accentColor":"#2eb85c"}}`)
	if err := json.Unmarshal(patch, &current); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	current.Normalize()

	if current.Localization.Language != "ar" {
		t.Errorf("language = %q, want ar", current.Localization.Language)
	}
	if current.Appearance.AccentColor != "#2eb85c" {
		t.Errorf("accent = %q, want #2eb85c", current.Appearance.AccentColor)
	}
	if current.Appearance.Theme != "dark" {
		t.Errorf("theme lost on merge: %q", current.Appearance.Theme)
	}
	if !current.Preferences.SnapToGrid {
		t.Error("snapToGrid lost on merge")
	}
}

func TestEffectiveSettingsForLegacyUsers(t *testing.T) {
	u := &User{} // pre-settings account: no settings document
	s := u.EffectiveSettings()
	if s.Appearance.Theme != "system" || !s.Privacy.ShowPresence {
		t.Errorf("legacy user did not get defaults: %+v", s)
	}
}
