package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"golang.org/x/crypto/bcrypt"

	"qomranote/backend/internal/domain"
)

// ShareService implements all four sharing mechanisms (§6.1): invite editors
// by email, shareable editor link, read-only link with feedback, and
// account-free view link (with optional password + welcome message).
// Permissions cascade to nested sub-boards automatically because access
// resolution walks ancestors (see AccessResolver).
type ShareService struct {
	elements      domain.ElementRepository
	users         *UserService
	notifications domain.NotificationRepository
	access        *AccessResolver
}

// NewShareService constructs the service.
func NewShareService(elements domain.ElementRepository, users *UserService, notifications domain.NotificationRepository, access *AccessResolver) *ShareService {
	return &ShareService{elements: elements, users: users, notifications: notifications, access: access}
}

// ShareState is what the Share dialog renders.
type ShareState struct {
	OwnerID        string           `json:"ownerId"`
	Editors        []string         `json:"editors"`
	PublicEditLink string           `json:"publicEditLink,omitempty"`
	ViewLink       *domain.ViewLink `json:"viewLink,omitempty"`
}

func (s *ShareService) requireOwner(ctx context.Context, boardID string, p *domain.Principal) (*domain.Element, error) {
	board, err := s.elements.Get(ctx, boardID)
	if err != nil {
		return nil, err
	}
	if board.Type != domain.TypeBoard {
		return nil, domain.ErrValidation
	}
	if isHome(board) {
		return nil, domain.ErrHomeBoard // the Home board can never be shared (§3.1)
	}
	role, _, err := s.access.Resolve(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if role != RoleOwner {
		return nil, domain.ErrForbidden
	}
	if board.ACL == nil {
		board.ACL = &domain.ACL{OwnerID: p.Sub, Editors: []string{}}
	}
	return board, nil
}

// State returns the current sharing configuration.
func (s *ShareService) State(ctx context.Context, p *domain.Principal, boardID string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// InviteEditor grants full edit rights to an existing account by email
// (mechanism 1). The invitee is notified with a deep link to the board.
func (s *ShareService) InviteEditor(ctx context.Context, p *domain.Principal, boardID, email string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	sub, _, err := s.users.LookupByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if sub == board.ACL.OwnerID {
		return stateFromACL(board.ACL), nil
	}
	for _, e := range board.ACL.Editors {
		if e == sub {
			return stateFromACL(board.ACL), nil
		}
	}
	board.ACL.Editors = append(board.ACL.Editors, sub)
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	_ = s.notifications.Insert(ctx, &domain.Notification{
		ID: newToken(12), UserID: sub, Kind: domain.NotifyShare, ActorID: p.Sub,
		BoardID: boardID, Message: p.Name + " shared \"" + board.Title() + "\" with you",
		CreatedAt: time.Now().UTC(),
	})
	return stateFromACL(board.ACL), nil
}

// RemoveEditor revokes a collaborator.
func (s *ShareService) RemoveEditor(ctx context.Context, p *domain.Principal, boardID, sub string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	kept := board.ACL.Editors[:0]
	for _, e := range board.ACL.Editors {
		if e != sub {
			kept = append(kept, e)
		}
	}
	board.ACL.Editors = kept
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// LinkOptions configures link creation.
type LinkOptions struct {
	Kind           string `json:"kind"` // edit | view
	AllowFeedback  bool   `json:"allowFeedback"`
	RequireAccount bool   `json:"requireAccount"`
	Password       string `json:"password,omitempty"`
	WelcomeMessage string `json:"welcomeMessage,omitempty"`
}

// CreateLink enables an editor link (mechanism 2) or a read-only/view link
// (mechanisms 3–4).
func (s *ShareService) CreateLink(ctx context.Context, p *domain.Principal, boardID string, opts LinkOptions) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	switch opts.Kind {
	case "edit":
		if board.ACL.PublicEditLink == "" {
			board.ACL.PublicEditLink = newToken(24)
		}
	case "view":
		link := &domain.ViewLink{
			Token:          newToken(24),
			AllowFeedback:  opts.AllowFeedback,
			RequireAccount: opts.RequireAccount,
			WelcomeMessage: opts.WelcomeMessage,
		}
		if board.ACL.ViewLink != nil {
			link.Token = board.ACL.ViewLink.Token // regenerating settings keeps the URL stable
		}
		if opts.Password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(opts.Password), bcrypt.DefaultCost)
			if err != nil {
				return nil, err
			}
			link.PasswordHash = string(hash)
		}
		board.ACL.ViewLink = link
	default:
		return nil, domain.ErrValidation
	}
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// RevokeLink disables a link kind.
func (s *ShareService) RevokeLink(ctx context.Context, p *domain.Principal, boardID, kind string) (*ShareState, error) {
	board, err := s.requireOwner(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "edit":
		board.ACL.PublicEditLink = ""
	case "view":
		board.ACL.ViewLink = nil
	default:
		return nil, domain.ErrValidation
	}
	if err := s.elements.SetACL(ctx, boardID, board.ACL); err != nil {
		return nil, err
	}
	return stateFromACL(board.ACL), nil
}

// ResolveToken maps a share token (from a /shared/:token URL) to its board,
// enforcing the optional password. Used by the public board entry point.
func (s *ShareService) ResolveToken(ctx context.Context, token, password string) (*domain.Element, string, error) {
	boards, err := s.elements.BoardsByShareToken(ctx, token)
	if err != nil {
		return nil, "", err
	}
	for _, board := range boards {
		if board.ACL == nil {
			continue
		}
		if board.ACL.PublicEditLink == token {
			return board, "edit", nil
		}
		if vl := board.ACL.ViewLink; vl != nil && vl.Token == token {
			if vl.PasswordHash != "" {
				if bcrypt.CompareHashAndPassword([]byte(vl.PasswordHash), []byte(password)) != nil {
					return nil, "", domain.ErrUnauthorized
				}
			}
			return board, "view", nil
		}
	}
	return nil, "", domain.ErrNotFound
}

func stateFromACL(acl *domain.ACL) *ShareState {
	st := &ShareState{OwnerID: acl.OwnerID, Editors: acl.Editors, PublicEditLink: acl.PublicEditLink}
	if acl.ViewLink != nil {
		st.ViewLink = acl.ViewLink
	}
	if st.Editors == nil {
		st.Editors = []string{}
	}
	return st
}

// newToken returns n bytes of hex-encoded cryptographic randomness.
func newToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
