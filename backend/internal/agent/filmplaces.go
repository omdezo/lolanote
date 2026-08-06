package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// Where the sun actually is.
//
// The ephemeris was computed for Muscat and only for Muscat, which is right for
// most of this workspace's work and quietly wrong for the rest: Salalah sits
// 6.5° further south and 4.3° further west, so its sunset in June is more than
// half an hour off Muscat's. Half an hour is a setup. A call sheet that says
// "last light 19:04" when the location loses it at 18:31 is the kind of wrong
// that costs the shot everybody drove five hundred kilometres for.
//
// Two ways in, because a card says what a person writes on a card. Either
// coordinates, when somebody has pasted them off a map — or the place's NAME,
// which is what actually appears on a recce card and a call sheet. The gazetteer
// is small and Omani on purpose: this is not a geocoder and must never look like
// one, so an unknown place falls back to Muscat and SAYS it did.

// omanPlace is one filming location and where it is.
type omanPlace struct {
	Name    string
	Aliases []string
	Lat     float64
	Lng     float64
}

// omanPlaces are the locations this country's productions actually shoot,
// including the ones far enough from Muscat for the difference to matter.
//
// Approximate to about a kilometre, which is three orders of magnitude finer
// than sunrise arithmetic needs — the point is the DEGREE of latitude, not the
// street.
var omanPlaces = []omanPlace{
	{Name: "Muscat", Aliases: []string{"muscat", "مسقط", "ruwi", "قريات"}, Lat: 23.588, Lng: 58.408},
	{Name: "Mutrah", Aliases: []string{"mutrah", "muttrah", "مطرح"}, Lat: 23.617, Lng: 58.564},
	{Name: "Seeb", Aliases: []string{"seeb", "السيب"}, Lat: 23.670, Lng: 58.190},
	{Name: "Qurayyat", Aliases: []string{"qurayyat", "quriyat"}, Lat: 23.259, Lng: 58.913},
	{Name: "Barka", Aliases: []string{"barka", "بركاء"}, Lat: 23.706, Lng: 57.889},
	{Name: "Sohar", Aliases: []string{"sohar", "صحار"}, Lat: 24.347, Lng: 56.709},
	{Name: "Nizwa", Aliases: []string{"nizwa", "نزوى"}, Lat: 22.933, Lng: 57.533},
	{Name: "Bahla", Aliases: []string{"bahla", "بهلاء"}, Lat: 22.964, Lng: 57.300},
	{Name: "Al Hamra", Aliases: []string{"al hamra", "الحمراء", "misfat"}, Lat: 23.117, Lng: 57.300},
	{Name: "Jebel Shams", Aliases: []string{"jebel shams", "jabal shams", "جبل شمس"}, Lat: 23.238, Lng: 57.264},
	{Name: "Jebel Akhdar", Aliases: []string{"jebel akhdar", "jabal akhdar", "الجبل الأخضر"}, Lat: 23.073, Lng: 57.665},
	{Name: "Wadi Shab", Aliases: []string{"wadi shab", "وادي شاب"}, Lat: 22.840, Lng: 59.235},
	{Name: "Sur", Aliases: []string{"sur,", "sur ", "صور"}, Lat: 22.567, Lng: 59.529},
	{Name: "Ras al Jinz", Aliases: []string{"ras al jinz", "ras al hadd", "رأس الجنز"}, Lat: 22.428, Lng: 59.833},
	{Name: "Wahiba Sands", Aliases: []string{"wahiba", "sharqiya sands", "رمال الشرقية"}, Lat: 21.900, Lng: 58.700},
	{Name: "Duqm", Aliases: []string{"duqm", "الدقم"}, Lat: 19.670, Lng: 57.700},
	{Name: "Masirah", Aliases: []string{"masirah", "مصيرة"}, Lat: 20.667, Lng: 58.900},
	{Name: "Salalah", Aliases: []string{"salalah", "صلالة", "dhofar", "ظفار"}, Lat: 17.019, Lng: 54.089},
	{Name: "Mughsail", Aliases: []string{"mughsail", "mughsayl", "المغسيل"}, Lat: 16.874, Lng: 53.769},
	{Name: "Khasab", Aliases: []string{"khasab", "musandam", "مسندم", "خصب"}, Lat: 26.179, Lng: 56.244},
}

// coordPattern reads a decimal lat/long pair somebody pasted off a map.
//
// Both signs allowed and the ranges checked at use, because a transposed pair —
// 58.4, 23.5 — is a real typo and it puts the shoot in the Arabian Sea with a
// sunset forty minutes wrong rather than failing loudly.
var coordPattern = regexp.MustCompile(`(-?\d{1,3}\.\d{3,6})\s*,\s*(-?\d{1,3}\.\d{3,6})`)

// placeFor resolves where a piece of text is talking about.
//
// Coordinates win over a name, because somebody who pasted them meant that exact
// spot; a name is the gazetteer's guess at a region. Returns ok=false when
// neither is there, so the caller decides what the default is and says so.
func placeFor(text string) (name string, lat, lng float64, ok bool) {
	if m := coordPattern.FindStringSubmatch(text); m != nil {
		la, err1 := strconv.ParseFloat(m[1], 64)
		ln, err2 := strconv.ParseFloat(m[2], 64)
		if err1 == nil && err2 == nil && la >= -90 && la <= 90 && ln >= -180 && ln <= 180 {
			return "stated coordinates", la, ln, true
		}
	}
	low := strings.ToLower(text)
	best, bestLen := omanPlace{}, 0
	for _, p := range omanPlaces {
		for _, a := range p.Aliases {
			// Longest alias wins: "Wadi Shab" must not resolve as whatever else
			// on the card happens to contain a shorter token, and "Jebel Shams"
			// must beat a bare "jebel" if one is ever added.
			if len(a) > bestLen && strings.Contains(low, a) {
				best, bestLen = p, len(a)
			}
		}
	}
	if bestLen == 0 {
		return "", 0, 0, false
	}
	return best.Name, best.Lat, best.Lng, true
}

// placeForRun resolves the location for a dated scene: what the card says
// first, then what the board is called, then Muscat.
//
// The fallback is named in the return so the caller can print it. A sunset
// computed for the wrong place looks exactly like a sunset computed for the
// right one, and the only defence is saying which place it was.
func placeForRun(s *BoardScope, text string) (name string, lat, lng float64, assumed bool) {
	if n, la, ln, ok := placeFor(text); ok {
		return n, la, ln, false
	}
	if s != nil && s.Board != nil {
		if t, _ := s.Board.Content["title"].(string); t != "" {
			if n, la, ln, ok := placeFor(t); ok {
				return n, la, ln, false
			}
		}
	}
	return "Muscat", muscatLat, muscatLng, true
}
