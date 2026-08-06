// Package agent is the AI harness: the runtime substrate that compiles context
// from the element graph, drives a tool-use loop, mediates every proposed
// action through policy, persists an ordered journal, and decides completion
// from re-read environment state rather than from the model's claim.
//
// The one architectural rule that shapes everything here: the agent gets NO new
// write path. Every side effect it produces is an ordinary domain.Transaction
// applied through service.TransactionService under an attenuated Delegation. It
// therefore inherits — for free — durability, ordering, the IDOR scope guard,
// realtime broadcast, Ctrl+Z, and revert.
//
// See AI_AGENT_ARCHITECTURE.md for the full design and its conformance mapping.
package agent

import (
	"math"
	"time"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
)

// Delegation is the attenuated grant a run acts under. It lives in domain
// because domain.Principal carries it and the write path enforces it; this
// alias keeps agent-package code readable.
type Delegation = domain.Delegation

// Usage is model spend for a run. It is the cognition plane's type: spend is
// measured where the calls are made, and the run merely accumulates it.
type Usage = cognition.Usage

// ---------------------------------------------------------------------------
// Run state machine
// ---------------------------------------------------------------------------

// RunState is the authoritative controller state. Only the reducer changes it;
// terminal states are immutable except for administrative annotation.
type RunState string

const (
	StateCreated   RunState = "CREATED"
	StatePlanning  RunState = "PLANNING"
	StateRunning   RunState = "RUNNING"
	StateProposed  RunState = "PROPOSED" // awaiting the human's Apply/Discard
	StateApplying  RunState = "APPLYING"
	StateVerifying RunState = "VERIFYING"

	// Terminal.
	StateCompleted  RunState = "COMPLETED"
	StatePartial    RunState = "PARTIAL"
	StateDiscarded  RunState = "DISCARDED"
	StateCancelled  RunState = "CANCELLED"
	StateFailed     RunState = "FAILED"
	StateDenied     RunState = "DENIED"
	StateExhausted  RunState = "BUDGET_EXHAUSTED"
	StateQuarantine RunState = "SECURITY_QUARANTINED"
	StateReverted   RunState = "REVERTED"
)

// Terminal reports whether no further transition is possible.
func (s RunState) Terminal() bool {
	switch s {
	case StateCompleted, StatePartial, StateDiscarded, StateCancelled,
		StateFailed, StateDenied, StateExhausted, StateQuarantine, StateReverted:
		return true
	}
	return false
}

// Active reports whether the run holds the board's single-run slot.
func (s RunState) Active() bool { return !s.Terminal() }

// allowedTransitions is the complete edge set. Kept as data so the reducer is
// one table lookup and the machine can be property-tested exhaustively.
var allowedTransitions = map[RunState][]RunState{
	StateCreated:  {StatePlanning, StateCancelled, StateDenied, StateFailed, StateExhausted},
	StatePlanning: {StateRunning, StateCancelled, StateFailed, StateExhausted, StateQuarantine},
	StateRunning:  {StateProposed, StateApplying, StateCancelled, StateFailed, StateExhausted, StateQuarantine, StatePartial},
	// PROPOSED → PLANNING is the REFINE edge. Nobody gets a structural request
	// right first time, because they discover what they wanted by seeing what
	// they did not want. Without this edge the only way to adjust a plan is to
	// discard it and pay for a fresh run that has not been told what was wrong.
	// PROPOSED → DENIED covers the gap between proposing and applying: the
	// person can be removed from the board, or their grant can expire, while a
	// plan sits waiting for a decision. Without the edge, Apply on a revoked
	// board left the run PROPOSED holding the board's only slot on behalf of
	// somebody who is no longer on it.
	//
	// PROPOSED → QUARANTINE is Discard of a plan that board content steered.
	StateProposed: {StateApplying, StatePlanning, StateDiscarded, StateCancelled,
		StateFailed, StateDenied, StateQuarantine},
	// APPLYING → PROPOSED is the retry edge: an apply rejected before it wrote
	// anything (a stale fingerprint, most often) must return the run to the
	// human. Without it the run would wedge mid-apply and hold the board's run
	// slot forever.
	StateApplying:  {StateVerifying, StateProposed, StateFailed, StateCancelled, StatePartial},
	StateVerifying: {StateCompleted, StateFailed, StatePartial},
	StateCompleted: {StateReverted},
	StatePartial:   {StateReverted},
	// FAILED and CANCELLED reach REVERTED because revertability is a question
	// about EFFECTS, not about how the run ended. A run cancelled mid-apply or
	// failed after committing has a real transaction id and a working Revert;
	// without these two edges the board was genuinely restored and the run's
	// label could not follow it, so the ledger kept offering the undo.
	StateFailed:    {StateReverted},
	StateCancelled: {StateReverted},
}

