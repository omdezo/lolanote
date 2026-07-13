// Package auth integrates Keycloak: stateless OIDC token verification for
// every API/WS request, and an admin-API identity provider for collaborator
// lookup. The package is framework-free; the HTTP layer adapts it to Echo.
package auth

import (
	"context"
	"fmt"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"

	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
)

// Verifier validates Keycloak-issued JWTs against the realm's JWKS.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	// allowedAzp pins tokens to the browser client: a JWT minted for any
	// OTHER client in the realm (service accounts, future clients) is not a
	// valid credential for this API. Empty disables the check.
	allowedAzp string
}

// NewVerifier discovers the realm configuration. In docker split-horizon
// setups the token's issuer (browser-facing URL) differs from the URL the
// API uses to reach Keycloak; go-oidc supports that via an issuer override.
func NewVerifier(ctx context.Context, cfg *config.Config) (*Verifier, error) {
	discoveryBase := strings.TrimSuffix(cfg.KeycloakInternalBase, "/") + "/realms/" + cfg.KeycloakRealm
	if discoveryBase != cfg.KeycloakIssuer {
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.KeycloakIssuer)
	}
	provider, err := oidc.NewProvider(ctx, discoveryBase)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	return &Verifier{
		// Keycloak puts the requesting client in `azp` (aud carries
		// `account`), so the audience pin is enforced on azp below rather
		// than through go-oidc's ClientID check.
		verifier:   provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		allowedAzp: cfg.KeycloakWebClient,
	}, nil
}

// Claims we care about from Keycloak access tokens.
type tokenClaims struct {
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Azp               string `json:"azp"`
}

// VerifyToken checks signature, issuer, expiry, and the authorized party,
// and returns the caller (with the token's expiry, so downstream long-lived
// sessions — WebSockets — can end when the credential does).
func (v *Verifier) VerifyToken(ctx context.Context, raw string) (*domain.Principal, error) {
	if raw == "" {
		return nil, domain.ErrUnauthorized
	}
	idToken, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrUnauthorized, err)
	}
	var claims tokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: bad claims", domain.ErrUnauthorized)
	}
	if v.allowedAzp != "" && claims.Azp != v.allowedAzp {
		return nil, fmt.Errorf("%w: token issued to client %q", domain.ErrUnauthorized, claims.Azp)
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	return &domain.Principal{
		Sub: idToken.Subject, Email: claims.Email, Name: name,
		ExpiresAt: idToken.Expiry,
	}, nil
}
