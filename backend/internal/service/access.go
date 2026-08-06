// Package service holds the business logic. Each service is a struct with
// constructor-injected dependencies declared as domain interfaces — the OOP
// backbone of the backend. Nothing here imports Echo or Mongo.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"qomranote/backend/internal/domain"
)

// Role is the effective permission a caller has on an element.
type Role int

const (
	RoleNone     Role = iota
	RoleView          // can see content
	RoleFeedback      // view + comment/react/draw (read-only link with feedback, §6.1)
	RoleEdit          // full editing
	RoleOwner         // edit + sharing/ACL control
)

// CanView / CanEdit express the role lattice.
func (r Role) CanView() bool { return r >= RoleView }
func (r Role) CanEdit() bool { return r >= RoleEdit }

// AccessResolver computes effective permissions by walking the containment
// chain upward: sharing cascades downward, so any ancestor board's ACL can
// grant access to a deeply nested element (§3.2, §6.1).
type AccessResolver struct {
	elements domain.ElementRepository
	unlocks  *unlockMemo
}

// NewAccessResolver constructs the resolver.
func NewAccessResolver(elements domain.ElementRepository) *AccessResolver {
	return &AccessResolver{elements: elements, unlocks: newUnlockMemo()}
}

// unlockMemo remembers that one password already verified against one hash.
//
// Every read on a shared board now asks the password question, and bcrypt at the
// default cost is deliberately ~100ms — paying that per request would turn a
// board load into a second and a half and hand anyone with the link a CPU
// amplifier. So the comparison runs once per (hash, password) pair and the
// answer is remembered.
//
// The KEY is the hash, which is what makes this safe to keep: change the
// password and the bcrypt hash changes with it (new salt, new digest), so every
// remembered answer for the old one is unreachable by construction. There is no
// invalidation to forget. Nothing here is a credential — both halves are hashed
// together before they are stored, and the map holds only the digest.
type unlockMemo struct {
	mu sync.Mutex
	ok map[string]struct{}
}

// unlockMemoCap bounds the map. It is keyed on attacker-suppliable input, so it
// must not be allowed to grow without limit; a wrong password is never recorded,
// so reaching this at all means a great many distinct correct passwords.
const unlockMemoCap = 4096

func newUnlockMemo() *unlockMemo { return &unlockMemo{ok: map[string]struct{}{}} }

func (m *unlockMemo) verify(hash, password string) bool {
	if hash == "" {
		return true // no password on this link
	}
	if password == "" {
		return false
	}
	sum := sha256.Sum256([]byte(hash + "\x00" + password))
	k := hex.EncodeToString(sum[:])

	m.mu.Lock()
	_, known := m.ok[k]
	m.mu.Unlock()
	if known {
		return true
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return false
	}
	m.mu.Lock()
	if len(m.ok) >= unlockMemoCap {
		m.ok = map[string]struct{}{}
	}
	m.ok[k] = struct{}{}
	m.mu.Unlock()
	return true
}

// maxDepth bounds ancestor walks against cycles created by bad data.
const maxDepth = 64

// Provenance says HOW a role was obtained: named in the ACL, or held by a
// bearer token somebody could have forwarded.
//
// Every caller in the system asks "may this person edit here", and for ordinary
// edits the answer is rightly the same either way — the link exists so a
// contractor can drag a card. But a capability that MINTS capabilities is
// different: an agent run is a delegation plus a live model budget, and anyone
// who forwards an edit link would be handing a stranger both on somebody else's
// board. So admission asks the second question, and nothing else has to.
type Provenance int

const (
	// ProvNone is no access at all.
	ProvNone Provenance = iota
	// ProvViewLink / ProvEditLink: a bearer token, forwardable by construction.
	ProvViewLink
	ProvEditLink
	// ProvMember means the ACL names this person: owner or editor.
	ProvMember
)

// FromLink reports whether the role rests on a forwardable token.
func (p Provenance) FromLink() bool { return p == ProvViewLink || p == ProvEditLink }