// CanTransition reports whether from → to is a legal edge.
func CanTransition(from, to RunState) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Task contract
// ---------------------------------------------------------------------------

// Autonomy is how much the run may do without a human keystroke.
type Autonomy string

const (
	// AutonomyPreview stops at PROPOSED and writes nothing until the user
	// applies. The default, and the only mode that is safe by construction.
	AutonomyPreview Autonomy = "preview"
	// AutonomyAuto applies immediately. Still one transaction, still
	// revertible — and never offered the delete capability.
	AutonomyAuto Autonomy = "auto"
)

// Valid reports whether a is a known autonomy mode.
func (a Autonomy) Valid() bool { return a == AutonomyPreview || a == AutonomyAuto }

// Scope narrows what the run is pointed at.
type Scope string

const (
	ScopeBoard     Scope = "board"     // every eligible direct child
	ScopeUnsorted  Scope = "unsorted"  // only the Unsorted tray
	ScopeSelection Scope = "selection" // only the ids the user selected
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool {
	return s == ScopeBoard || s == ScopeUnsorted || s == ScopeSelection
}

// TaskSpec is the normative task contract.
type TaskSpec struct {
	// Intent is the person's request, in their own words. It is the whole
	// task: there is no fixed workload, so the same harness organizes a board,
	// drafts a project structure, or does nothing, depending on what was asked.
	Intent string `bson:"intent" json:"intent"`
	// Source is WHO composed that intent — the person, or the product on their
	// behalf.
	//
	// Four producers converge on Create indistinguishably: the composer's typed
	// text, Continue's server-composed continuation, Retry's carry-forward, and
	// the ambient drift pill's canned request. Every rate in every dashboard is
	// meaningless while the denominator mixes them: a hint accepted with one
	// click and a paragraph somebody composed at 2 a.m. have different priors on
	// apply rate, adjustment rate and cost tolerance, so a shown/accepted counter
	// on a hint stops at the click and can never reach the outcome it exists to
	// justify. Distinct from transport (web/api/mcp): this is authorship.
	Source TaskSource `bson:"source,omitempty" json:"source,omitempty"`
	// IntentKey is a content-free fingerprint of the request: lowercased,
	// punctuation-stripped, stopworded, hashed.
	//
	// "Where do people re-ask the same thing" sounds like a pipeline and is a
	// two-index query — successive runs by one tenant on one board within a
	// window whose intents are near-duplicates — and three consecutive
	// near-identical intents is the operational definition of a capability that
	// does not work. It is also the missing trigger for "offer a rule after the
	// third identical run", which was specified with no way to detect the third.
	// A hash rather than the text, so the aggregate is content-free by
	// construction and therefore legal to retain.
	IntentKey string `bson:"intentKey,omitempty" json:"-"`
	// Owner is the tenant whose content the run may search.
	Owner       string `bson:"owner"                  json:"-"`
	RootBoardID string `bson:"rootBoardId"            json:"rootBoardId"`
	// AncestorIDs are the boards this run's root is nested inside, nearest
	// first, resolved at admission.
	//
	// The one-run-per-board guard and the blast radius were measured in
	// different units. Scope is the whole SUBTREE under the root, so a run
	// rooted at A reads and writes everything nested under A — while the guard
	// was `FindOne(task.rootBoardId == A, active)`. A second person starting a
	// run rooted at nested board B passed that check trivially, and both wrote
	// the same elements concurrently. Deep subtrees are now the normal shape of
	// a board, so nesting is exactly where the content lives.
	//
	// Denormalised onto the run rather than walked at query time because the
	// conflict question is asked by the storage layer, which has no resolver:
	// a run conflicts if its root is in my chain, or mine is in its.
	AncestorIDs []string `bson:"ancestorIds,omitempty"  json:"-"`
	Scope       Scope    `bson:"scope"                  json:"scope"`
	SelectionID []string `bson:"selectionIds,omitempty" json:"selectionIds,omitempty"`
	Autonomy    Autonomy `bson:"autonomy"               json:"autonomy"`
	// AutonomyNote discloses a downgrade the BOARD imposed on the request.
	//
	// A bare refusal makes the assistant look broken on every shared board, so
	// a non-owner who asks for an unattended run gets a previewed one plus this
	// sentence — the honest middle between doing what was asked and refusing it.
	AutonomyNote string `bson:"autonomyNote,omitempty" json:"autonomyNote,omitempty"`
	Budget       Budget `bson:"budget"                 json:"budget"`
	// Refinements are the follow-up steers a person gave while reviewing a
	// proposed plan. Kept in order and all replayed, so a second pass sees the
	// whole conversation rather than only the latest correction — "make it four
	// columns" after "group by theme" means both, not just the last one.
	Refinements []string `bson:"refinements,omitempty" json:"refinements,omitempty"`
	// Adjustments are the TYPED edits the person had already made to the plan
	// when they typed that steer — rows dropped, retitled, refiled.
	//
	// They used to be thrown away. Drop three rows, then type "also make it four
	// columns", and the drops vanished: the second plan re-proposed exactly what
	// had just been removed, which reads as the agent ignoring you and is the
	// most annoying possible outcome of a refine. And refinement is the ONLY
	// correction channel that reaches the model, so it was being fed free prose
	// while the precise, non-lossy signal sitting beside it in the same store
	// was deleted.
	//
	// Replayed as facts rather than as prose: "they removed X, do not propose it
	// again" is something a plan can be checked against; a summary of it is not.
	Adjustments []Adjustment `bson:"adjustments,omitempty" json:"adjustments,omitempty"`
	// AttachmentIDs are files the person attached to the REQUEST itself — a
	// brief, a screenshot, a spec. Distinct from look_at, which reads something
	// already on the board: these are part of what was asked, so they ride with
	// the opening message rather than being discovered mid-run.
	AttachmentIDs []string `bson:"attachmentIds,omitempty" json:"attachmentIds,omitempty"`
	// Viewport is the region of the canvas the person was looking at when they
	// asked, in board coordinates.
	//
	// Without it the agent is spatially blind: it knew what was on the board and
	// not where anyone was working, so on a large board new content appeared
	// wherever the packing algorithm decided — often thousands of pixels from
	// the part being worked on, which reads as the agent ignoring the request.
	Viewport *Viewport `bson:"viewport,omitempty" json:"viewport,omitempty"`
	// ActiveLabelIDs is the label filter the person had switched on when they
	// asked — a view they deliberately constructed, and one the run could not
	// see.
	//
	// "Tidy what I'm looking at" while filtering to Blocked reorganised the
	// ninety dimmed cards along with the six lit ones. Viewport was added for
	// exactly this reason and stopped one field short: where the person is
	// looking is spatial AND categorical.
	//
	// A steer, never a gate. A filter is a view, not a permission, and a hard
	// scope here would one day refuse the right answer — so the digest marks the
	// matching items and says what the filter is, and the model decides.
	ActiveLabelIDs []string `bson:"activeLabelIds,omitempty" json:"activeLabelIds,omitempty"`
	// Outline is the sketch this run showed before it started, and OutlineKept is
	// which of its steps the person left ticked.
	//
	// Carried on the task rather than on the plan because it is an INPUT: the
	// steps somebody struck out become a synthesized instruction the authoring
	// turn is held to, in the same shape and for the same reason the typed
	// adjustments are replayed as decisions rather than summarised.
	//
	// SkipOutline is the per-account escape. A person who never edits the sketch
	// is paying a call for a screen they click through, and a pre-phase nobody
	// can turn off is a tax rather than a feature.
	Outline     *Outline `bson:"outline,omitempty"     json:"outline,omitempty"`
	OutlineKept []bool   `bson:"outlineKept,omitempty" json:"outlineKept,omitempty"`
	SkipOutline bool     `bson:"skipOutline,omitempty" json:"skipOutline,omitempty"`
}

// Viewport is what the person can see, in board coordinates.
type Viewport struct {
	X      float64 `bson:"x"      json:"x"`
	Y      float64 `bson:"y"      json:"y"`
	Width  float64 `bson:"width"  json:"width"`
	Height float64 `bson:"height" json:"height"`
}

// Valid reports whether a viewport describes a real region. A client that sends
// nothing, or sends a degenerate box before layout has settled, is treated as
// having sent nothing at all.
func (v *Viewport) Valid() bool {
	return v != nil && v.Width > 200 && v.Height > 200 &&
		math.Abs(v.X) < 1e7 && math.Abs(v.Y) < 1e7
}

// Center is the middle of the visible region.
func (v *Viewport) Center() (x, y float64) {
	return v.X + v.Width/2, v.Y + v.Height/2
}

// Budget bounds the run. These are the limits that actually bind a tool loop.
type Budget struct {
	// MaxSteps caps model turns — the termination guarantee.
	MaxSteps int `bson:"maxSteps" json:"maxSteps"`
	// MaxActions caps the size of one plan, so a runaway proposal cannot
	// rewrite an entire board in a single transaction.
	MaxActions int           `bson:"maxActions" json:"maxActions"`
	MaxTokens  int           `bson:"maxTokens"  json:"maxTokens"`
	MaxCostUSD float64       `bson:"maxCostUsd" json:"maxCostUsd"`
	Deadline   time.Duration `bson:"deadline"   json:"deadline"`
}

// DefaultBudget is the shipped envelope.
func DefaultBudget() Budget {
	return Budget{
		// MaxSteps was 14, and 14 turns was the budget that actually bound.
		// A run is allowed 60 changes; the model stages two or three a turn out
		// of politeness, so the real ceiling was under thirty and "make a film"
		// was cut mid-flow with its last column created and left empty. The
		// prompt now says batching is the norm and this says there is room to
		// finish; the cost cap is what bounds spend, and it did not move.
		MaxSteps:   24,
		MaxActions: 60,
		MaxTokens:  8_000,
		MaxCostUSD: 0.35,
		// The deadline must scale with MaxSteps or it becomes the real budget:
		// at 3 minutes, a 24-step run over a nested-scope digest was killed by
		// the clock long before the step budget it was nominally allowed —
		// raising one without the other is a fix that cannot fire. Sized for
		// the worst honest run (~20s a step); the cost cap bounds spend.
		Deadline: 8 * time.Minute,
	}
}

// CallTimeoutFor sizes one HTTP call against the run that contains it.
//
// A single call is allowed three steps' worth of the run's clock: long enough
// that a slow-but-working turn is never cut, short enough that a wedged one
// leaves most of the deadline for the rest of the run. The provider used to
// hold a flat three minutes and retry three times, so one hung call could burn
// nine minutes against an eight-minute deadline — the run died of a provider
// outage and reported it as running out of budget.
func CallTimeoutFor(b Budget) time.Duration {
	steps := b.MaxSteps
	if steps < 1 {
		steps = DefaultBudget().MaxSteps
	}
	d := b.Deadline
	if d <= 0 {
		d = DefaultBudget().Deadline
	}
	return d / time.Duration(steps) * 3
}

// Normalize clamps a caller-supplied budget to the server envelope. A client
// may ask for less, never for more.
func (b *Budget) Normalize() {
	d := DefaultBudget()
	clampInt(&b.MaxSteps, 2, d.MaxSteps)
	clampInt(&b.MaxActions, 1, d.MaxActions)
	clampInt(&b.MaxTokens, 1_000, d.MaxTokens)
	if b.MaxCostUSD <= 0 || b.MaxCostUSD > d.MaxCostUSD {
		b.MaxCostUSD = d.MaxCostUSD
	}
	if b.Deadline <= 0 || b.Deadline > d.Deadline {
		b.Deadline = d.Deadline
	}
}

func clampInt(v *int, lo, hi int) {
	if *v <= 0 || *v > hi {
		*v = hi
	}
	if *v < lo {
		*v = lo
	}
}

// ---------------------------------------------------------------------------
// Human adjustments
// ---------------------------------------------------------------------------

// AdjustmentKind is the closed set of edits a human may make to a plan.
// Anything outside it is rejected — this is what stops a client smuggling
// arbitrary mutations into an agent-authored transaction.
type AdjustmentKind string

const (
	// AdjustDrop removes one action from the plan.
	AdjustDrop AdjustmentKind = "drop"
	// AdjustRetitle renames what an action creates or renames.
	AdjustRetitle AdjustmentKind = "retitle"
	// AdjustRetext rewrites the body of a note action.
	AdjustRetext AdjustmentKind = "retext"
	// AdjustReparent sends an action somewhere else — Value is the destination
	// element id, or the root board.
	//
	// Without it, the right card in the wrong column could only be DROPPED and
	// then filed by hand afterwards. A plan that is ninety percent correct was
	// therefore applied at ninety percent and finished manually every time, and
	// the ten percent the agent got wrong cost more than doing it all yourself.
	AdjustReparent AdjustmentKind = "reparent"
)

// Adjustment is one typed human edit, addressed by the action's sequence.
//
// The bson tags are not decoration. This struct carried json tags only, so it
// had no persistent home anywhere in the schema — it arrived on a request, was
// folded into a plan, and ceased to exist. It now rides on the task (for a
// refine's replay) and is diffed into Corrections at commit, and both of those
// are stored.
type Adjustment struct {
	Kind  AdjustmentKind `bson:"kind"            json:"kind"`
	Seq   int            `bson:"seq"             json:"seq"`
	Value string         `bson:"value,omitempty" json:"value,omitempty"`
}

// TaskSource is who composed the intent.
type TaskSource string

const (
	// SourceTyped is a person writing in the composer.
	SourceTyped TaskSource = "typed"
	// SourceHint is the ambient suggestion pill: one click, server-authored text.
	SourceHint TaskSource = "hint"
	// SourceContinue is a continuation composed from a prior run's unmet list.
	SourceContinue TaskSource = "continue"
	// SourceRetry is the same request asked again after a bad answer.
	SourceRetry TaskSource = "retry"
)

// RuleOutcome is the person's answer to a proposed standing rule.
type RuleOutcome string

const (
	RuleAccepted RuleOutcome = "accepted"
	RuleDeclined RuleOutcome = "declined"
)

// ---------------------------------------------------------------------------
// Verdict
// ---------------------------------------------------------------------------

// CriterionResult is one checked acceptance criterion.
type CriterionResult struct {
	Name   string `bson:"name"             json:"name"`
	Passed bool   `bson:"passed"           json:"passed"`
	Detail string `bson:"detail,omitempty" json:"detail,omitempty"`
	Fatal  bool   `bson:"fatal"            json:"fatal"`
}

// Verdict is the outcome of a verification pass.
type Verdict struct {
	Passed   bool              `bson:"passed"   json:"passed"`
	Criteria []CriterionResult `bson:"criteria" json:"criteria"`
}

// Fail records a failed criterion.
func (v *Verdict) Fail(name, detail string, fatal bool) {
	v.Criteria = append(v.Criteria, CriterionResult{Name: name, Detail: detail, Fatal: fatal})
}

// Pass records a satisfied criterion.
func (v *Verdict) Pass(name string) {
	v.Criteria = append(v.Criteria, CriterionResult{Name: name, Passed: true})
}

// Settle computes Passed from the recorded criteria: any fatal failure sinks
// the verdict.
func (v *Verdict) Settle() {
	v.Passed = true
	for _, c := range v.Criteria {
		if !c.Passed && c.Fatal {
			v.Passed = false
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// Run is the authoritative record of one agent invocation.
type Run struct {
	ID     string `bson:"_id"       json:"id"`
	Tenant string `bson:"tenantSub" json:"-"` // the human sub; ACLs still key on this
	// BoardOwnerSub is who owns the root board, stamped at admission from the
	// ACL the RequireEdit call has already resolved.
	//
	// Tenant is the human who STARTED the run, not the person whose content it
	// read. Purge was strictly tenant-keyed, so a collaborator's run on your
	// board — carrying your cards' titles in Plan.Actions, your words quoted in
	// the intent, your structure in every staged-action summary — was filed
	// under THEIR sub. You deleted your account, the elements were hard-deleted,
	// and a verbatim partial copy of your board lived on indefinitely in
	// somebody else's agent history, readable by them over HTTP for as long as
	// their account existed.
	//
	// One field settles three items: erasure runs on both axes, the owner's
	// audit view has an index to read, and the account-wide run query has a
	// second key.
	BoardOwnerSub string   `bson:"boardOwnerSub,omitempty" json:"-"`
	Task          TaskSpec `bson:"task"      json:"task"`

	State RunState `bson:"state" json:"state"`
	// Active mirrors !State.Terminal() as a stored field, so the storage layer
	// can carry a unique partial index enforcing one live run per board — a
	// rule that must hold under a race, not merely under a read.
	Active bool   `bson:"active"           json:"-"`
	Reason string `bson:"reason,omitempty" json:"reason,omitempty"`
	Rev    int64  `bson:"rev"              json:"rev"`

	Delegation Delegation `bson:"delegation" json:"-"`

	Plan    *Plan    `bson:"plan,omitempty"    json:"plan,omitempty"`
	Verdict *Verdict `bson:"verdict,omitempty" json:"verdict,omitempty"`

	// ProposedPlan is the plan as the person was shown it, frozen at the
	// PROPOSED transition and never rewritten.
	//
	// Plan used to be one field playing two roles: commit overwrote it with the
	// EFFECTIVE plan, so the proposal a person adjusted was destroyed by the act
	// of applying their adjustments. That is not a signal nobody read — it is a
	// signal that cannot be recovered, for every run already in the database.
	// It has a live cost too: RevertedElementIDs and the outcome card's per-row
	// Undo index into a plan that is no longer the one the person reviewed, so
	// the row they dropped is simply absent from their own audit trail.
	//
	// One field, no new collection: both halves are already bson subdocuments
	// and retention rides PurgeTenant unchanged.
	ProposedPlan *Plan `bson:"proposedPlan,omitempty" json:"proposedPlan,omitempty"`

	// Corrections are the typed edits the person made to the proposal, derived
	// where both plans are in hand — the only place the diff is computable
	// without a join.
	Corrections []Correction `bson:"corrections,omitempty" json:"corrections,omitempty"`

	// RuleOutcome records whether the person accepted the rule this run
	// proposed. Empty means the card was never answered.
	//
	// The ProposedRule card is a two-button experiment the product runs on every
	// corrected run and never scored: Save appended a sentence to the board's
	// rules through an ordinary transaction with no run link, and "Not now" set
	// a local flag and vanished. So "does the agent propose rules people
	// actually want" — the direct measure of whether the whole memory wave is
	// worth building — was unanswerable, and an accepted rule entered the rules
	// string indistinguishable from something the owner typed. The moment the
	// provenance is knowable was the moment it was discarded.
	RuleOutcome RuleOutcome `bson:"ruleOutcome,omitempty" json:"ruleOutcome,omitempty"`

	// TransactionIDs are the board transactions this run committed, in order.
	// Revert replays their inverses. Usually exactly one.
	TransactionIDs []string `bson:"transactionIds,omitempty" json:"transactionIds,omitempty"`

	// Steers are corrections a person typed while the run was still working,
	// not yet delivered to the model.
	//
	// Watching a plan form and being able to do nothing about it but Stop was
	// the worst part of the wait: by the third staged change you can usually
	// tell it has misread the request, and killing the run threw away the
	// tokens along with the work. The loop is already multi-turn, so a steer is
	// a queue and one check between steps rather than a new architecture.
	Steers []string `bson:"steers,omitempty" json:"steers,omitempty"`

	// ProposalExpiresAt is when an unanswered PROPOSED run stops holding this
	// board's single-run slot.
	//
	// A proposal is deliberately durable — it survives a server restart, because
	// a person who previewed a plan and came back tomorrow should still find it.
	// Durable was implemented as eternal, and PROPOSED is non-terminal, so one
	// preview that nobody answered locked the board's agent for everyone,
	// permanently, with no way to see or clear it from anywhere in the product.
	//
	// It also outlived its own authority: the delegation expires after 30
	// minutes, so a proposal older than that could not have been applied even
	// if someone had found it.
	ProposalExpiresAt *time.Time `bson:"proposalExpiresAt,omitempty" json:"proposalExpiresAt,omitempty"`

	// RevertedElementIDs are elements this run touched that have since been
	// individually undone. A whole-run revert moves the run to REVERTED and
	// does not use this; a partial one leaves the run COMPLETED and records
	// here what is no longer standing, so the review list can grey those rows
	// and the button cannot be pressed twice.
	RevertedElementIDs []string `bson:"revertedElementIds,omitempty" json:"revertedElementIds,omitempty"`
	// StaleElementIDs are the elements that changed under the plan while it was
	// being reviewed.
	//
	// Persisted rather than only broadcast on the event stream: the recovery
	// surface used to have to mine the journal for an error event to find out
	// WHAT had moved, so a reload — or simply opening the run later — turned a
	// specific, fixable problem back into a toast naming nothing.
	StaleElementIDs []string `bson:"staleElementIds,omitempty" json:"staleElementIds,omitempty"`

	// RevertBlockedReason is why this run's inverse can never be replayed —
	// JN8's honesty clause, reached through JN20's trigger.
	//
	// Emptying the trash hard-deletes elements a past run created, and every
	// inverse op opens with a fetch of the thing it is undoing. So the run stays
	// COMPLETED, the ledger keeps drawing an Undo button, and the button 500s —
	// forever, identically, with an error naming nothing. A guarantee that fails
	// the same way every time is worse than one that was never offered, because
	// the person keeps spending trust on it.
	//
	// Set only once a revert has proved it, and only when NOTHING was
	// recoverable; a partially-blocked revert still runs and reports its number.
	// The ledger renders this string where the button was.
	RevertBlockedReason string `bson:"revertBlockedReason,omitempty" json:"revertBlockedReason,omitempty"`

	// RevertedAt is when each of those elements was undone, keyed by element id.
	//
	// A partial revert used to stamp only UpdatedAt, so eight per-action undos
	// spread over an hour collapsed into one timestamp and "how long did the
	// person live with this change before taking it back" was unanswerable for
	// every one of them but the last. Kept beside the id list rather than
	// replacing it, because the client reads that list as a set of ids and the
	// timing is nobody's business but the metrics'.
	RevertedAt map[string]time.Time `bson:"revertedAt,omitempty" json:"-"`

	// ContinuesRunID is the run this one picks up from, set when a person
	// pressed Continue on a run that ended with work left over.
	//
	// A big job could not span runs. The film build was cut at thirty actions
	// and the only way to say "keep going" was to type a fresh, context-free
	// prompt — which is how the follow-up ended up building a second copy of the
	// structure beside the first. The link is the audit trail: two runs, one job,
	// and it is possible to see afterwards that they were the same job.
	ContinuesRunID string `bson:"continuesRunId,omitempty" json:"continuesRunId,omitempty"`

	// RetryOfRunID is the run this one was asked to do over.
	//
	// Retry is the purest "you got it wrong, do it again" the product has, and
	// it wrote a row that looked like a first attempt: no link, and an intent
	// mutated by concatenating the prior run's refinements onto it. So the retry
	// rate — the cleanest per-capability failure rate obtainable, and the only
	// one needing no interpretation — read as zero, and the concatenation
	// defeated even the fallback of clustering by intent similarity, because a
	// retry's intent was no longer the original request.
	//
	// Same field shape and same sparse-index pattern as ContinuesRunID, which is
	// the sibling this path was copied from and then drifted away from.
	RetryOfRunID string `bson:"retryOfRunId,omitempty" json:"retryOfRunId,omitempty"`

	// ChargedTo is whose daily ledger this run draws on, and BoardCapUSD is the
	// board's own ceiling, both resolved at admission from the owner's policy.
	//
	// Carried on the run rather than re-resolved between turns: the budget
	// re-check runs inside the model loop, where an ACL read per step would be
	// a database round trip per step, and where a policy edited mid-run must
	// not silently move the line the run is already being measured against.
	// RequestID and Source are the provenance of the request that started this
	// run, carried onto every transaction it commits.
	//
	// Without them a change can originate from a source the board's own history
	// cannot name — which is tolerable while the only client is a browser and
	// indefensible the moment a webhook, a chat command or an external agent can
	// write. Stamped at admission because that is the only moment the transport
	// is known: a run commits from a goroutine, minutes later, with no request
	// in scope.
	RequestID string `bson:"requestId,omitempty" json:"requestId,omitempty"`
	Source    string `bson:"source,omitempty"    json:"source,omitempty"`

	ChargedTo   string  `bson:"chargedTo,omitempty"   json:"-"`
	BoardCapUSD float64 `bson:"boardCapUsd,omitempty" json:"-"`

	Usage Usage `bson:"usage" json:"usage"`

	// Build identifies the code that produced this run.
	//
	// Without it an apply-rate that moves after a deploy cannot be attributed to
	// anything: the prompt is Go string literals, the budgets are package
	// constants, and none of them left a trace on the row. Every aggregate over
	// runs is therefore uninterpretable across a release boundary — which is how
	// the eval suite's pass rate came to be a number somebody typed into a
	// markdown file. Four hashes, computed once at boot.
	Build BuildStamp `bson:"build,omitempty" json:"build,omitempty"`

	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	// CompletedAt is the FIRST terminal stamp. COMPLETED→REVERTED is a legal
	// edge, so this field alone used to answer "when was it applied" with the
	// revert time — losing both ends of the interval every metric about regret
	// is measured over. StateAt keeps them apart.
	CompletedAt *time.Time `bson:"completedAt,omitempty" json:"completedAt,omitempty"`

	// StateAt records when the run first entered each state.
	//
	// Written in transition, the one place State changes, so it cannot drift
	// from the state machine. Time-to-decide is StateAt[APPLYING] −
	// StateAt[PROPOSED]; model latency is StateAt[PROPOSED] − CreatedAt; regret
	// is StateAt[REVERTED] − StateAt[COMPLETED]. All arithmetic over fields, no
	// new collection and no new event.
	StateAt map[RunState]time.Time `bson:"stateAt,omitempty" json:"stateAt,omitempty"`
}

// BuildStamp is the experiment identity of one run: which code, which prompt,
// which tools, which budgets. Hashes rather than contents — the point is to
// group runs that were produced by the same thing, not to reconstruct it.
type BuildStamp struct {
	GitSHA        string `bson:"gitSha,omitempty"        json:"gitSha,omitempty"`
	PromptHash    string `bson:"promptHash,omitempty"    json:"promptHash,omitempty"`
	CatalogueHash string `bson:"catalogueHash,omitempty" json:"catalogueHash,omitempty"`
	BudgetsHash   string `bson:"budgetsHash,omitempty"   json:"budgetsHash,omitempty"`
}

// ---------------------------------------------------------------------------
// Event journal
// ---------------------------------------------------------------------------

// EventType enumerates the journal's vocabulary. Security events are named
// separately so alerting can key on the prefix.
type EventType string

const (
	// EvRunCreated means admission and nothing else. It used to be emitted by
	// Refine and Steer as well, with an untranslated English Message as the
	// only discriminator — so `count(type = run.created)` was runs plus
	// refinements plus steers, and the first histogram anyone built over the
	// journal would have reported all three rates wrong. The vocabulary is the
	// expensive thing to change once a consumer exists, so it is being made
	// unambiguous before one does.
	EvRunCreated EventType = "run.created"
	// EvRefined is a person asking for a revision of a plan they have seen.
	EvRefined EventType = "run.refined"
	// EvSteered is a correction handed to a run that is still working.
	EvSteered EventType = "run.steered"
	// EvSteerDelivered is the turn where a queued correction actually reached
	// the model.
	//
	// The person had evidence of neither half before: they typed "no, not the
	// archive", watched the identical spinner, typed it again, and the third
	// repetition hit maxSteersPerRun and was refused with a message that reads
	// as a telling-off. Queued and Heard are different states and only the
	// server knows when one becomes the other.
	EvSteerDelivered EventType = "run.steer.delivered"
	EvRunState       EventType = "run.state"
	EvStepFinished   EventType = "step.finished"
	// EvOutlineReady carries the typed sketch of what the run is about to do,
	// emitted before any of it is paid for.
	//
	// The first chance to redirect a run used to be the refine edge — after the
	// whole thing had been bought. A misread intent therefore cost a full run's
	// tokens and a full minute of somebody's attention before they could say "no,
	// not like that", and Refine then paid for a second full run.
	EvOutlineReady EventType = "plan.outline"
	// EvActionStaged fires as each change is decided, so the client can show a
	// plan writing itself rather than a spinner followed by a wall of text.
	EvActionStaged EventType = "action.staged"
	EvPlanReady    EventType = "plan.ready"
	EvOpsCommitted EventType = "ops.committed"
	EvVerdict      EventType = "verdict"
	EvReverted     EventType = "run.reverted"
	EvError        EventType = "error"

	// Security. EvSecIDOutOfScope is the primary detector for a successful
	// prompt injection: the model named an element it was never shown.
	EvSecIDOutOfScope EventType = "security.id_out_of_scope"
	EvSecSanitized    EventType = "security.content_sanitized"
	// EvSecQuarantined records that board content steered a run hard enough to
	// hold it for review. Quarantine shipped with bilingual copy, a dedicated UI
	// branch and a terminal state, and left no countable trace of any kind: the
	// only sign was an amber sentence on a review card, so a security halt read
	// exactly like a validation refusal and no alert could key on it.
	EvSecQuarantined EventType = "security.quarantined"
)

// Event is one immutable fact about a run. Sequence is unique per run and gives
// the client a resumable cursor after a reconnect.
type Event struct {
	ID       string    `bson:"_id"      json:"id"`
	RunID    string    `bson:"runId"    json:"runId"`
	Sequence int64     `bson:"sequence" json:"sequence"`
	Type     EventType `bson:"type"     json:"type"`
	// Tenant and RootBoardID are denormalised from the run.
	//
	// Both are in hand at every emit call site, and without them no question
	// wider than one run could be asked of the journal without joining every
	// row back through agent_runs. The timing data was never missing; it was
	// unreadable.
	Tenant      string         `bson:"tenantSub,omitempty"   json:"-"`
	RootBoardID string         `bson:"rootBoardId,omitempty" json:"-"`
	Message     string         `bson:"message,omitempty"     json:"message,omitempty"`
	Data        map[string]any `bson:"data,omitempty"        json:"data,omitempty"`
	At          time.Time      `bson:"at"                    json:"at"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrNotRunning means a steer arrived for a run that is not working.
	ErrNotRunning = wrap(domain.ErrConflict, "this run is not working right now")
	// ErrRunActive means the board already holds a non-terminal run.
	ErrRunActive = wrap(domain.ErrConflict, "a run is already active on this board")
	// ErrStalePlan means a targeted element changed since the plan was made.
	ErrStalePlan = wrap(domain.ErrConflict, "the board changed while you were reviewing")
	// ErrNotProposed means apply/discard was called in the wrong state.
	ErrNotProposed = wrap(domain.ErrConflict, "this run has no plan awaiting a decision")
	// ErrDisabled means the deployment has no model provider configured.
	ErrDisabled = wrap(domain.ErrUnavailable, "the AI agent is not enabled on this server")
	// ErrBudget means a configured budget was exhausted.
	ErrBudget = wrap(domain.ErrValidation, "budget exhausted")
	// ErrNoIntent means the request carried no instruction.
	ErrNoIntent = wrap(domain.ErrValidation, "tell Qomra what you would like it to do")
	// ErrNothingLeft means Continue was called on a run that left nothing over.
	ErrNothingLeft = wrap(domain.ErrValidation, "that run did everything it set out to do")
	// ErrNoSuchVariant means apply named a shape this run never offered.
	ErrNoSuchVariant = wrap(domain.ErrValidation, "that alternative is not one this plan offered")
	// ErrDestructiveNeedsReview means an unattended run proposed a deletion.
	ErrDestructiveNeedsReview = wrap(domain.ErrForbidden, "deletions must be reviewed before they are applied")
)

type agentError struct {
	wrapped error
	msg     string
}

func (e *agentError) Error() string { return e.msg }
func (e *agentError) Unwrap() error { return e.wrapped }

func wrap(base error, msg string) error { return &agentError{wrapped: base, msg: msg} }
