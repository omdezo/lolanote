package authz

import "context"

// orgRoleKey keys the caller's role for the organization the request addresses.
// The type is unexported and empty, so no other package can collide with it or
// plant a value under it — the middleware that resolves the org segment is the
// only writer.
type orgRoleKey struct{}

// WithOrgRole stashes the resolved role for the rest of the request. Which
// organization it belongs to is not carried: one request addresses one org, and
// the resolution happens on the route that names it.
//
// This is a plain context.Context helper, not an echo.Context one, so services
// and the resolver can read the role without transport ever reaching them.
func WithOrgRole(ctx context.Context, r OrgRole) context.Context {
	return context.WithValue(ctx, orgRoleKey{}, r)
}

// OrgRoleFrom reads the role back. An absent value is OrgNone rather than an
// error: a handler reached without the middleware that resolves membership must
// be denied, never trusted.
func OrgRoleFrom(ctx context.Context) OrgRole {
	r, _ := ctx.Value(orgRoleKey{}).(OrgRole)
	return r
}
