package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Corrections: what a person changed about a plan before letting it run.
//
// The product captures the richest supervision signal any of the surveyed tools
// has — a typed, per-action record of exactly which rows a human dropped,
// renamed, rewrote or refiled — and then threw all of it away at commit. The
// applied plan overwrote the proposed one in the same field, so the diff was not
// merely unread: it was destroyed, and it cannot be backfilled. Every run
// already in the database that was applied with adjustments has lost the
// proposal it was adjusted from, and each further day of use loses more.
//
// Two halves live here. The RECORD (Correction) is written once, at the moment
// both plans are in hand. The PROVENANCE (DropRecord) is what keeps the record
// honest: one click dropping a column that holds nine cards removes ten actions,
// and a naive diff would write ten correction records — nine of which the person
// never made — which is exactly how a rule-miner infers a durable preference
// from a single click.

// DropCause separates a row the person removed from one the harness removed
// along with it.
type DropCause string

const (
	// DropExplicit is a row the person actually clicked away.
	DropExplicit DropCause = "explicit"
	// DropCascade is a row that could not survive its parent's removal. It is a
	// consequence of a decision, never a decision.
	DropCascade DropCause = "cascade"
)

// DropRecord is one action that did not survive the review, and why.
type DropRecord struct {
	Seq   int
	Kind  ActionKind
	Cause DropCause
	// ParentSeq is the explicit drop that killed this one, for a cascade.
	ParentSeq int
}

// CorrectionKind is what the person did to the row.
type CorrectionKind string

const (
	CorrectDrop     CorrectionKind = "drop"
	CorrectRetitle  CorrectionKind = "retitle"
	CorrectRetext   CorrectionKind = "retext"
	CorrectReparent CorrectionKind = "reparent"
	// CorrectRevert is a per-action undo AFTER the apply — the same statement
	// made later and with more information, so it weighs more, not less.
	CorrectRevert CorrectionKind = "revert"
)

// CorrectionOutcome is what happened to the plan the correction was made on.
// It is the label's weight: a person who dropped four rows and then gave up
// entirely said something stronger than one who dropped four rows and applied
// the rest.
type CorrectionOutcome string

const (
	OutcomeApplied   CorrectionOutcome = "applied"
	OutcomeRefined   CorrectionOutcome = "refined"
	OutcomeAbandoned CorrectionOutcome = "abandoned"
	OutcomeReverted  CorrectionOutcome = "reverted"
)

// Correction is one supervision label, derived from a human's typed edit.
type Correction struct {
	Kind CorrectionKind `bson:"kind" json:"kind"`
	Seq  int            `bson:"seq"  json:"seq"`
	// ActionKind is which capability was corrected — the segmentation key that
	// answers "which verb is worst", which is the question the whole record
	// exists to make answerable.
	ActionKind ActionKind `bson:"actionKind" json:"actionKind"`
	// Children is how many further rows went with this one. Zero for a leaf; a
	// container drop carries its real blast radius here instead of spawning that
	// many phantom corrections.
	Children int `bson:"children,omitempty" json:"children,omitempty"`
	// Target is the normalized thing the action was about — a lowercased title,
	// or the head of a note's body. It is what makes two corrections on
	// different runs recognisably the same correction.
	Target string `bson:"target,omitempty" json:"target,omitempty"`
	// ElementID is the existing element an edit was aimed at, empty for creates.
	ElementID string `bson:"elementId,omitempty" json:"elementId,omitempty"`
	// Value is what the person put in its place: the new title, the new body,
	// the destination id.
	Value   string            `bson:"value,omitempty" json:"value,omitempty"`
	Outcome CorrectionOutcome `bson:"outcome"         json:"outcome"`
	At      time.Time         `bson:"at"              json:"at"`
	// RunID is stamped when the record is read back out of a run for
	// generalizing. It is not stored on the row — the run it sits on already
	// says which run it is — but a rule compiled from a pile of corrections has
	// to be able to name its evidence.
	RunID string `bson:"-" json:"-"`
}

// StampRun labels a run's corrections with their origin, for the generalizer.
func StampRun(runID string, in []Correction) []Correction {
	out := make([]Correction, 0, len(in))
	for _, c := range in {
		c.RunID = runID
		out = append(out, c)
	}
	return out
}

// ApplyAdjustments folds the human's typed edits into a plan and returns the
// effective plan, resequenced. See ApplyAdjustmentsDetailed — this is the shape
// callers that do not care about provenance keep using.
func ApplyAdjustments(p *Plan, adjustments []Adjustment, scope *BoardScope) *Plan {
	out, _ := ApplyAdjustmentsDetailed(p, adjustments, scope)
	return out
}

