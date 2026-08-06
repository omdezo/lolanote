package domain

import "time"

// Delegation is an attenuated, expiring, capability-based grant under which a
// non-human actor (the AI agent) acts on a human's behalf.
//
// The model NEVER holds the user's authority. It proposes; the harness mints a
// Delegation that is strictly weaker than the human's own permissions, and the
// existing write path verifies every op against it. Two independent gates must
// both pass for an agent write to land:
//
//  1. the human's ACL role on the board  (AccessResolver — unchanged)
//  2. the delegation's scope + capability (verifyDelegation — added)
//
// This is what makes prompt injection non-catastrophic. A card whose text says
// "share this board publicly" cannot succeed: sharing is not a Capability, is
// not in any tool registry, and is rejected at the service layer even if
// somehow reached.
type Delegation struct {
	// RunID identifies the agent run this grant belongs to. Every transaction
	// it authorizes carries the same id, which is what makes a run revertible.
	RunID string `bson:"runId" json:"runId"`

	// OnBehalfOf is the human sub. ACL checks continue to use this — the agent
	// can never reach content its principal could not reach.
	OnBehalfOf string `bson:"onBehalfOf" json:"onBehalfOf"`

	// RootBoardID is a hard containment boundary, computed server-side at
	// admission and never taken from the model. Every op must target an element
	// inside this board's subtree.
	RootBoardID string `bson:"rootBoardId" json:"rootBoardId"`

	// Capabilities is the exact closed set of operations permitted. Absent
	// capability = denied; there is no implicit grant.
	Capabilities []Capability `bson:"capabilities" json:"capabilities"`

	// Consequence is the highest side-effect class this grant may produce.
	Consequence Consequence `bson:"consequence" json:"consequence"`

	// MaxOps bounds a single transaction so a malformed proposal cannot rewrite
	// an entire board in one commit.
	MaxOps int `bson:"maxOps" json:"maxOps"`

	// DestinationBoardIDs widens containment to a CLOSED, explicit list of
	// boards an approved plan files into. Filing an item onto the board it
	// actually belongs on is the commonest real chore, and a single-subtree
	// grant cannot express it.
	//
	// Three properties keep this ATTENUATION rather than expansion:
	//   - Every id is validated against the HUMAN's own edit rights at apply
	//     time, so the grant can never reach further than the person it acts
	//     for.
	//   - It is derived from a plan a person approved, not from the model's
	//     request, so board content cannot steer it.
	//   - It is minted per commit and expires with the run.
	DestinationBoardIDs []string `bson:"destinationBoardIds,omitempty" json:"destinationBoardIds,omitempty"`

	// ContentKeys is the allowlist of content keys this grant may write, keyed
	// by the originating ActionKind — the compiler's own answer to "what does
	// this kind produce", carried on the grant so the write path can enforce it
	// without importing the planner that derives it.
	//
	// An allowlist rather than a denylist because content is a schemaless map:
	// the guard used to name isHome and isTemplate and miss `locked`,
	// `cloneSourceId` and `agentInstructions` — the standing rules that
	// CONSTRAIN the agent — and the only thing stopping a run rewriting its own
	// instructions was that no tool happened to emit that key. Under an
	// allowlist privileged keys need no enumeration at all: they are excluded
	// by not being produced.
	//
	// A kind ABSENT from the map is refused. That polarity is the whole point —
	// treating "not declared" as "no restrictions" rebuilds the denylist one
	// exception at a time.
	ContentKeys map[string][]string `bson:"contentKeys,omitempty" json:"contentKeys,omitempty"`

	// RequiresApproval says a human must have looked at the plan before this
	// grant may write, because the BOARD said so — not because the request did.
	//
	// Autonomy used to be a property of the request, so whoever pressed the
	// button decided whether anyone previewed. On a shared board that is the
	// wrong human: an editor could set autonomy=auto and have thirty
	// model-chosen actions land on the owner's board with no preview by anyone
	// but themselves. Admission now resolves the board's policy and downgrades,
	// and this flag is the write path's independent copy of that decision — so
	// a later code path that reconstructs a run cannot quietly restore auto.
	RequiresApproval bool `bson:"requiresApproval,omitempty" json:"requiresApproval,omitempty"`

	// ApprovedBy is the sub of the human who pressed Apply. Empty on an
	// unattended run, which is exactly the pair RequiresApproval refuses.
	ApprovedBy string `bson:"approvedBy,omitempty" json:"approvedBy,omitempty"`

	ExpiresAt time.Time `bson:"expiresAt" json:"expiresAt"`
}

