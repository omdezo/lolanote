package service

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// AccountService owns everything behind the Settings dialog: profile edits,
// email/password changes (written through to Keycloak so the identity stays
// the source of truth), per-user settings, the privacy data export, and full
// account deletion.
type AccountService struct {
	users         domain.UserRepository
	elements      domain.ElementRepository
	labels        domain.LabelRepository
	attachments   domain.AttachmentRepository
	notifications domain.NotificationRepository
	accounts      domain.AccountManager // nil when the Keycloak admin client is not configured
	log           *zap.Logger
}

// NewAccountService constructs the service.
func NewAccountService(
	users domain.UserRepository,
	elements domain.ElementRepository,
	labels domain.LabelRepository,
	attachments domain.AttachmentRepository,
	notifications domain.NotificationRepository,
	accounts domain.AccountManager,
	log *zap.Logger,
) *AccountService {
	return &AccountService{
		users: users, elements: elements, labels: labels,
		attachments: attachments, notifications: notifications,
		accounts: accounts, log: log.Named("account"),
	}
}

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// ProfilePatch is the mutable slice of the profile. Empty fields are ignored;
// AvatarURL supports explicit clearing via the "-" sentinel.
type ProfilePatch struct {
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	AvatarURL   string `json:"avatarUrl"`
}

// UpdateProfile applies name/email/avatar changes to Keycloak (when managed)
// and the local mirror, returning the fresh user.
func (s *AccountService) UpdateProfile(ctx context.Context, p *domain.Principal, patch ProfilePatch) (*domain.User, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}

	patch.DisplayName = strings.TrimSpace(patch.DisplayName)
	patch.Email = strings.TrimSpace(strings.ToLower(patch.Email))
	if patch.Email != "" && !emailPattern.MatchString(patch.Email) {
		return nil, domain.ErrValidation
	}
	if patch.Email != "" && patch.Email != u.Email {
		// Refuse an address another local account already mirrors.
		if other, err := s.users.GetByEmail(ctx, patch.Email); err == nil && other.KeycloakSub != p.Sub {
			return nil, domain.ErrConflict
		}
	}

	// Write through to the identity server first — it is authoritative, and a
	// failure there must not desync the mirror.
	if s.accounts != nil && (patch.DisplayName != "" || patch.Email != "") {
		first, last := splitName(patch.DisplayName)
		if err := s.accounts.UpdateProfile(ctx, p.Sub, first, last, patch.Email); err != nil {
			return nil, err
		}
	}

	if patch.DisplayName != "" {
		u.DisplayName = patch.DisplayName
	}
	if patch.Email != "" {
		u.Email = patch.Email
	}
	switch patch.AvatarURL {
	case "":
	case "-":
		u.AvatarURL = ""
	default:
		u.AvatarURL = patch.AvatarURL
	}
	if err := s.users.Update(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// splitName maps a display name onto Keycloak's first/last fields.
func splitName(display string) (first, last string) {
	parts := strings.Fields(display)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}

// ChangePassword verifies the current password, then sets the new one.
// Requires the Keycloak admin client (manage-users).
func (s *AccountService) ChangePassword(ctx context.Context, p *domain.Principal, current, next string) error {
	if s.accounts == nil {
		return domain.ErrUnavailable
	}
	if len(next) < 8 {
		return domain.ErrValidation
	}
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return err
	}
	if err := s.accounts.VerifyPassword(ctx, u.Email, current); err != nil {
		return domain.ErrUnauthorized
	}
	return s.accounts.SetPassword(ctx, p.Sub, next)
}

// Settings returns the user's effective settings (defaults filled in).
func (s *AccountService) Settings(ctx context.Context, p *domain.Principal) (*domain.UserSettings, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	settings := u.EffectiveSettings()
	return &settings, nil
}

// UpdateSettings merges a JSON patch over the current effective settings —
// unmarshalling into the prefilled struct gives natural merge semantics
// (absent fields keep their value) — then normalizes and persists.
func (s *AccountService) UpdateSettings(ctx context.Context, p *domain.Principal, patch json.RawMessage) (*domain.UserSettings, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	settings := u.EffectiveSettings()
	if err := json.Unmarshal(patch, &settings); err != nil {
		return nil, domain.ErrValidation
	}
	settings.Normalize()
	if err := s.users.UpdateSettings(ctx, p.Sub, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// DataExport is the "download my data" payload (privacy tab).
type DataExport struct {
	ExportedAt time.Time         `json:"exportedAt"`
	User       *domain.User      `json:"user"`
	Boards     []*domain.Element `json:"boards"`
	Elements   []*domain.Element `json:"elements"`
	Labels     []*domain.Label   `json:"labels"`
}

// ExportData bundles everything the user owns into one JSON document.
func (s *AccountService) ExportData(ctx context.Context, p *domain.Principal) (*DataExport, error) {
	u, err := s.users.GetBySub(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	boards, err := s.elements.OwnedBoards(ctx, p.Sub, false)
	if err != nil {
		return nil, err
	}
	export := &DataExport{ExportedAt: time.Now().UTC(), User: u, Boards: boards}
	seen := map[string]bool{}
	for _, b := range boards {
		seen[b.ID] = true
	}
	for _, b := range boards {
		desc, err := s.elements.Descendants(ctx, b.ID, false)
		if err != nil {
			return nil, err
		}
		for _, el := range desc {
			if !seen[el.ID] {
				seen[el.ID] = true
				export.Elements = append(export.Elements, el)
			}
		}
	}
	if labels, err := s.labels.ListByOwner(ctx, p.Sub); err == nil {
		export.Labels = labels
	}
	return export, nil
}

// DeleteAccount purges everything the user owns, then removes the identity.
// Content the user contributed to other people's boards stays (their boards,
// their data); the departing user is only stripped from ACLs.
func (s *AccountService) DeleteAccount(ctx context.Context, p *domain.Principal) error {
	boards, err := s.elements.OwnedBoards(ctx, p.Sub, true)
	if err != nil {
		return err
	}
	for _, b := range boards {
		desc, err := s.elements.Descendants(ctx, b.ID, true)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(desc)+1)
		for _, el := range desc {
			ids = append(ids, el.ID)
		}
		ids = append(ids, b.ID)
		if err := s.elements.HardDelete(ctx, ids); err != nil {
			return err
		}
	}
	if err := s.elements.RemoveEditorEverywhere(ctx, p.Sub); err != nil {
		return err
	}
	// Best-effort side data; the account row and identity are what matter.
	if err := s.labels.DeleteByOwner(ctx, p.Sub); err != nil {
		s.log.Warn("purge labels", zap.Error(err))
	}
	if err := s.attachments.DeleteByOwner(ctx, p.Sub); err != nil {
		s.log.Warn("purge attachments", zap.Error(err))
	}
	if err := s.notifications.DeleteByUser(ctx, p.Sub); err != nil {
		s.log.Warn("purge notifications", zap.Error(err))
	}
	if err := s.users.Delete(ctx, p.Sub); err != nil {
		return err
	}
	if s.accounts != nil {
		if err := s.accounts.DeleteUser(ctx, p.Sub); err != nil {
			// The identity outliving the data is recoverable (next login
			// bootstraps a fresh empty account) — log loudly, don't fail.
			s.log.Error("keycloak user deletion failed after data purge",
				zap.String("sub", p.Sub), zap.Error(err))
		}
	}
	s.log.Info("account deleted", zap.String("sub", p.Sub))
	return nil
}
