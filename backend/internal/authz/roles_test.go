package authz

import "testing"

// The ordering is the whole authorization mechanism — every gate is a >=
// against it — so it is pinned rather than assumed from iota.
func TestOrgRole_LatticeOrdering(t *testing.T) {
	ascending := []OrgRole{OrgNone, OrgGuest, OrgMember, OrgAdmin, OrgOwner}
	for i := 1; i < len(ascending); i++ {
		if !(ascending[i-1] < ascending[i]) {
			t.Fatalf("%v is not below %v; the lattice has been reordered",
				ascending[i-1], ascending[i])
		}
	}
	if OrgNone != 0 {
		t.Errorf("OrgNone = %d, want 0 — the zero value must be the denying one", OrgNone)
	}
}

func TestOrgRole_AtLeast(t *testing.T) {
	cases := []struct {
		name string
		have OrgRole
		min  OrgRole
		want bool
	}{
		{"none clears nothing above it", OrgNone, OrgGuest, false},
		{"none clears none", OrgNone, OrgNone, true},
		{"guest is not a member", OrgGuest, OrgMember, false},
		{"member is not an admin", OrgMember, OrgAdmin, false},
		{"admin is not an owner", OrgAdmin, OrgOwner, false},
		{"member covers guest", OrgMember, OrgGuest, true},
		{"admin covers member", OrgAdmin, OrgMember, true},
		{"owner covers admin", OrgOwner, OrgAdmin, true},
		{"owner covers everything", OrgOwner, OrgGuest, true},
		{"a role covers itself", OrgAdmin, OrgAdmin, true},
		// A zero-valued minimum is what an uninitialized gate looks like; it
		// must not read as "anyone", but as "the caller need only exist".
		{"a zero minimum admits a non-member", OrgNone, OrgNone, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.have.AtLeast(tc.min); got != tc.want {
				t.Errorf("%v.AtLeast(%v) = %v, want %v", tc.have, tc.min, got, tc.want)
			}
		})
	}
}

// ParseOrgRole is the only door between stored text and a privilege, so every
// way of not being a known role has to land on OrgNone.
func TestParseOrgRole_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want OrgRole
	}{
		{"guest", "guest", OrgGuest},
		{"member", "member", OrgMember},
		{"admin", "admin", OrgAdmin},
		{"owner", "owner", OrgOwner},
		{"the none spelling", "none", OrgNone},
		{"empty — a missing field", "", OrgNone},
		{"unknown word", "superuser", OrgNone},
		{"a near miss", "owners", OrgNone},
		{"wrong case", "Owner", OrgNone},
		{"shouted", "OWNER", OrgNone},
		{"padded", " owner ", OrgNone},
		{"trailing newline", "owner\n", OrgNone},
		{"embedded null", "owner\x00", OrgNone},
		{"a role name inside a sentence", "role: owner", OrgNone},
		{"json-looking", `{"role":"owner"}`, OrgNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseOrgRole(tc.in); got != tc.want {
				t.Errorf("ParseOrgRole(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The string form is the persisted one, so it has to survive a round trip
// through storage unchanged.
func TestOrgRole_StringRoundTrips(t *testing.T) {
	for _, r := range []OrgRole{OrgNone, OrgGuest, OrgMember, OrgAdmin, OrgOwner} {
		if got := ParseOrgRole(r.String()); got != r {
			t.Errorf("ParseOrgRole(%v.String()) = %v, want %v", r, got, r)
		}
	}
}

// A value from outside the lattice — a widened enum, a corrupt read — must not
// name itself anything that would parse back into a grant.
func TestOrgRole_StringOfUnknownIsNone(t *testing.T) {
	for _, r := range []OrgRole{-1, OrgOwner + 1, 99} {
		if got := r.String(); got != roleNameNone {
			t.Errorf("OrgRole(%d).String() = %q, want %q", int(r), got, roleNameNone)
		}
	}
}