// ContentKeysVerbatim marks a kind whose key set cannot be enumerated because it
// copies another element's content wholesale. Exactly one kind is like this —
// duplicate — and it is named here rather than allowed to fall through the
// "undeclared" branch, because "we cannot tell" and "it writes nothing" are
// different answers and only one of them is safe to wave through.
const ContentKeysVerbatim = "*"

// AllowsContentKey reports whether an op compiled from this action kind may
// write this content key, and whether the caller must still apply its own
// last-resort checks (true for a verbatim copy, which has no enumerable set).
func (d *Delegation) AllowsContentKey(kind, key string) (allowed, verbatim bool) {
	if d == nil {
		return false, false
	}
	keys, declared := d.ContentKeys[kind]
	if !declared {
		return false, false
	}
	for _, k := range keys {
		if k == ContentKeysVerbatim {
			return true, true
		}
		if k == key {
			return true, false
		}
	}
	return false, false
}

// Roots is every board this grant may write inside: its containment root, plus
// any destination an approved plan added. Containment checks run against the
// whole set rather than the root alone.
func (d *Delegation) Roots() []string {
	if d == nil {
		return nil
	}
	out := make([]string, 0, len(d.DestinationBoardIDs)+1)
	out = append(out, d.RootBoardID)
	out = append(out, d.DestinationBoardIDs...)
	return out
}

// Capability is one permitted operation class.
type Capability string

const (
	CapElementCreate Capability = "element.create"
	CapElementUpdate Capability = "element.update"
	CapElementMove   Capability = "element.move"
	CapElementDelete Capability = "element.delete"
)

// Consequence classifies side effects by reversibility and blast radius
// (spec §6.3 side_effect / §25.2 approval by consequence class).
type Consequence int

const (
	// ConsequenceRead performs no mutation.
	ConsequenceRead Consequence = iota
	// ConsequenceReversibleWrite mutates the element graph in ways the
	// precomputed UndoChanges fully restore.
	ConsequenceReversibleWrite
	// ConsequenceExternal reaches outside the system (web fetch, email).
	ConsequenceExternal
	// ConsequenceDestructive deletes or otherwise loses information.
	ConsequenceDestructive
)

// AtLeast reports whether c permits side effects of class other.
func (c Consequence) AtLeast(other Consequence) bool { return c >= other }

// Allows reports whether the grant carries a capability.
func (d *Delegation) Allows(c Capability) bool {
	if d == nil {
		return false
	}
	for _, have := range d.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// Expired reports whether the grant is no longer valid at t.
func (d *Delegation) Expired(t time.Time) bool { return d == nil || t.After(d.ExpiresAt) }

// CapabilityForAction maps a transaction op action onto the capability it needs.
func CapabilityForAction(a Action) Capability {
	switch a {
	case ActionCreate:
		return CapElementCreate
	case ActionUpdate:
		return CapElementUpdate
	case ActionMove:
		return CapElementMove
	case ActionDelete, ActionRestore:
		return CapElementDelete
	}
	return Capability("unknown")
}

// ConsequenceForAction maps an op action onto its side-effect class.
func ConsequenceForAction(a Action) Consequence {
	if a == ActionDelete {
		return ConsequenceDestructive
	}
	return ConsequenceReversibleWrite
}
