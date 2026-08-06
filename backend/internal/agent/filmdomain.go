package agent

import (
	"fmt"
	"sort"
	"strings"
)

// The domain pack: film production as it is actually practised, and Oman.
//
// The whole of the agent's film knowledge used to be twelve lines in the system
// prompt naming artefacts — "call sheets, principal photography, dailies,
// continuity log, sound report" — and not one of them had a structure attached.
// Every one is a real document a practitioner recognises instantly and rejects
// if malformed. A call sheet has a header block, a scene table, a cast table
// with per-person makeup and set times, department notes, walkie channels,
// weather, sunrise/sunset, the nearest hospital and tomorrow's advance
// schedule. The agent knew the WORD and produced a sticky note saying "Call
// sheets" — which is the precise moment a filmmaker decides the tool is a toy:
// it used the right word and then proved it did not know what the word meant.
//
// So: the model NAMES the artefact and the server supplies the structure. Same
// division of labour as cardSwatches — the model says "yellow", the server
// knows the hex — and the same one the digest already runs on everywhere else.
//
// Two properties this file is built around, and both of them are the reason it
// is a table rather than a longer prompt:
//
//   - IT COSTS NOTHING ON A BOARD THAT IS NOT ABOUT FILM. The DOMAIN block is
//     emitted only when the board's own words trigger it. A sprint board never
//     pays a token for any of this.
//   - EVERY REGULATORY FACT IS CITED AND DATED. An agent that asserts "the
//     midday ban is 12:30–15:30" is guessing as far as anyone reading can tell;
//     one that says "Ministerial Resolution 286, as of 2026-07" can be checked,
//     and goes stale visibly rather than silently. Same provenance rule the
//     trust labels already enforce on board content, applied to knowledge.

// ---- what triggers the pack ------------------------------------------------

// filmWords are the tokens that say this board is production work.
//
// Deliberately narrow and deliberately bilingual. A word like "shot" or "cast"
// on its own is ordinary English — "cast" appears on a foundry board and "grade"
// on a school one — so the list is made of terms that are film in context and
// the threshold below is what stops a single coincidence from switching the
// pack on.
var filmWords = []string{
	"scene", "slugline", "int.", "ext.", "shot list", "shotlist", "storyboard",
	"call sheet", "callsheet", "call time", "stripboard", "breakdown",
	"day out of days", "dood", "principal photography", "dailies", "rushes",
	"continuity", "script supervisor", "screenplay", "treatment", "logline",
	"synopsis", "1st ad", "first ad", "gaffer", "best boy", "grip", "dop",
	"director of photography", "cinematographer", "boom op", "production design",
	"picture lock", "offline edit", "online edit", "conform", "colour grade",
	"color grade", "sound mix", "adr", "foley", "m&e", "deliverables",
	"dcp", "prores", "festival", "casting", "recce", "location scout",
	"shooting schedule", "shoot day", "wrap", "turnaround", "company move",
	// Arabic. A production board in Muscat is as likely to be written in these
	// as in English, and the register item (a mixed ar/en board is this user's
	// NORMAL state) means the trigger has to see both halves of one board.
	"مشهد", "لقطة", "تصوير", "سيناريو", "مخرج", "إنتاج", "مونتاج", "ممثل",
	"جدول التصوير", "تسليم",
}

// filmTriggerHits is how many distinct domain words a board must show before
// the pack is worth its tokens. Two, because one is a coincidence — a card
// saying "scene" on a design board — and two of these words together is not.
const filmTriggerHits = 2

