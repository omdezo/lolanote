package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"

	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
)

// KeycloakIdentityProvider looks users up — and manages accounts — through
// the Keycloak Admin API using the confidential qomranote-api service account
// (view-users + manage-users roles).
type KeycloakIdentityProvider struct {
	client       *gocloak.GoCloak
	realm        string
	clientID     string
	clientSecret string
	// webClientID is the public browser client; direct grants against it
	// verify a user's current password without the API ever storing it.
	webClientID string

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

var (
	_ domain.IdentityProvider = (*KeycloakIdentityProvider)(nil)
	_ domain.AccountManager   = (*KeycloakIdentityProvider)(nil)
)

// NewKeycloakIdentityProvider constructs the provider against the
// container-internal Keycloak base URL.
func NewKeycloakIdentityProvider(cfg *config.Config) *KeycloakIdentityProvider {
	return &KeycloakIdentityProvider{
		client:       gocloak.NewClient(cfg.KeycloakInternalBase),
		realm:        cfg.KeycloakRealm,
		clientID:     cfg.KeycloakAdminClient,
		clientSecret: cfg.KeycloakAdminSecret,
		webClientID:  cfg.KeycloakWebClient,
	}
}

func (p *KeycloakIdentityProvider) adminToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Now().Before(p.tokenExpiry) {
		return p.token, nil
	}
	if p.clientSecret == "" {
		return "", fmt.Errorf("keycloak admin client secret not configured")
	}
	jwt, err := p.client.LoginClient(ctx, p.clientID, p.clientSecret, p.realm)
	if err != nil {
		return "", fmt.Errorf("keycloak service login: %w", err)
	}
	p.token = jwt.AccessToken
	// Refresh a bit before actual expiry.
	p.tokenExpiry = time.Now().Add(time.Duration(jwt.ExpiresIn-30) * time.Second)
	return p.token, nil
}

// FindUserByEmail resolves an email to a Keycloak subject for invite-by-email
// sharing. Falls back to domain.ErrNotFound when no account exists.
func (p *KeycloakIdentityProvider) FindUserByEmail(ctx context.Context, email string) (string, string, error) {
	token, err := p.adminToken(ctx)
	if err != nil {
		return "", "", err
	}
	exact := true
	users, err := p.client.GetUsers(ctx, token, p.realm, gocloak.GetUsersParams{
		Email: &email, Exact: &exact,
	})
	if err != nil {
		return "", "", fmt.Errorf("keycloak user lookup: %w", err)
	}
	for _, u := range users {
		if u.ID == nil {
			continue
		}
		name := ""
		if u.FirstName != nil {
			name = *u.FirstName
		}
		if u.LastName != nil {
			name = fmt.Sprintf("%s %s", name, *u.LastName)
		}
		if name == "" && u.Username != nil {
			name = *u.Username
		}
		return *u.ID, name, nil
	}
	return "", "", domain.ErrNotFound
}

// UpdateProfile writes name/email changes back to Keycloak so the identity
// stays the single source of truth. Empty fields are left untouched.
func (p *KeycloakIdentityProvider) UpdateProfile(ctx context.Context, sub, firstName, lastName, email string) error {
	token, err := p.adminToken(ctx)
	if err != nil {
		return err
	}
	user, err := p.client.GetUserByID(ctx, token, p.realm, sub)
	if err != nil {
		return fmt.Errorf("keycloak get user: %w", err)
	}
	if firstName != "" {
		user.FirstName = gocloak.StringP(firstName)
	}
	if lastName != "" || firstName != "" {
		user.LastName = gocloak.StringP(lastName)
	}
	if email != "" {
		user.Email = gocloak.StringP(email)
		// A changed address needs re-verification once SMTP is wired; keep
		// the account usable meanwhile.
		user.EmailVerified = gocloak.BoolP(true)
	}
	if err := p.client.UpdateUser(ctx, token, p.realm, *user); err != nil {
		return fmt.Errorf("keycloak update user: %w", err)
	}
	return nil
}

// VerifyPassword confirms the caller knows their current password by running
// a resource-owner grant against the public web client. The password is used
// once and never persisted.
func (p *KeycloakIdentityProvider) VerifyPassword(ctx context.Context, usernameOrEmail, password string) error {
	_, err := p.client.Login(ctx, p.webClientID, "", p.realm, usernameOrEmail, password)
	if err != nil {
		return domain.ErrUnauthorized
	}
	return nil
}

// SetPassword replaces the user's password (non-temporary).
func (p *KeycloakIdentityProvider) SetPassword(ctx context.Context, sub, newPassword string) error {
	token, err := p.adminToken(ctx)
	if err != nil {
		return err
	}
	if err := p.client.SetPassword(ctx, token, sub, p.realm, newPassword, false); err != nil {
		return fmt.Errorf("keycloak set password: %w", err)
	}
	return nil
}

// DeleteUser permanently removes the identity.
func (p *KeycloakIdentityProvider) DeleteUser(ctx context.Context, sub string) error {
	token, err := p.adminToken(ctx)
	if err != nil {
		return err
	}
	if err := p.client.DeleteUser(ctx, token, p.realm, sub); err != nil {
		return fmt.Errorf("keycloak delete user: %w", err)
	}
	return nil
}