// Resolve returns the caller's role on the element plus the nearest ancestor
// BOARD (the room key for realtime broadcast and the ACL carrier).
func (a *AccessResolver) Resolve(ctx context.Context, elementID string, p *domain.Principal) (Role, *domain.Element, error) {
	role, board, _, err := a.ResolveDetailed(ctx, elementID, p)
	return role, board, err
}

// ResolveDetailed is Resolve plus the provenance of the winning role.
func (a *AccessResolver) ResolveDetailed(ctx context.Context, elementID string, p *domain.Principal) (Role, *domain.Element, Provenance, error) {
	role := RoleNone
	prov := ProvNone
	var nearestBoard *domain.Element

	id := elementID
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		el, err := a.elements.Get(ctx, id)
		if err != nil {
			return RoleNone, nil, ProvNone, err
		}
		if el.Type == domain.TypeBoard {
			if nearestBoard == nil {
				nearestBoard = el
			}
			// Strictly greater, so the STRONGEST grant on the chain decides both
			// the role and how it was come by. A person named in a nested board's
			// ACL who also holds the parent's view link stays a member.
			if r, pv := a.roleFromACL(el.ACL, p); r > role || (r == role && pv > prov) {
				role, prov = r, pv
			}
		}
		id = el.Location.ParentID
	}
	if nearestBoard == nil {
		return RoleNone, nil, ProvNone, domain.ErrNotFound
	}
	return role, nearestBoard, prov, nil
}

// BoardChain returns the boards elementID is nested INSIDE, nearest first, plus
// the governing ACL — the nearest one on the chain, self included.
//
// Two callers needed the same walk for different reasons and both were doing it
// wrong. The single-run guard compared one board id against one board id while
// a run's blast radius is its whole subtree, so a second run started one level
// down passed the check trivially. And the run row recorded who STARTED it and
// never who owned the board it read, so account erasure had no key by which to
// find a collaborator's copy of a deleted person's content.
//
// The element itself is excluded from ancestors even when it is a board: a run
// rooted at B does not conflict with itself.
func (a *AccessResolver) BoardChain(ctx context.Context, elementID string) ([]string, *domain.ACL, error) {
	var ancestors []string
	var governing *domain.ACL

	id := elementID
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		el, err := a.elements.Get(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if el.Type == domain.TypeBoard {
			if governing == nil && el.ACL != nil {
				governing = el.ACL
			}
			if depth > 0 {
				ancestors = append(ancestors, el.ID)
			}
		}
		id = el.Location.ParentID
	}
	return ancestors, governing, nil
}

// roleFromACL answers what one ACL grants this caller.
//
// THE LINK'S CONDITIONS ARE PART OF THE LINK, and they were not.
//
// A view link can carry a password and a "requires an account" flag. Both were
// stored, both round-tripped to the sharing dialog, and neither was ever
// consulted here — the password was checked in exactly one place, the
// GET /shared/:token resolver a browser happens to call first, and
// requireAccount was checked nowhere in the codebase at all. So the token alone
// opened /boards/:id, /children, /unsorted and /export?format=json: skip the
// courtesy call and the password is not a control, it is a speed bump. Worse,
// changing the password did not revoke anything, because regenerating a view
// link deliberately keeps the token stable — so somebody who entered the old
// password once, learned the board id, and was then locked out by a password
// change was not locked out.
//
// A condition on a capability has to be evaluated where the capability is
// resolved. That is here, and only here, for every route at once.
func (a *AccessResolver) roleFromACL(acl *domain.ACL, p *domain.Principal) (Role, Provenance) {
	if acl == nil || p == nil {
		return RoleNone, ProvNone
	}
	if acl.OwnerID == p.Sub {
		return RoleOwner, ProvMember
	}
	for _, e := range acl.Editors {
		if e == p.Sub {
			return RoleEdit, ProvMember
		}
	}
	if p.ShareToken != "" {
		if acl.PublicEditLink != "" && acl.PublicEditLink == p.ShareToken && p.Sub != "" {
			// Editor links require a logged-in account (§6.1 mechanism 2).
			return RoleEdit, ProvEditLink
		}
		if vl := acl.ViewLink; vl != nil && vl.Token == p.ShareToken {
			if !a.viewLinkOpen(vl, p) {
				return RoleNone, ProvNone
			}
			if vl.AllowFeedback && p.Sub != "" {
				return RoleFeedback, ProvViewLink
			}
			return RoleView, ProvViewLink
		}
	}
	return RoleNone, ProvNone
}

