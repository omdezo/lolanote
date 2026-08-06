// Package authz holds the organization-level permission lattice. It sits beside
// the board ACL in service.AccessResolver, not above it: an org role decides who
// administers an organization — members, settings, billing, audit — and never by
// itself who may read an element (§4.2, §4.3). Nothing here imports Echo or Mongo.
//
// The OrgResolver that turns (org, subject) into an OrgRole by reading
// org_memberships is phase 1 (§II.1.2): the repository port it needs does not
// exist yet, and a stub answering from nothing would be a lattice with no
// membership behind it. What lives here is the vocabulary alone — the ordering,
// the parser, and the context carrier — which the resolver and the route
// middleware will both use unchanged.
package authz

// OrgRole is the effective standing a caller has inside one organization. It is
// persisted as a string in org_memberships.role; the ordering, not the string,
// is what code compares.
type OrgRole int

const (
	OrgNone   OrgRole = iota // not a member, or a membership that is not active
	OrgGuest                 // named on org boards, no org surface (unbilled seat, §6)
	OrgMember                // + create org boards
	OrgAdmin                 // + members, settings, audit, content override
	OrgOwner                 // + billing, plan changes, ownership transfer, delete org
)

// The stored spellings. Membership rows carry these, so they are part of the
// data format and may not be renamed without a migration.
const (
	roleNameNone   = "none"
	roleNameGuest  = "guest"
	roleNameMember = "member"
	roleNameAdmin  = "admin"
	roleNameOwner  = "owner"
)

// AtLeast expresses the lattice: each role carries the powers of every role
// below it.
func (r OrgRole) AtLeast(min OrgRole) bool { return r >= min }

// String is the stored spelling. Anything outside the lattice reports as none,
// so a corrupt value cannot be written back as a role that grants something.
func (r OrgRole) String() string {
	switch r {
	case OrgGuest:
		return roleNameGuest
	case OrgMember:
		return roleNameMember
	case OrgAdmin:
		return roleNameAdmin
	case OrgOwner:
		return roleNameOwner
	default:
		return roleNameNone
	}
}

// ParseOrgRole reads a stored role. It fails closed: an empty string, an unknown
// word, a different case, or surrounding whitespace all yield OrgNone. The input
// is an enum this backend wrote, never text a person typed, so there is nothing
// to be lenient about — and leniency here is a privilege grant.
func ParseOrgRole(s string) OrgRole {
	switch s {
	case roleNameGuest:
		return OrgGuest
	case roleNameMember:
		return OrgMember
	case roleNameAdmin:
		return OrgAdmin
	case roleNameOwner:
		return OrgOwner
	default:
		return OrgNone
	}
}
