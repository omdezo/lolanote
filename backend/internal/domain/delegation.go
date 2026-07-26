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

	ExpiresAt time.Time `bson:"expiresAt" json:"expiresAt"`
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