// DiffCorrections turns one review into supervision labels.
//
// The rule that makes the output honest: only an EXPLICIT drop becomes a record.
// A cascade is folded into its parent as a child count, because "they removed
// the Ideas column" and "they removed the Ideas column and the four cards it was
// going to hold" are the same decision, and counting them as five would make the
// most common correction shape in the product carry the largest amplification
// error — invisibly, because the two kinds of drop used to be the same boolean.
func DiffCorrections(proposed *Plan, adjustments []Adjustment, drops []DropRecord,
	outcome CorrectionOutcome, now time.Time) []Correction {
	if proposed == nil || len(proposed.Actions) == 0 {
		return nil
	}
	cascadeCount := map[int]int{}
	explicit := map[int]DropRecord{}
	for _, d := range drops {
		switch d.Cause {
		case DropExplicit:
			explicit[d.Seq] = d
		case DropCascade:
			cascadeCount[d.ParentSeq]++
		}
	}

	var out []Correction
	seen := map[int]bool{}
	for _, adj := range adjustments {
		if adj.Seq < 0 || adj.Seq >= len(proposed.Actions) || seen[adj.Seq] {
			continue
		}
		a := proposed.Actions[adj.Seq]
		c := Correction{
			Seq: adj.Seq, ActionKind: a.Kind, Outcome: outcome, At: now,
			Target: CorrectionTarget(a), Value: adj.Value,
		}
		switch adj.Kind {
		case AdjustDrop:
			if _, ok := explicit[adj.Seq]; !ok {
				// The client asked to drop a row the fold did not drop — out of
				// range, or already gone. Nothing happened, so nothing is a label.
				continue
			}
			c.Kind = CorrectDrop
			c.Children = cascadeCount[adj.Seq]
			c.Value = ""
		case AdjustRetitle:
			c.Kind = CorrectRetitle
		case AdjustRetext:
			c.Kind = CorrectRetext
		case AdjustReparent:
			c.Kind = CorrectReparent
		default:
			continue
		}
		if !a.Kind.Creates() {
			c.ElementID = a.ElementID
		}
		seen[adj.Seq] = true
		out = append(out, c)
	}
	return out
}

// CorrectionTarget normalizes what an action was about, so the same correction
// made on two different runs is recognisably the same correction.
//
// Titles first, then the head of a body: those are the two ways an action names
// its subject, and an id is useless for generalizing because a create's id is
// derived from the run that proposed it.
func CorrectionTarget(a Action) string {
	for _, s := range []string{a.Title, a.Text} {
		if key := normalizeKey(s); key != "" {
			return key
		}
	}
	return ""
}

// normalizeKey lowercases, collapses whitespace and strips punctuation, so
// "Ideas!" and "ideas" are the same key.
func normalizeKey(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return truncate(b.String(), 60)
}

// intentStopwords are the words that carry no request. Kept small and English
// plus the handful of Arabic function words this product's users actually type,
// because the key only has to make two phrasings of the same ask collide — an
// aggressive stemmer would make two DIFFERENT asks collide, which is the error
// that matters here.
var intentStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true, "these": true,
	"my": true, "our": true, "please": true, "can": true, "could": true, "you": true,
	"for": true, "me": true, "to": true, "of": true, "on": true, "in": true,
	"and": true, "with": true, "it": true, "all": true, "up": true, "is": true,
	"في": true, "من": true, "على": true, "هذا": true, "هذه": true, "الى": true, "إلى": true,
}

// IntentKey is the content-free fingerprint two re-asks share.
//
// Sorted rather than sequential: "tidy the board" and "board, tidy it" are the
// same request, and a person re-asking almost never re-types the same word
// order. Returns "" for an intent too short to cluster on, because a
// one-content-word key would group every "organise" ever typed.
func IntentKey(intent string) string {
	words := strings.Fields(normalizeKeyLong(intent))
	kept := make([]string, 0, len(words))
	seen := map[string]bool{}
	for _, w := range words {
		if intentStopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		kept = append(kept, w)
	}
	if len(kept) < 2 {
		return ""
	}
	sortStrings(kept)
	sum := sha256.Sum256([]byte(strings.Join(kept, " ")))
	return hex.EncodeToString(sum[:8])
}

// normalizeKeyLong is normalizeKey without the 60-character cap, for text that
// is a whole request rather than one element's title.
func normalizeKeyLong(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r > 127:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// FreezeProposal stores the plan the person is about to be shown, once.
//
// Called at the PROPOSED transition and never again: the whole point is that
// this field does NOT move when commit rewrites Plan with the effective one, so
// the pair is a diff rather than two views of the same list. Idempotent, because
// PROPOSED is reachable more than once — the refine edge and the apply-retry
// edge both return here, and a re-freeze on the second visit would silently
// adopt the corrected plan as the thing that was originally proposed.
func (r *Run) FreezeProposal() {
	if r == nil || r.Plan == nil || r.ProposedPlan != nil {
		return
	}
	frozen := *r.Plan
	frozen.Actions = append([]Action(nil), r.Plan.Actions...)
	r.ProposedPlan = &frozen
}

// RecordCorrections appends this review's labels to the run.
//
// Written where both halves are in hand, which is the only place in the system
// the diff is computable without a join. Appends rather than replaces: a refine
// followed by an apply is two reviews of two plans, and the first one's
// corrections are the more interesting of the two.
func (r *Run) RecordCorrections(adjustments []Adjustment, drops []DropRecord,
	outcome CorrectionOutcome, now time.Time) {
	if r == nil {
		return
	}
	base := r.ProposedPlan
	if base == nil {
		base = r.Plan
	}
	r.Corrections = append(r.Corrections, DiffCorrections(base, adjustments, drops, outcome, now)...)
}