// viewLinkOpen reports whether this caller has satisfied the link's own
// conditions: the password if there is one, an account if the owner required
// one.
func (a *AccessResolver) viewLinkOpen(vl *domain.ViewLink, p *domain.Principal) bool {
	if vl.RequireAccount && p.Sub == "" {
		return false
	}
	if vl.PasswordHash == "" {
		return true
	}
	if a.unlocks == nil {
		// A resolver built by a struct literal rather than the constructor. Fail
		// CLOSED: an unanswerable password question must not answer yes.
		return false
	}
	return a.unlocks.verify(vl.PasswordHash, p.SharePassword)
}

// ACLFor returns the slice of a board's ACL a caller of this role may see.
//
// A read-only link holder could promote themselves to editor by reading one JSON
// response. ACL.PublicEditLink and ViewLink.Token are ordinary json fields on
// Element, and GET /boards/:id, /children and /export?format=json all sit in the
// optional-auth group — so `?format=json` with a view token returned the board's
// own EDIT token, every nested board's tokens, and every collaborator's subject
// id. Only PasswordHash carried json:"-", which proves the author knew how to
// withhold a field and simply did not think of these two.
//
// A share token now never leaves through an Element AT ANY ROLE, including the
// owner's: the owner-gated ShareState is the single door, which makes the
// default for the next field added to this struct "withheld" rather than
// "exposed". Editors keep the member list because the assignee picker is built
// from it; a viewer gets the owner and nothing else.
func ACLFor(acl *domain.ACL, role Role) *domain.ACL {
	if acl == nil {
		return nil
	}
	out := &domain.ACL{OwnerID: acl.OwnerID}
	if role.CanEdit() && len(acl.Editors) > 0 {
		out.Editors = append([]string(nil), acl.Editors...)
	}
	// The board's agent policy travels to anyone who could act on it. It is not
	// a credential — it is the rule that explains why an editor's request for an
	// unattended run comes back downgraded, and a downgrade with no visible
	// cause reads as the assistant being broken.
	if role.CanEdit() && acl.AgentPolicy != nil {
		p := *acl.AgentPolicy
		out.AgentPolicy = &p
	}
	return out
}

// redact rewrites one element's ACL to what this role may see. The pointer is
// REPLACED rather than edited, so a repository double that hands out its own
// state cannot be scrubbed by a read.
func redact(el *domain.Element, role Role) *domain.Element {
	if el == nil || el.ACL == nil {
		return el
	}
	el.ACL = ACLFor(el.ACL, role)
	return el
}

// redactAll is redact over a result set. Every read path that returns Elements
// goes through one of these two, which is what keeps "one function at every
// serialization boundary" checkable by grep rather than by memory.
func redactAll(els []*domain.Element, role Role) []*domain.Element {
	for _, el := range els {
		redact(el, role)
	}
	return els
}

// RequireEdit is the common guard for mutation paths.
func (a *AccessResolver) RequireEdit(ctx context.Context, elementID string, p *domain.Principal) (*domain.Element, error) {
	role, board, err := a.Resolve(ctx, elementID, p)
	if err != nil {
		return nil, err
	}
	if !role.CanEdit() {
		return nil, domain.ErrForbidden
	}
	return board, nil
}

// RequireView guards read paths.
func (a *AccessResolver) RequireView(ctx context.Context, elementID string, p *domain.Principal) (Role, *domain.Element, error) {
	role, board, err := a.Resolve(ctx, elementID, p)
	if err != nil {
		return RoleNone, nil, err
	}
	if !role.CanView() {
		return RoleNone, nil, domain.ErrForbidden
	}
	return role, board, nil
}