// looksLikeProduction reports whether this board's own vocabulary is film.
//
// Over the digest's rendered text rather than over element types, because the
// signal is words: the same COLUMN holds a sprint on one board and a shooting
// schedule on another, and only what is written in it can tell them apart.
func looksLikeProduction(s *BoardScope) bool {
	if s == nil {
		return false
	}
	// The REQUEST counts on its own, and one term in it is enough.
	//
	// A board is evidence about what already exists; a request is evidence about
	// what is about to. "Make tomorrow's call sheet" on an empty board is the
	// single most important thing this pack exists for, and the board-vocabulary
	// trigger answered it with silence — so the agent, asked for the artefact
	// this whole corner is built around, was handed none of its structure.
	if IntentLooksLikeProduction(s.Intent) {
		return true
	}
	var b strings.Builder
	if s.Board != nil {
		if t, _ := s.Board.Content["title"].(string); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	for _, it := range s.Items {
		b.WriteString(it.Text)
		b.WriteByte('\n')
	}
	for _, c := range s.ExistingColumns {
		b.WriteString(c)
		b.WriteByte('\n')
	}
	for _, h := range s.History {
		b.WriteString(h.Intent)
		b.WriteByte('\n')
	}
	return countFilmWords(b.String()) >= filmTriggerHits
}

// countFilmWords counts DISTINCT domain terms, not occurrences.
//
// Occurrences would let one column titled "Scene" repeated forty times switch
// the pack on by itself, which is the coincidence the threshold exists to
// exclude.
func countFilmWords(text string) int {
	low := strings.ToLower(text)
	seen := 0
	for _, w := range filmWords {
		if strings.Contains(low, w) {
			seen++
		}
	}
	return seen
}

// IntentLooksLikeProduction reports whether a REQUEST is film work, for the
// case the board cannot answer: an empty board and "make tomorrow's call
// sheet".
//
// Exported because the eval harness seeds empty boards for exactly these probes
// and would otherwise be measuring the pack's trigger rather than the answer.
func IntentLooksLikeProduction(intent string) bool {
	return countFilmWords(intent) >= 1
}

// ---- the artefacts ---------------------------------------------------------

// artefactShape is the board shape a document takes. Naming it is half the
// value of the spec: the recurring failure is not a wrong column, it is a scene
// list built as forty columns or a call sheet built as one note.
type artefactShape string

const (
	shapeTable     artefactShape = "TABLE"
	shapeBoard     artefactShape = "BOARD"
	shapeColumns   artefactShape = "COLUMNS"
	shapeChecklist artefactShape = "CHECKLIST"
	shapeDocument  artefactShape = "DOCUMENT"
	shapeGraph     artefactShape = "GRAPH"
)

// artefactSpec is one production document, as the trade defines it.
type artefactSpec struct {
	// Key is what film_spec takes. Kebab-case, because it is typed by a model.
	Key string
	// Name is the trade's own name for it, and NameAR is what an Omani crew
	// would actually call it — which is sometimes the transliterated English,
	// and saying so is the point (see the register block).
	Name   string
	NameAR string
	// Shape is the board shape the document takes.
	Shape artefactShape
	// Columns are the canonical headers where the shape is a table. This is the
	// cheapest domain competence in the whole pack: the prompt already tells the
	// agent that a shot list wants a table and then leaves it to invent the
	// headers, and a shot list with no movement or lens column is a list of
	// sentences that a DP rejects in two seconds.
	Columns []string
	// Fields are the required parts where the shape is not a grid.
	Fields []string
	// Note is the one thing a practitioner knows that a generic answer misses.
	Note string
	// Oman is the local overlay, where there is one.
	Oman string
	// Facts are external, dated, citable knowledge this document needs — a
	// festival's window and fee tiers, a regulator's rule.
	//
	// Carried on the artefact rather than in the always-on block because it is
	// the most perishable knowledge in the pack and the least often needed: a
	// deadline is true for a season, and a run that is not building a festival
	// tracker should not be paying for one. Deliberately NOT a scraper — the
	// pack states when it was compiled and the agent says so, which is the only
	// honest way to hold a date that moves.
	Facts []domainFact
	// Destinations are the variants of this document that differ by where the
	// work is GOING.
	//
	// Only the delivery list has them, and it is the reason the field exists: a
	// festival DCP, a broadcaster, a streamer and a self-release share a core and
	// then diverge completely, and a single flat list is wrong for all four. The
	// agent that hands a festival the streamer's list has produced forty items
	// nobody asked for and omitted the one — a SMPTE-conformant DCP subtitle
	// track — that stops the screening.
	Destinations []deliveryDestination
}

// deliveryDestination is one place a finished film goes, and what that place
// asks for on top of the core list.
type deliveryDestination struct {
	Key   string
	Name  string
	Items []string
	Note  string
}

// filmArtefacts is the table. Ordered, so the digest's list and the tool's
// refusal both read the same way every run and the prompt cache is not broken
// by map iteration.
var filmArtefacts = []artefactSpec{
	{
		Key: "call-sheet", Name: "Call Sheet", NameAR: "ورقة النداء (call sheet)",
		Shape: shapeBoard,
		Fields: []string{
			"production title, DAY x OF y, date",
			"general crew call, and the shooting call if it differs",
			"set address AND a separate parking / basecamp address",
			"nearest hospital, with the route",
			"weather, with sunrise and sunset",
			"scene table: scene #, I/E, description, D/N, pages, cast IDs, location",
			"cast table: cast ID, character, artist, pickup / makeup-hair / on-set time, status",
			"crew grid by department",
			"department notes",
			"walkie channels",
			"advance schedule — what tomorrow is",
		},
		Note: "A BOARD, never one note. Headings for the regions, create_table twice " +
			"(scenes, cast), a column for department notes, a document for the header " +
			"block. Any field you cannot source — hospital, weather, parking — goes in " +
			"unmet. NEVER invent one: a call sheet is trusted on sight and a wrong " +
			"hospital address is the worst thing on this page.",
		Oman: "times are local wall clock (Asia/Muscat, +04, no DST); a summer EXT day " +
			"has a legal stop 12:30–15:30 (see the OMAN block).",
	},
	{
		Key: "breakdown", Name: "Breakdown Sheet", NameAR: "تفريغ السيناريو",
		Shape:   shapeTable,
		Fields:  []string{"production", "scene #", "page count in eighths", "I/E", "set", "D/N", "synopsis"},
		Columns: []string{"Scene", "I/E", "Set", "D/N", "Pages", "Cast", "Elements", "Notes"},
		Note: "The pivot of the craft: schedule, budget and every department's prep " +
			"derive from it. The element categories are the TWENTY standard ones and " +
			"they are a fixed vocabulary, not a set you invent — see BREAKDOWN " +
			"CATEGORIES. Tag with labels, not with columns: a card has one background " +
			"colour (its strip colour) and many labels (its elements), and the two " +
			"systems are orthogonal in this trade.",
	},
	{
		Key: "shot-list", Name: "Shot List", NameAR: "قائمة اللقطات",
		Shape: shapeTable,
		Columns: []string{"Scene", "Shot", "Size", "Angle", "Movement", "Lens",
			"Subject", "Description", "Equipment", "Est. time", "Done"},
		Note: "Sizes are the trade's: VWS / WS / MS / MCU / CU / ECU. A shot list " +
			"without a movement or a lens column is a list of sentences — that is the " +
			"omission a DP notices immediately.",
	},
	{
		Key: "stripboard", Name: "Stripboard", NameAR: "لوحة الشرائط (stripboard)",
		Shape:   shapeTable,
		Columns: []string{"Strip", "Scene", "I/E", "Set", "D/N", "Pages", "Cast", "Day"},
		Note: "One strip per scene, 60–200 of them for a feature: this is a document " +
			"with a shape the trade decided, and the 'three to six containers' guidance " +
			"does not apply to it. Strip colour is a CODE, not decoration — INT/DAY " +
			"white, EXT/DAY yellow, EXT/NIGHT blue, INT/NIGHT green.",
	},
	{
		Key: "dood", Name: "Day Out of Days", NameAR: "Day Out of Days (DOOD)",
		Shape:   shapeTable,
		Columns: []string{"Cast", "Day 1", "Day 2", "…", "Work", "Hold", "Travel", "Total"},
		Note: "A MATRIX: cast down, shooting days across, one code per cell. The " +
			"alphabet is fixed — SW start work, W work, WF work finish, SWF " +
			"start-work-finish, H hold (not used, still paid), T travel, R rehearsal. " +
			"Its whole value is the arithmetic: hold days are days an actor is paid for " +
			"and not used, and a DOOD is the report that justifies reordering a shoot.",
	},
	{
		Key: "schedule", Name: "Shooting Schedule", NameAR: "جدول التصوير",
		Shape: shapeColumns,
		Note: "One column per SHOOTING DAY — eighteen to thirty of them on a modest " +
			"feature, and that is correct, not a wall. Shooting order is NOT story " +
			"order: a schedule is grouped by location, then company move, then day/night, " +
			"then cast availability. Use regroup for that, never a story-order sort.",
	},
	{
		Key: "budget", Name: "Budget Top Sheet", NameAR: "الميزانية التقديرية",
		Shape:   shapeTable,
		Columns: []string{"Account", "Category", "Estimate", "Actual", "Variance", "Notes"},
		Fields: []string{
			"ABOVE THE LINE — story & rights, writer, producer, director, cast, casting",
			"PRODUCTION (below the line) — AD/stage management, camera, electric, grip, " +
				"art, set dressing, props, wardrobe, HMU, sound, locations, transport, catering",
			"POST — editorial, picture finishing, sound post, music, VFX, deliverables",
			"OTHER BELOW THE LINE — insurance, legal, publicity, general expenses",
			"FRINGES — the loading on every labour line",
			"CONTINGENCY — 10–15%",
		},
		Note: "Build the accounts with the figures BLANK rather than inventing money. " +
			"Two lines a first-time producer omits and a financier checks first: " +
			"FRINGES and CONTINGENCY. Say so out loud when contingency is missing or " +
			"under 10% — that judgement is objective here, so it is a fact you can " +
			"state rather than an opinion you are offering. Typical split: ATL 30–35%, " +
			"production 25–30%, post 20–25%, contingency 10–15%.",
	},
	{
		Key: "casting", Name: "Casting Pipeline", NameAR: "مسار الاختيار",
		Shape: shapeColumns,
		Fields: []string{"Breakdown released", "Submissions", "Sides issued", "Self-tape",
			"First read", "Callback", "Avail check", "Pin", "Hold", "Offer", "Deal memo",
			"Contract", "Scheduled"},
		Note: "A kanban, and the column names are not yours to invent: 'pinned', 'on " +
			"avail' and 'on hold' are distinct states with contractual and financial " +
			"meaning that 'maybe' flattens. An avail check and a pin both EXPIRE, so " +
			"those two columns are where a due date earns its place.",
	},
	{
		Key: "crew", Name: "Crew List", NameAR: "قائمة طاقم العمل",
		Shape: shapeGraph,
		Fields: []string{
			"Camera — DP → operator → 1st AC (focus puller) → 2nd AC (loader) → DIT",
			"Electric — gaffer → best boy electric → electricians",
			"Grip — key grip → best boy grip → dolly grip → grips",
			"Sound — mixer → boom operator → utility",
			"Art — production designer → art director → set decorator → props master → set dressers",
			"AD — 1st AD → 2nd AD → 2nd 2nd AD → PAs",
			"Locations, wardrobe, hair & makeup, script supervisor, production office",
		},
		Note: "A departmental tree with a strict chain, and the rule is DO NOT SKIP A " +
			"RUNG. Camera: DP → operator → 1st AC (focus puller) → 2nd AC (loader) → " +
			"DIT. Electric: gaffer → best boy electric → electricians. Grip: key grip → " +
			"best boy grip → dolly grip → grips. Sound: mixer → boom op → utility. Art: " +
			"production designer → art director → set decorator → props master → set " +
			"dressers. AD: 1st AD → 2nd AD → 2nd 2nd AD → PAs. Plus locations, wardrobe, " +
			"HMU, script supervisor, production office. SCALE IT: a crew of six collapses " +
			"roles (director/DP, sound recordist, gaffer-grip, 1st AD/production, art, " +
			"runner) — listing forty positions for a six-person shoot is the generic answer.",
		Oman: "a local fixer / service producer and a government-facing coordinator are " +
			"real line items here, not overhead.",
	},
	{
		Key: "deliverables", Name: "Deliverables & QC", NameAR: "قائمة التسليم",
		Shape: shapeChecklist,
		Fields: []string{
			"picture masters — ProRes 422 HQ, HD and 4K",
			"textless elements",
			"DCDM / DCP for a theatrical grade",
			"audio 48 kHz / 24-bit, mix normalised to −24 LKFS",
			"fully-filled 5.1 M&E submix — foley and ambience fill, NO dialogue",
			"stems (dialogue / music / effects)",
			"Dolby Atmos where applicable",
			"captions IMSC1 / TTML with xml:lang, tts:origin, tts:extent and a " +
				"ttp:frameRate matching picture",
			"DCP subtitles conforming to SMPTE ST 428-7:2014",
			"chain of title and clearance paperwork",
			"a manual QC pass",
		},
		Note: "The one film artefact that is natively a checklist, so it is a real " +
			"create_todo with the specs attached and a document beside it carrying the " +
			"numbers. Delivery is where independent films die: a bounce for a missing " +
			"M&E or a wrong LKFS costs weeks and money, and M&E is the one people " +
			"forget. THE LIST DIFFERS BY WHERE IT IS GOING — call film_spec again with a " +
			"destination (festival, broadcaster, streamer, self) for the items that " +
			"destination adds, and ask which one it is rather than guessing.",
		Destinations: []deliveryDestination{
			{
				Key: "festival", Name: "Festival / theatrical",
				Items: []string{
					"DCP, SMPTE, unencrypted unless the festival asks for a KDM",
					"DCP subtitles conforming to SMPTE ST 428-7:2014 — burnt-in only if the " +
						"festival says so",
					"a screener (H.264, 1080p) with the festival's own naming",
					"5.1 mix at −27 LKFS for theatrical; the −24 figure is a broadcast number",
					"dialogue list and a spotting list for the festival's subtitlers",
					"press kit: stills at 300 dpi, director bio, synopsis in two lengths",
				},
				Note: "PREMIERE STATUS IS SPENT ONCE — check the festival tracker before " +
					"anything is sent anywhere.",
			},
			{
				Key: "broadcaster", Name: "Broadcaster",
				Items: []string{
					"programme master to the broadcaster's own spec — ask, never assume",
					"textless elements at the tail",
					"a fully-filled 5.1 M&E, foley and ambience complete, NO dialogue",
					"audio 48 kHz / 24-bit, mix normalised to −24 LKFS (EBU R128 territories: −23 LUFS)",
					"clock, bars and tone, and a stated programme start timecode",
					"as-run durations per part, and the part boundaries",
					"compliance: flash-and-pattern check, legal levels",
					"music cue sheet with timings, composers and shares",
				},
				Note: "The M&E is the item people forget, and it is the one a broadcaster " +
					"cannot dub without.",
			},
			{
				Key: "streamer", Name: "Streamer / platform",
				Items: []string{
					"IMF package where the platform takes one; otherwise ProRes 422 HQ at native res",
					"stems: dialogue, music, effects, separately",
					"Dolby Atmos where the platform carries it, plus a 5.1 fold-down",
					"IMSC1 / TTML captions with xml:lang, tts:origin, tts:extent and a " +
						"ttp:frameRate matching picture",
					"forced narrative subtitles as a separate track",
					"audio description and SDH where the platform requires them",
					"artwork at every requested aspect, title-safe",
					"E&O insurance and a clean chain of title — a platform will not take " +
						"delivery without them",
				},
			},
			{
				Key: "self", Name: "Self-distribution / online",
				Items: []string{
					"H.264 / H.265 masters at the resolutions you will actually publish",
					"a −16 LUFS stereo mix for online platforms, and the −24 broadcast mix kept separately",
					"burnt-in subtitle versions per language, because most players will not " +
						"carry a sidecar",
					"a textless master kept for the versions you have not thought of yet",
					"music licences that cover ONLINE use specifically — a festival licence does not",
				},
				Note: "The cheapest route and the one where rights are most often assumed " +
					"rather than cleared.",
			},
		},
	},
	{
		Key: "post", Name: "Post Pipeline", NameAR: "مراحل ما بعد الإنتاج",
		Shape: shapeGraph,
		Fields: []string{
			"offline edit → picture lock",
			"picture lock BLOCKS turnover (AAF / XML / EDL out)",
			"turnover BLOCKS conform to the full-res camera masters",
			"conform BLOCKS online / VFX pulls and the colour grade",
			"sound: spot → sound edit → ADR → foley → pre-dub → final mix",
			"the final mix DEPENDS ON locked picture; M&E DEPENDS ON the mix",
			"M&E → layback → masters → QC → deliver",
		},
		Note: "The canonical dependency graph of this trade, and the reason to reach " +
			"for design_as(\"flow\") and connect: offline edit → picture lock → turnover " +
			"(AAF/XML/EDL) → conform to camera masters → online / VFX pulls → colour " +
			"grade → sound (spot, edit, ADR, foley, pre-dub, final mix) → M&E → layback " +
			"→ masters → QC → deliver. Direction matters and a professional sees it " +
			"instantly: picture lock BLOCKS conform, conform BLOCKS grade, the mix " +
			"DEPENDS ON locked picture, M&E DEPENDS ON the mix.",
	},
	{
		Key: "festivals", Name: "Festival Tracker", NameAR: "متابعة المهرجانات",
		Shape: shapeTable,
		Columns: []string{"Festival", "Deadline", "Fee tier", "Premiere required",
			"Submitted", "Notification", "Decision"},
		Note: "PREMIERE STATUS IS A RESOURCE YOU SPEND ONCE. Most major festivals " +
			"require a world premiere and showing anywhere first disqualifies you — so " +
			"this is the only part of the job with genuinely irreversible moves, and the " +
			"one place to warn before acting rather than after. Fee tiers (early / " +
			"regular / late) make it date-dense; columns by decision state " +
			"(researching / submitted / selected / declined) with the table beside them.",
		Oman: "the regional ladder is the real career path here: Muscat International " +
			"Film Festival (Omani short narrative and short documentary competitions), " +
			"Red Sea IFF, Cairo, El Gouna.",
		Facts: []domainFact{
			{
				Topic: "Premiere status",
				Text: "Most major festivals require a WORLD premiere and showing anywhere " +
					"first disqualifies you — including online. It is the only resource in " +
					"this trade you spend once, so warn BEFORE a submission, not after. " +
					"Berlinale: world premieres are prioritised in every section; only " +
					"Perspectives, Panorama, Generation and Forum accept a European " +
					"premiere; national premieres are not accepted; anything already " +
					"broadcast on television or shown on the internet or VOD is out; and " +
					"it takes no FilmFreeway submissions.",
				Source: "Berlinale regulations", AsOf: factsAsOf,
			},
			{
				Topic: "Red Sea IFF",
				Text: "Submissions 26 Apr – 2 Aug 2026; free until 7 Jul, then 100 SAR for " +
					"shorts and 200 SAR for features. Festival runs 3–12 Dec 2026. The Red " +
					"Sea Souk project market has its own deadlines, 11 Jun and 24 Jul.",
				Source: "Red Sea International Film Festival submission terms", AsOf: factsAsOf,
			},
			{
				Topic: "Muscat International Film Festival",
				Text: "Running since 2000, with dedicated Omani short narrative and Omani " +
					"short documentary competitions — the one ladder rung with a category " +
					"reserved for this country's work.",
				Source: "Muscat International Film Festival", AsOf: factsAsOf,
			},
		},
	},
	{
		Key: "recce", Name: "Location Recce", NameAR: "معاينة الموقع",
		Shape: shapeTable,
		Columns: []string{"Location", "Power", "Parking / basecamp", "Sound", "Access / load-in",
			"Sun path", "Permits", "Safety", "Facilities", "Signal", "Cost"},
		Fields: []string{
			"power — is there a 400 A service or does this need a genny, and where do cables run",
			"parking and basecamp capacity within walking distance",
			"sun path — where it rises and sets relative to the location, and what that does to the day",
			"sound contamination — flight paths, freeways, rail, HVAC — and local noise restrictions",
			"access and load-in; rigging points",
			"permits and heritage restrictions",
			"safety; toilets, holding and catering space; mobile signal",
		},
		Note: "The real recce list is longer and harsher than any list a model produces " +
			"unprompted, and every omitted row is a shoot day lost. Tech scout with " +
			"department heads 1–2 weeks before the shoot.",
		Oman: "add: heritage-site approval via the Ministry of Heritage & Tourism; " +
			"whether the site needs a military observer; whether there is SHADE for the " +
			"12:30–15:30 stop; and water and heat provisioning.",
	},
	{
		Key: "lookbook", Name: "Lookbook", NameAR: "الكتيب البصري",
		Shape: shapeBoard,
		Note: "The one development artefact this product is genuinely the best tool " +
			"for: swatch cards for the palette, image cards for reference, link cards " +
			"for sources, and a document for the treatment. The chain around it is " +
			"logline → synopsis → treatment → lookbook → pitch deck / prospectus → " +
			"budget → finance plan → co-production structure → sales agent → chain of title.",
	},
	{
		Key: "clearances", Name: "Rights & Clearances", NameAR: "الحقوق والتصاريح",
		Shape: shapeChecklist,
		Fields: []string{
			"chain of title — copyright search, UCC and lien searches, every contract " +
				"creating, acquiring or transferring rights",
			"E&O insurance — required by all major streamers; it REQUIRES clean chain of " +
				"title, signed releases and music clearance",
			"music: master use AND synchronisation licences, separately",
			"documentary adds: appearance releases, archive licences, parental consent for minors",
			"location releases",
		},
		Note: "The half of the job nobody enjoys and the half that kills films: a " +
			"documentary delivered without appearance releases or archive licences " +
			"cannot be insured, and therefore cannot be distributed. State WHY each " +
			"item exists — the E&O dependency is what makes the list persuasive rather " +
			"than bureaucratic.",
	},
	{
		Key: "shoot-day", Name: "Shoot Day", NameAR: "يوم تصوير",
		Shape: shapeBoard,
		Fields: []string{
			"the day's scenes as cards, in shooting order",
			"a takes table — scene, slate, take, circled, notes",
			"a media log — roll, camera, card size, destinations, checksum verified, cleared by",
			"a wrap-paperwork checklist",
		},
		Note: "The shoot has a physical rhythm: crew call, first shot, lunch (a hard " +
			"six-hour meal clock on union shoots), company move, wrap, and TURNAROUND — " +
			"the mandated rest between wrap and the next call, which is what makes a " +
			"late wrap cascade into a late call tomorrow. The day's own paperwork is the " +
			"script supervisor's: lined script, editor's log (circled takes), continuity " +
			"log (wardrobe, props, hair/makeup, screen direction, action match), plus " +
			"camera and sound reports per roll. The DPR at the end is derived, not " +
			"invented: pages shot vs scheduled, setups, first shot / lunch / wrap, issues.",
	},
	{
		Key: "media-log", Name: "Media Management", NameAR: "إدارة الوسائط",
		Shape: shapeChecklist,
		Columns: []string{"Roll", "Camera", "Card size", "Copy 1", "Copy 2", "Copy 3 (off-site)",
			"Checksum verified", "Cleared by"},
		Note: "3-2-1 WITH VERIFIED CHECKSUMS: three copies, two technologies (so they " +
			"cannot share a failure mode), one geographically separate; transfer logs " +
			"retained per roll; and an explicit CLEAR-TO-FORMAT HANDSHAKE between camera " +
			"or sound and the DIT before any card is wiped. Losing a card is the one " +
			"production failure money and time cannot recover, and on a small crew there " +
			"is no DIT — the director is the DIT. 'Back up the footage' as a single " +
			"to-do item is the failure this discipline exists to prevent.",
	},
	{
		Key: "dpr", Name: "Daily Production Report", NameAR: "التقرير اليومي للإنتاج",
		Shape: shapeDocument,
		Fields: []string{
			"day x of y, date, unit",
			"call, first shot, lunch in / out, first shot after lunch, wrap",
			"pages shot against pages scheduled, in eighths",
			"scenes completed / part-completed / not shot",
			"setups completed",
			"cast worked, and any held",
			"issues, accidents, lost time and why",
		},
		Note: "DERIVED, never invented: every field on it comes off the day's shot list, " +
			"the day's marked cards and the call sheet that was issued. It is the join " +
			"between production and post — the editor's log is how the cutting room knows " +
			"which take to load — and generating it from the day's board is doing the job " +
			"of a second AD. \"Pages shot\" is the headline number and it is in eighths; " +
			"do not do that arithmetic yourself.",
	},
	{
		Key: "scene-list", Name: "Scene List", NameAR: "قائمة المشاهد",
		Shape:   shapeTable,
		Columns: []string{"#", "I/E", "Set", "D/N", "Pages", "Cast", "Notes"},
		Note: "A scene is a ROW, never a column: '3 INT. HARBOUR OFFICE – NIGHT' is 29 " +
			"characters and a column header holds 20, so a column per scene destroys " +
			"INT/EXT and D/N — the two facts that decide lighting, permits and schedule " +
			"order. If a scene needs a card of its own, the number leads the title and " +
			"the slugline is its first line.",
	},
}

// artefactFor resolves a film_spec key.
func artefactFor(key string) (artefactSpec, bool) {
	want := strings.ToLower(strings.TrimSpace(key))
	want = strings.ReplaceAll(want, " ", "-")
	want = strings.ReplaceAll(want, "_", "-")
	for _, a := range filmArtefacts {
		if a.Key == want {
			return a, true
		}
	}
	// Tolerance for the near-misses a model actually types, resolved by the
	// artefact's own NAME rather than by a second alias table nobody maintains.
	for _, a := range filmArtefacts {
		if strings.EqualFold(strings.ReplaceAll(a.Name, " ", "-"), want) {
			return a, true
		}
	}
	return artefactSpec{}, false
}

// deliveryDestinationKeys is the destination menu for the tool schema.
//
// Read off the artefact table rather than written out beside it: a destination
// added to the spec and forgotten in the enum is a value the model can never
// send, which is the same drift the content-key checks exist to catch.
func deliveryDestinationKeys() []string {
	for _, a := range filmArtefacts {
		if len(a.Destinations) > 0 {
			return a.destinationKeys()
		}
	}
	return nil
}

// artefactKeys is the menu, in table order.
func artefactKeys() []string {
	out := make([]string, 0, len(filmArtefacts))
	for _, a := range filmArtefacts {
		out = append(out, a.Key)
	}
	return out
}

// Render writes one spec as the model receives it.
func (a artefactSpec) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s (%s) · shape: %s\n", strings.ToUpper(a.Key), a.Name, a.NameAR, a.Shape)
	if len(a.Columns) > 0 {
		fmt.Fprintf(&b, "COLUMNS (use these headers, in this order): %s\n", strings.Join(a.Columns, " | "))
	}
	if len(a.Fields) > 0 {
		b.WriteString("REQUIRED:\n")
		for _, f := range a.Fields {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if a.Note != "" {
		fmt.Fprintf(&b, "WHAT A PRACTITIONER KNOWS: %s\n", a.Note)
	}
	if a.Oman != "" {
		fmt.Fprintf(&b, "IN OMAN: %s\n", a.Oman)
	}
	if len(a.Facts) > 0 {
		b.WriteString("DATED FACTS — cite the source and the date when you use one, and say " +
			"plainly that a deadline moves and this pack was compiled at a point in time. " +
			"An uncited deadline is indistinguishable from an invented one:\n")
		for _, f := range a.Facts {
			fmt.Fprintf(&b, "  %s: %s [%s, as of %s]\n", f.Topic, f.Text, f.Source, f.AsOf)
		}
	}
	if len(a.Destinations) > 0 {
		keys := make([]string, 0, len(a.Destinations))
		for _, d := range a.Destinations {
			keys = append(keys, d.Key)
		}
		fmt.Fprintf(&b, "DESTINATIONS — this document differs by where the work is going. Call "+
			"film_spec again with destination=<%s> for the items that one adds on top of the "+
			"required list above.\n", strings.Join(keys, " | "))
	}
	return b.String()
}

// destinationFor resolves a delivery destination on an artefact, or reports the
// menu it should have asked from.
func (a artefactSpec) destinationFor(key string) (deliveryDestination, bool) {
	want := strings.ToLower(strings.TrimSpace(key))
	for _, d := range a.Destinations {
		if d.Key == want || strings.EqualFold(d.Name, want) {
			return d, true
		}
	}
	return deliveryDestination{}, false
}

// destinationKeys is the menu for one artefact.
func (a artefactSpec) destinationKeys() []string {
	out := make([]string, 0, len(a.Destinations))
	for _, d := range a.Destinations {
		out = append(out, d.Key)
	}
	return out
}

// RenderFor writes the spec plus the one destination's extra items.
//
// Additive rather than a replacement list, because the core is genuinely common
// and a model handed two overlapping full lists will build both.
func (a artefactSpec) RenderFor(d deliveryDestination) string {
	var b strings.Builder
	b.WriteString(a.Render())
	fmt.Fprintf(&b, "\nFOR %s — ON TOP OF the required list above, not instead of it:\n",
		strings.ToUpper(d.Name))
	for _, item := range d.Items {
		fmt.Fprintf(&b, "  - %s\n", item)
	}
	if d.Note != "" {
		fmt.Fprintf(&b, "WATCH: %s\n", d.Note)
	}
	return b.String()
}

// ---- the twenty breakdown categories ---------------------------------------

// breakdownCategory is one element category and its traditional marking colour.
//
// The colours are not decoration: a breakdown is READ by colour across a table,
// and the product's swatch palette happens to contain the ones the trade uses.
// Only the categories with a traditional colour carry one — inventing a colour
// for the other fifteen would be exactly the fabrication this pack exists to
// stop.
type breakdownCategory struct {
	Name   string
	Colour string
}

var breakdownCategories = []breakdownCategory{
	{"Cast", ""},
	{"Extras", "green"},
	{"Stunts", "orange"},
	{"Vehicles", ""},
	{"Props", "purple"},
	{"Costumes", ""},
	{"Makeup", ""},
	{"Livestock", ""},
	{"Animal Handlers", ""},
	{"Music", ""},
	{"Sound", ""},
	{"Set Dressing", ""},
	{"Greenery", ""},
	{"Special Equipment", ""},
	{"Security", ""},
	{"Additional Labor", ""},
	{"Optical Effects", "blue"},
	{"Mechanical Effects", "blue"},
	{"Miscellaneous", ""},
	{"Scene Notes", ""},
}

// breakdownVocabulary renders the twenty as the seeded label vocabulary, with
// the colours where the trade has one.
func breakdownVocabulary() string {
	parts := make([]string, 0, len(breakdownCategories))
	for _, c := range breakdownCategories {
		if c.Colour != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", c.Name, c.Colour))
			continue
		}
		parts = append(parts, c.Name)
	}
	return strings.Join(parts, ", ")
}

// ---- colour as a code ------------------------------------------------------

// stripColours is the stripboard's own key. The product's palette contains the
// trade's four exactly, which is a coincidence worth spending two lines on: a
// producer glances at a board across a room and sees the night-exterior load
// without reading a word.
var stripColours = []string{
	"INT/DAY = default (white)",
	"EXT/DAY = yellow",
	"EXT/NIGHT = blue",
	"INT/NIGHT = green",
}

// revisionWheel is the script revision order. The trade burned nine colours of
// paper on versioning because being one revision behind is how a scene gets
// shot wrong, and the product's palette carries the first five.
var revisionWheel = []string{"White", "Blue", "Pink", "Yellow", "Green", "Goldenrod",
	"Buff", "Salmon", "Cherry", "Tan"}

// ---- the facts, each with a source and a date ------------------------------

// domainFact is a piece of external knowledge the agent may STATE rather than
// assert.
//
// The provenance fields are the whole design. A regulatory rule delivered in
// the agent's own voice is indistinguishable from a fabrication, and it goes
// stale silently; the same rule with its resolution number and an 'as of' date
// can be checked by the person reading it and visibly expires. That is the
// trust-label rule applied to knowledge instead of to board content.
type domainFact struct {
	Topic  string
	Text   string
	Source string
	AsOf   string
}

// factsAsOf is when this pack's regulatory and calendar facts were compiled.
// One constant, so every line goes stale together and nobody has to guess which
// half was refreshed.
const factsAsOf = "2026-07"

var omanFacts = []domainFact{
	{
		Topic: "General Filming Permit",
		Text: "Public filming in Oman needs a General Filming Permit from the Ministry " +
			"of Information, applied for through gov.om. Fee 0 OMR; the stated service " +
			"timeline is 10 business days, but 2–3 weeks is normal and 3–5 weeks happens " +
			"depending on content. Required with the application: a Letter of Intent " +
			"(synopsis, commissioning body, locations, contributors), crew passport " +
			"copies, a full kit list with models, country of manufacture, serial numbers " +
			"and values — with PHOTOGRAPHS of the serial numbers on high-value items — " +
			"and a signed letter authorising the local partner.",
		Source: "Ministry of Information / gov.om service description", AsOf: factsAsOf,
	},
	{
		Topic: "Equipment customs",
		Text: "The filming permit gates temporary import of equipment through customs. " +
			"A deposit of 10% of the kit-list value is held and released about a month " +
			"after the equipment leaves the country. This is why the kit list with serial " +
			"numbers is a real artefact and not paperwork theatre.",
		Source: "Royal Oman Police — Directorate General of Customs, temporary admission",
		AsOf:   factsAsOf,
	},
	{
		Topic: "Drone and aerial",
		Text: "Drone filming needs Civil Aviation Authority approval AND Defence " +
			"approval: 3–4 weeks at best, 6–8 weeks in practice, commercial applicants " +
			"only through a local production company, with a military observer required " +
			"in some areas. Unpermitted drones are confiscated at the airport. Aerial " +
			"permits (manned) run 15–21 days.",
		Source: "Civil Aviation Authority (CAA) UAV permit conditions", AsOf: factsAsOf,
	},
	{
		Topic: "Other authorities",
		Text: "Protected and heritage areas need the Ministry of Heritage & Tourism. " +
			"The Sultan Qaboos Grand Mosque and the Royal Opera House Muscat each grant " +
			"their own permission. Crew visas go through evisa.rop.gov.om.",
		Source: "Ministry of Heritage & Tourism; ROP eVisa", AsOf: factsAsOf,
	},
	{
		Topic: "No rebate",
		Text: "Oman has NO film rebate, cash incentive or tax credit. Say so when a " +
			"budget or finance plan is being built: every generic film-financing source " +
			"assumes an incentive exists, and planning around one that does not is a hole " +
			"in the finance plan rather than a missing line.",
		Source: "absence — no incentive programme published", AsOf: factsAsOf,
	},
	{
		Topic: "Midday outdoor work ban",
		Text: "Outdoor work in open spaces is PROHIBITED 12:30–15:30 from 1 June to 31 " +
			"August. Fines are OMR 500–1,000. Summer temperatures pass 45 °C. A summer " +
			"EXT day is therefore a split day or a night — a 13:00 call in July is not a " +
			"scheduling preference, it is illegal.",
		Source: "Ministerial Resolution No. 286", AsOf: factsAsOf,
	},
	{
		Topic: "Ramadan hours",
		Text: "During Ramadan, Muslim employees are capped at 6 hours a day and 36 hours " +
			"a week, against a standard 12-hour Omani set day — a 50% cut, so a schedule " +
			"crossing Ramadan needs roughly twice the days. Eating, drinking and smoking " +
			"in public during daylight are prohibited, which reshapes catering, crafty " +
			"and the break structure. Ramadan moves about 11 days earlier each Gregorian " +
			"year, so 'can we shoot in March?' has a different answer every year — and " +
			"it is the region's busiest broadcast season, which is when Gulf television " +
			"actually commissions. Eid closes the country for several days after.",
		Source: "Oman Labour Law, Ramadan working-hours provision", AsOf: factsAsOf,
	},
	{
		Topic: "Times",
		Text: "Oman is UTC+04 all year with no DST, and Muscat sits at about 23.6°N, " +
			"58.6°E. Every call time on a call sheet is local wall clock; a time written " +
			"as a UTC instant is wrong by a constant four hours, which is worse than a " +
			"variable error because it looks deliberate.",
		Source: "IANA zone Asia/Muscat", AsOf: factsAsOf,
	},
}

// arabicDeliveryFacts are the numbers an Arabic deliverable is QC'd against.
//
// This is the one place where being bilingual is a commercial advantage rather
// than a UI problem: for a filmmaker in Oman, Arabic is a DELIVERABLE with a
// spec sheet, and getting it wrong is a bounce.
var arabicDeliveryFacts = []domainFact{
	{
		Topic: "Arabic timed text",
		Text: "42 characters per line, maximum two lines. Reading speed 20 CPS adult / " +
			"17 CPS children; SDH 23 / 20. Modern Standard Arabic only — no dialects. NO " +
			"ITALICS in Arabic. Numbers 1–10 spelled in words except in times and dates; " +
			"11 and above numeric. Ordinals 1–9 in words, 10+ numeric with kashida " +
			"(الـ15). Four digits and more grouped with a comma, years unseparated. A " +
			"single ellipsis character U+2026. No space before a comma, question mark or " +
			"exclamation. Bottom-heavy pyramid line breaks. Kashida U+0640 before a " +
			"bracket following the definite article.",
		Source: "regional broadcaster Arabic subtitling style guides", AsOf: factsAsOf,
	},
	{
		Topic: "Numerals on this product",
		Text: "This app substitutes Arabic-Indic numerals (٠١٢٣) based on a card's text " +
			"direction. When you author Arabic subtitle or deliverable text whose spec " +
			"calls for Western digits, PIN the direction with set_direction deliberately " +
			"rather than letting auto-detection choose — a silently substituted numeral " +
			"is a spec violation nobody sees until QC.",
		Source: "this product's own rendering rule", AsOf: factsAsOf,
	},
}

// ---- register: what the trade calls things ---------------------------------
//
// The sharpest edge of the bilingual promise, and the place a tourist is
// exposed fastest. An agent writing "Filming" where the trade says "Principal
// Photography" has told a 1st AD everything they need to know about it in two
// words. And in this trade the ar/en mixing on one board is not sloppiness, it
// is the REGISTER: Arabic prose, English artefact names, Arabic character and
// location names, English technical specs.

// termPair is one production term and how it is actually said on a Gulf set.
type termPair struct {
	Generic string // the word a non-practitioner reaches for
	Trade   string // what the trade says
	Arabic  string // the working Arabic form, or "" where the trade uses English
}

var productionRegister = []termPair{
	{"Filming", "Principal Photography", "التصوير الرئيسي"},
	{"Filming days", "Shooting days", "أيام التصوير"},
	{"Schedule", "Shooting Schedule", "جدول التصوير"},
	{"Script person", "Script Supervisor", "مشرف السيناريو"},
	{"Lighting person", "Gaffer", ""},
	{"Camera assistant", "1st AC / focus puller", ""},
	{"Rough cut", "Assembly / Rough Cut", "المونتاج الأولي"},
	{"Final cut", "Picture Lock", ""},
	{"Colour correction", "Colour Grade", "التصحيح اللوني"},
	{"Audio mix", "Sound Mix / Final Mix", "مزج الصوت"},
	{"Footage", "Dailies / Rushes", ""},
	{"Actors list", "Cast List", "قائمة الممثلين"},
	{"Crew list", "Crew List", "قائمة طاقم العمل"},
	{"Daily plan", "Call Sheet", ""},
	{"Location visit", "Recce / Tech Scout", "معاينة الموقع"},
	{"Scene breakdown", "Breakdown", "تفريغ السيناريو"},
}

// registerBlock states the vocabulary rule and the terms it applies to.
func registerBlock() string {
	var b strings.Builder
	b.WriteString("REGISTER — say what the trade says. Writing \"Filming\" where the trade " +
		"says \"Principal Photography\" reads as somebody who has never been on a set:\n")
	for _, t := range productionRegister {
		if t.Arabic != "" {
			fmt.Fprintf(&b, "  %s → %s (ar: %s)\n", t.Generic, t.Trade, t.Arabic)
			continue
		}
		fmt.Fprintf(&b, "  %s → %s (used in English on Gulf sets — do NOT invent an Arabic form)\n",
			t.Generic, t.Trade)
	}
	b.WriteString("BILINGUAL RULE: prose follows the person's language; ARTEFACT NAMES and " +
		"TECHNICAL SPECS keep their working form. \"call sheet\", \"DOOD\", \"stripboard\", " +
		"\"ProRes 422 HQ\", \"−24 LKFS\" have no agreed Arabic and are said in English on " +
		"an Omani set — translating them produces something no crew member recognises. " +
		"Equally, do not translate Arabic character or location names into English when " +
		"rewriting a board: that destroys the writer's voice.\n")
	return b.String()
}

// ---- the digest block ------------------------------------------------------

// ---- whose board is this ---------------------------------------------------

// boardRole is one reading of what a production board is FOR.
//
// The prompt's registers say what to do — organise, author, report — and never
// who for, and in a craft with departments that is the missing dimension. The
// same 64 scenes are act structure to the writer, a stripboard to the 1st AD, a
// shot list to the DP, a breakdown to the department heads, a cost report to
// the producer and a delivery list to post: different containers, different
// titles, different granularity, ALL CORRECT. An agent that does not know which
// one it is looking at will restructure a 1st AD's schedule into three acts and
// destroy it, believing it improved the board.
type boardRole struct {
	Name string
	// Marks are the words that say this is that board. Measured rather than
	// asked, in the same spirit as the HOUSE STYLE block: the board's own
	// content is better evidence than a question, and it costs no turn.
	Marks []string
	// Wants names the artefacts this reader actually works in, so the same
	// request resolves differently on the AD's board and the writer's.
	Wants []string
}

var boardRoles = []boardRole{
	{"the writer's script", []string{"int.", "ext.", "slugline", "act i", "act ii", "beat",
		"screenplay", "logline", "treatment", "مشهد", "سيناريو"},
		[]string{"scene-list", "lookbook"}},
	{"the 1st AD's schedule", []string{"call sheet", "call time", "stripboard", "day out of days",
		"dood", "shooting schedule", "company move", "turnaround", "unit base", "جدول التصوير"},
		[]string{"schedule", "stripboard", "dood", "call-sheet", "shoot-day"}},
	{"the DP's shot plan", []string{"shot list", "lens", "mm ", "handheld", "dolly", "steadicam",
		"ecu", "mcu", "wide shot", "لقطة"},
		[]string{"shot-list", "recce"}},
	{"the producer's budget", []string{"budget", "omr", "rial", "contingency", "fringes",
		"above the line", "below the line", "ريال"},
		[]string{"budget", "clearances", "festivals"}},
	{"the post pipeline", []string{"picture lock", "conform", "colour grade", "color grade",
		"sound mix", "adr", "foley", "m&e", "deliverables", "dcp", "qc", "مونتاج"},
		[]string{"post", "deliverables", "media-log"}},
	{"the casting board", []string{"casting", "callback", "self-tape", "avail", "pinned",
		"deal memo", "ممثل"},
		[]string{"casting", "crew"}},
}

// roleOf reads the board for whose it is. Returns "" when the evidence is thin
// or split, because a confident wrong answer here is worse than none: it would
// silently pick one department's shape for another department's work.
func roleOf(s *BoardScope) (boardRole, bool) {
	if s == nil {
		return boardRole{}, false
	}
	var b strings.Builder
	if s.Board != nil {
		if t, _ := s.Board.Content["title"].(string); t != "" {
			b.WriteString(t + "\n")
		}
	}
	for _, it := range s.Items {
		b.WriteString(it.Text + "\n")
	}
	for _, c := range s.ExistingColumns {
		b.WriteString(c + "\n")
	}
	low := strings.ToLower(b.String())

	best, bestScore, runnerUp := boardRole{}, 0, 0
	for _, role := range boardRoles {
		score := 0
		for _, m := range role.Marks {
			if strings.Contains(low, m) {
				score++
			}
		}
		switch {
		case score > bestScore:
			best, runnerUp, bestScore = role, bestScore, score
		case score > runnerUp:
			runnerUp = score
		}
	}
	// Two marks to be evidence at all, and a clear win over the second reading —
	// a board carrying both sluglines and lens notes genuinely IS two boards,
	// and the honest move there is to say nothing and let the request decide.
	if bestScore < 2 || bestScore == runnerUp {
		return boardRole{}, false
	}
	return best, true
}

// roleLine states the reading, and states that it is a reading.
func roleLine(s *BoardScope) string {
	role, ok := roleOf(s)
	if !ok {
		return ""
	}
	return fmt.Sprintf("WHOSE BOARD THIS READS AS: %s — so the artefacts it works in are %s. "+
		"That is inferred from the words on it, not something anybody told me: if a request "+
		"clearly belongs to a different department, follow the request and say which reading "+
		"you took.\n", role.Name, strings.Join(role.Wants, ", "))
}

// domainBlock is the DOMAIN section of the digest, or "" on a board that is not
// production work.
//
// Conditional inclusion is the whole design: this is a substantial spend of
// context and it is free for everybody whose board is not about film. The
// artefact SPECS are not inlined — the menu is, and film_spec fetches the one
// the run is actually building, which is the same read_table shape the digest
// already uses for anything too big to print.
func (s *BoardScope) domainBlock() string {
	if !looksLikeProduction(s) {
		return ""
	}
	// The UNIT roll-up rides here and nowhere else, for the same reason the rest
	// of the pack does: on a board that is not production work a list of "roles
	// with nobody against them" is noise, and on one that is, it is the answer to
	// the question a second AD asks every day.
	return renderDomainBlock() + roleLine(s) + unitBlock(s)
}

// renderDomainBlock is the block itself, separated from the trigger so a probe
// can assert the content without constructing a film-shaped board and so the
// planner can reach it from an intent rather than from a board.
func renderDomainBlock() string {
	var b strings.Builder
	b.WriteString("\nPRODUCTION DOMAIN — this board's own words say film, so here is what the " +
		"trade's artefacts actually ARE. Each one is a document a practitioner " +
		"recognises on sight and rejects if malformed: naming one and producing a " +
		"sticky note that says its name is the failure to avoid.\n")
	fmt.Fprintf(&b, "ARTEFACTS — call film_spec(artefact) for the columns, required fields and "+
		"shape of any of these BEFORE you build it: %s\n", strings.Join(artefactKeys(), ", "))
	b.WriteString("SHAPE BEATS TASTE. A container title is a bucket name. A scene, a shot, a " +
		"budget line, a strip and a deliverable are ROWS, not buckets — and some of " +
		"these documents have a shape the trade decided (a schedule has one column per " +
		"shooting day, a DOOD one per day, a stripboard one strip per scene), which " +
		"wins over the three-to-six-containers guidance.\n")
	b.WriteString("IDENTIFIERS LEAD. Scene and shot numbers are LOCKED at the shooting script " +
		"and never renumbered — inserted scenes get letters (A-scenes) — because " +
		"breakdown, stripboard, DOOD, call sheet, lined script, camera and sound " +
		"reports and editorial all key off them. Keep the number at the front of the " +
		"title. Same for budget account codes and invoice numbers.\n")
	fmt.Fprintf(&b, "COLOUR IS A CODE HERE, not decoration — a producer reads the night-exterior "+
		"load off a board across a room without reading a word. Stripboard: %s.\n",
		strings.Join(stripColours, ", "))
	fmt.Fprintf(&b, "REVISIONS run on a colour wheel, in this order: %s. This trade burned nine "+
		"colours of paper on versioning because being one revision behind is how a scene "+
		"gets shot wrong — so when you revise a schedule, a script or a call sheet, give "+
		"it the next colour and put it in the title as \"Rev: Blue — 12 Aug\". \"Is this "+
		"the current one?\" is the question a board holding a schedule has to answer, and "+
		"duplicate and clone_here answer a different one.\n",
		strings.Join(revisionWheel, " → "))
	fmt.Fprintf(&b, "BREAKDOWN CATEGORIES — the twenty standard ones, a fixed vocabulary rather "+
		"than a set you invent, with their traditional marking colours: %s\n",
		breakdownVocabulary())
	b.WriteString("PAGE EIGHTHS are the unit of work: a script page is eight eighths, a strip " +
		"carries its own count, a feature day is typically 2–5 pages. \"We shot 4 2/8 " +
		"today\" is the daily report's headline number. Do NOT do this arithmetic in " +
		"your head — check_constraints sums eighths for you.\n")
	b.WriteString(registerBlock())
	b.WriteString("\nOMAN — cited rules, not your opinion. State the source when you rely on one, " +
		"so the person can check it and so it visibly goes stale:\n")
	for _, f := range omanFacts {
		fmt.Fprintf(&b, "  %s: %s [%s, as of %s]\n", f.Topic, f.Text, f.Source, f.AsOf)
	}
	b.WriteString("ARABIC AS A DELIVERABLE — hard numbers, not presentation:\n")
	for _, f := range arabicDeliveryFacts {
		fmt.Fprintf(&b, "  %s: %s [%s, as of %s]\n", f.Topic, f.Text, f.Source, f.AsOf)
	}
	b.WriteString("PEOPLE ON A PRODUCTION ARE MOSTLY NOT USERS of this app — cast carry a " +
		"character name and a cast ID (the join key on the call sheet), crew carry a " +
		"department and a role, and vendors, location owners and drivers carry neither. " +
		"set_assignee only reaches real collaborators, so write the person's name INTO " +
		"the card or the table row and say plainly which items have no owner. Never " +
		"guess a collaborator for a name that is not on the PEOPLE list.\n")
	return b.String()
}

// DomainPackText is the pack as one string, for a grader or a probe that needs
// to assert the agent was actually told something.
//
// Exported for the same reason MaxScopeElements is: a harness in another package
// asserting against a number or a phrase it hard-codes is a harness that drifts
// away from the thing it measures.
func DomainPackText() string { return renderDomainBlock() }

// DomainArtefactKeys is the artefact menu, exported so a seeder or a grader can
// enumerate the documents the agent claims to know.
func DomainArtefactKeys() []string { return artefactKeys() }

// DomainArtefactSpec renders one artefact's spec by key, or "" for an unknown
// one — the same text the model gets from film_spec.
func DomainArtefactSpec(key string) string {
	a, ok := artefactFor(key)
	if !ok {
		return ""
	}
	return a.Render()
}

// sortedTopics is a stable list of the fact topics, used by the constraint
// checker to name what it consulted.
func sortedTopics(facts []domainFact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.Topic)
	}
	sort.Strings(out)
	return out
}
