package authz

import (
	"context"
	"testing"
)

func TestWithOrgRole_RoundTrips(t *testing.T) {
	for _, r := range []OrgRole{OrgNone, OrgGuest, OrgMember, OrgAdmin, OrgOwner} {
		t.Run(r.String(), func(t *testing.T) {
			if got := OrgRoleFrom(WithOrgRole(context.Background(), r)); got != r {
				t.Errorf("OrgRoleFrom = %v, want %v", got, r)
			}
		})
	}
}

// The absent case is the one that matters: a handler reached without the
// membership middleware reads as a non-member, not as a missing check.
func TestOrgRoleFrom_AbsentIsNone(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"background", context.Background()},
		{"todo", context.TODO()},
		{"carrying an unrelated value", context.WithValue(context.Background(), struct{ other int }{}, OrgOwner)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OrgRoleFrom(tc.ctx); got != OrgNone {
				t.Errorf("OrgRoleFrom = %v, want %v", got, OrgNone)
			}
		})
	}
}

// The key type is unexported so that nothing outside this package can plant a
// role. A foreign key of identical shape and name must not be mistaken for it —
// context compares keys by type identity, and this pins that we rely on it.
func TestOrgRoleFrom_IgnoresAForeignKeyOfTheSameShape(t *testing.T) {
	type orgRoleKey struct{} // deliberately shadows the real one
	ctx := context.WithValue(context.Background(), orgRoleKey{}, OrgOwner)
	if got := OrgRoleFrom(ctx); got != OrgNone {
		t.Errorf("OrgRoleFrom = %v, want %v — a look-alike key forged a role", got, OrgNone)
	}
}

// The last writer wins, so a middleware that resolves twice cannot leave a
// stale higher role behind.
func TestWithOrgRole_Overrides(t *testing.T) {
	ctx := WithOrgRole(WithOrgRole(context.Background(), OrgOwner), OrgGuest)
	if got := OrgRoleFrom(ctx); got != OrgGuest {
		t.Errorf("OrgRoleFrom = %v, want %v", got, OrgGuest)
	}
}
