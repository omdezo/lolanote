// Package service holds the business logic. Each service is a struct with
// constructor-injected dependencies declared as domain interfaces — the OOP
// backbone of the backend. Nothing here imports Echo or Mongo.
package service

import (
	"context"

	"qomranote/backend/internal/domain"
)

// Role is the effective permission a caller has on an element.
type Role int

const (
	RoleNone Role = iota
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
}

// NewAccessResolver constructs the resolver.
func NewAccessResolver(elements domain.ElementRepository) *AccessResolver {
	return &AccessResolver{elements: elements}
}

// maxDepth bounds ancestor walks against cycles created by bad data.
const maxDepth = 64

// Resolve returns the caller's role on the element plus the nearest ancestor
// BOARD (the room key for realtime broadcast and the ACL carrier).
func (a *AccessResolver) Resolve(ctx context.Context, elementID string, p *domain.Principal) (Role, *domain.Element, error) {
	role := RoleNone
	var nearestBoard *domain.Element

	id := elementID
	for depth := 0; id != "" && depth < maxDepth; depth++ {
		el, err := a.elements.Get(ctx, id)
		if err != nil {
			return RoleNone, nil, err
		}
		if el.Type == domain.TypeBoard {
			if nearestBoard == nil {
				nearestBoard = el
			}
			if r := roleFromACL(el.ACL, p); r > role {
				role = r
			}
		}
		id = el.Location.ParentID
	}
	if nearestBoard == nil {
		return RoleNone, nil, domain.ErrNotFound
	}
	return role, nearestBoard, nil
}

func roleFromACL(acl *domain.ACL, p *domain.Principal) Role {
	if acl == nil || p == nil {
		return RoleNone
	}
	if acl.OwnerID == p.Sub {
		return RoleOwner
	}
	for _, e := range acl.Editors {
		if e == p.Sub {
			return RoleEdit
		}
	}
	if p.ShareToken != "" {
		if acl.PublicEditLink != "" && acl.PublicEditLink == p.ShareToken && p.Sub != "" {
			// Editor links require a logged-in account (§6.1 mechanism 2).
			return RoleEdit
		}
		if acl.ViewLink != nil && acl.ViewLink.Token == p.ShareToken {
			if acl.ViewLink.AllowFeedback && p.Sub != "" {
				return RoleFeedback
			}
			return RoleView
		}
	}
	return RoleNone
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
