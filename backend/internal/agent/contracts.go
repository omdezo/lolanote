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
	StateProposed: {StateApplying, StatePlanning, StateDiscarded, StateCancelled, StateFailed},
	// APPLYING → PROPOSED is the retry edge: an apply rejected before it wrote
	// anything (a stale fingerprint, most often) must return the run to the
	// human. Without it the run would wedge mid-apply and hold the board's run
	// slot forever.
	StateApplying:  {StateVerifying, StateProposed, StateFailed, StateCancelled, StatePartial},
	StateVerifying: {StateCompleted, StateFailed, StatePartial},
	StateCompleted: {StateReverted},
	StatePartial:   {StateReverted},
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
	// Owner is the tenant whose content the run may search.
	Owner       string   `bson:"owner"                  json:"-"`
	RootBoardID string   `bson:"rootBoardId"            json:"rootBoardId"`
	Scope       Scope    `bson:"scope"                  json:"scope"`
	SelectionID []string `bson:"selectionIds,omitempty" json:"selectionIds,omitempty"`
	Autonomy    Autonomy `bson:"autonomy"               json:"autonomy"`
	Budget      Budget   `bson:"budget"                 json:"budget"`
	// Refinements are the follow-up steers a person gave while reviewing a
	// proposed plan. Kept in order and all replayed, so a second pass sees the
	// whole conversation rather than only the latest correction — "make it four
	// columns" after "group by theme" means both, not just the last one.
	Refinements []string `bson:"refinements,omitempty" json:"refinements,omitempty"`
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
		MaxSteps:   14,
		MaxActions: 60,
		MaxTokens:  8_000,
		MaxCostUSD: 0.35,
		Deadline:   3 * time.Minute,
	}
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
)

// Adjustment is one typed human edit, addressed by the action's sequence.
type Adjustment struct {
	Kind  AdjustmentKind `json:"kind"`
	Seq   int            `json:"seq"`
	Value string         `json:"value,omitempty"`
}

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
	ID     string   `bson:"_id"       json:"id"`
	Tenant string   `bson:"tenantSub" json:"-"` // the human sub; ACLs still key on this
	Task   TaskSpec `bson:"task"      json:"task"`

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

	// TransactionIDs are the board transactions this run committed, in order.
	// Revert replays their inverses. Usually exactly one.
	TransactionIDs []string `bson:"transactionIds,omitempty" json:"transactionIds,omitempty"`

	Usage Usage `bson:"usage" json:"usage"`

	CreatedAt   time.Time  `bson:"createdAt"             json:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt"             json:"updatedAt"`
	CompletedAt *time.Time `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
}

// ---------------------------------------------------------------------------
// Event journal
// ---------------------------------------------------------------------------

// EventType enumerates the journal's vocabulary. Security events are named
// separately so alerting can key on the prefix.
type EventType string

const (
	EvRunCreated   EventType = "run.created"
	EvRunState     EventType = "run.state"
	EvStepFinished EventType = "step.finished"
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
)

// Event is one immutable fact about a run. Sequence is unique per run and gives
// the client a resumable cursor after a reconnect.
type Event struct {
	ID       string         `bson:"_id"               json:"id"`
	RunID    string         `bson:"runId"             json:"runId"`
	Sequence int64          `bson:"sequence"          json:"sequence"`
	Type     EventType      `bson:"type"              json:"type"`
	Message  string         `bson:"message,omitempty" json:"message,omitempty"`
	Data     map[string]any `bson:"data,omitempty"    json:"data,omitempty"`
	At       time.Time      `bson:"at"                json:"at"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
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
	// ErrDestructiveNeedsReview means an unattended run proposed a deletion.
	ErrDestructiveNeedsReview = wrap(domain.ErrForbidden, "deletions must be reviewed before they are applied")
)

type agentError struct {
	base error
	msg  string
}

func (e *agentError) Error() string { return e.msg }
func (e *agentError) Unwrap() error { return e.base }

func wrap(base error, msg string) error { return &agentError{base: base, msg: msg} }
