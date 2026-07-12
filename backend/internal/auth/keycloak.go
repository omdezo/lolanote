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

// KeycloakIdentityProvider looks users up through the Keycloak Admin API
// using the confidential qomranote-api service account (view-users role).
type KeycloakIdentityProvider struct {
	client       *gocloak.GoCloak
	realm        string
	clientID     string
	clientSecret string

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
}

var _ domain.IdentityProvider = (*KeycloakIdentityProvider)(nil)

// NewKeycloakIdentityProvider constructs the provider against the
// container-internal Keycloak base URL.
func NewKeycloakIdentityProvider(cfg *config.Config) *KeycloakIdentityProvider {
	return &KeycloakIdentityProvider{
		client:       gocloak.NewClient(cfg.KeycloakInternalBase),
		realm:        cfg.KeycloakRealm,
		clientID:     cfg.KeycloakAdminClient,
		clientSecret: cfg.KeycloakAdminSecret,
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
