package service

import (
	"context"
	"errors"
	"time"

	"qomranote/backend/internal/domain"
)

// IDGenerator mints 24-hex ObjectIds; injected so services stay database-agnostic.
type IDGenerator func() string

// UserService bootstraps accounts. Every account is rooted at a private Home
// board created on first login — it can never be shared or exported (§3.1).
type UserService struct {
	users    domain.UserRepository
	elements domain.ElementRepository
	identity domain.IdentityProvider
	newID    IDGenerator
}

// NewUserService constructs the service.
func NewUserService(users domain.UserRepository, elements domain.ElementRepository, identity domain.IdentityProvider, newID IDGenerator) *UserService {
	return &UserService{users: users, elements: elements, identity: identity, newID: newID}
}

// Bootstrap finds or lazily creates the user row + Home board for a verified
// principal. Called by GET /me on every app load.
func (s *UserService) Bootstrap(ctx context.Context, p *domain.Principal) (*domain.User, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	now := time.Now().UTC()
	home := &domain.Element{
		ID:   s.newID(),
		Type: domain.TypeBoard,
		Location: domain.Location{
			ParentID: "", // Home is the root: no parent
			Section:  domain.SectionCanvas,
		},
		Content: domain.Content{
			"title":  "Home",
			"isHome": true,
		},
		ACL:       &domain.ACL{OwnerID: p.Sub, Editors: []string{}},
		CreatedBy: p.Sub,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.elements.Insert(ctx, home); err != nil {
		return nil, err
	}

	u = &domain.User{
		ID:          s.newID(),
		KeycloakSub: p.Sub,
		Email:       p.Email,
		DisplayName: p.Name,
		HomeBoardID: home.ID,
		Plan:        "free",
		CreatedAt:   now,
	}
	if err := s.users.Insert(ctx, u); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Concurrent first requests raced; the winner's row is canonical.
			_ = s.elements.HardDelete(ctx, []string{home.ID})
			return s.users.GetBySub(ctx, p.Sub)
		}
		return nil, err
	}
	return u, nil
}

// ResolvedUser is a public-safe identity slice for rendering collaborators.
type ResolvedUser struct {
	Sub       string `json:"sub"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	Email     string `json:"email,omitempty"`
}

// ResolveNames maps subs to display names (comment authors, assignees,
// mention pickers). Emails are included only when the target user allows it
// (privacy → ShowEmailToOthers). Unknown subs resolve to a truncated id.
func (s *UserService) ResolveNames(ctx context.Context, subs []string) []ResolvedUser {
	if len(subs) > 100 {
		subs = subs[:100]
	}
	out := make([]ResolvedUser, 0, len(subs))
	seen := map[string]bool{}
	for _, sub := range subs {
		if sub == "" || seen[sub] {
			continue
		}
		seen[sub] = true
		u, err := s.users.GetBySub(ctx, sub)
		if err != nil {
			out = append(out, ResolvedUser{Sub: sub, Name: shortSub(sub)})
			continue
		}
		r := ResolvedUser{Sub: sub, Name: u.DisplayName, AvatarURL: u.AvatarURL}
		if u.EffectiveSettings().Privacy.ShowEmailToOthers {
			r.Email = u.Email
		}
		if r.Name == "" {
			r.Name = shortSub(sub)
		}
		out = append(out, r)
	}
	return out
}

func shortSub(sub string) string {
	if len(sub) > 8 {
		return sub[:8]
	}
	return sub
}

// LookupByEmail resolves a collaborator: Keycloak first (source of truth),
// local mirror as fallback when the admin client is not configured.
func (s *UserService) LookupByEmail(ctx context.Context, email string) (sub, name string, err error) {
	if s.identity != nil {
		sub, name, err = s.identity.FindUserByEmail(ctx, email)
		if err == nil {
			return sub, name, nil
		}
		if errors.Is(err, domain.ErrNotFound) {
			return "", "", domain.ErrNotFound
		}
	}
	u, uerr := s.users.GetByEmail(ctx, email)
	if uerr != nil {
		return "", "", uerr
	}
	return u.KeycloakSub, u.DisplayName, nil
}
