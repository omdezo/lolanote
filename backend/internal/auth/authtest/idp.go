// Package authtest stands up a real OIDC provider in-process so token
// verification can be tested for what it actually does — signature, issuer,
// expiry, and the authorized-party pin — rather than around it.
//
// A stub Verifier would prove only that the stub returns what the stub was
// told to. These are the checks that decide whether a request is authenticated
// at all, so they are worth exercising against a genuine JWKS handshake.
package authtest

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"qomranote/backend/internal/auth"
	"qomranote/backend/internal/config"
)

const (
	// Realm is the realm every helper here mints for; callers that build their
	// own config should use it so discovery lines up.
	Realm = "qomra-test"
	keyID = "test-key"
)

// IDP is a minimal but real OIDC issuer: a discovery document, a JWKS, and an
// RSA key it signs with.
type IDP struct {
	Server *httptest.Server
	Issuer string
	key    *rsa.PrivateKey
}

// NewIDP starts the provider and shuts it down when the test ends.
func NewIDP(t *testing.T) *IDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	idp := &IDP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/"+Realm+"/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.Issuer,
			"jwks_uri":                              idp.Issuer + "/protocol/openid-connect/certs",
			"authorization_endpoint":                idp.Issuer + "/protocol/openid-connect/auth",
			"token_endpoint":                        idp.Issuer + "/protocol/openid-connect/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/realms/"+Realm+"/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
		}}})
	})

	idp.Server = httptest.NewServer(mux)
	idp.Issuer = idp.Server.URL + "/realms/" + Realm
	t.Cleanup(idp.Server.Close)
	return idp
}

// Claims are the parts of a Keycloak access token this API reads. Zero values
// are filled with something valid, so a test names only what it is varying.
type Claims struct {
	Sub               string
	Email             string
	Name              string
	PreferredUsername string
	Azp               string
	RealmRoles        []string  // emitted as realm_access.roles; omitted when empty
	Issuer            string    // override to forge a token from elsewhere
	ExpiresAt         time.Time // zero → one hour out
}

// Sign mints a signed JWT with the given claims.
func (i *IDP) Sign(t *testing.T, c Claims) string {
	t.Helper()
	if c.Sub == "" {
		c.Sub = "user-1"
	}
	if c.Issuer == "" {
		c.Issuer = i.Issuer
	}
	if c.ExpiresAt.IsZero() {
		c.ExpiresAt = time.Now().Add(time.Hour)
	}
	payload := map[string]any{
		"iss": c.Issuer,
		"sub": c.Sub,
		"aud": "account",
		"exp": c.ExpiresAt.Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
	for key, v := range map[string]string{
		"email": c.Email, "name": c.Name,
		"preferred_username": c.PreferredUsername, "azp": c.Azp,
	} {
		if v != "" {
			payload[key] = v
		}
	}
	// Absent rather than empty when unset: that is how Keycloak mints a token
	// for a caller who holds no realm role.
	if len(c.RealmRoles) > 0 {
		payload["realm_access"] = map[string]any{"roles": c.RealmRoles}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: i.key, KeyID: keyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	signed, err := signer.Sign(body)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// Config is what NewVerifier needs to discover this provider.
func (i *IDP) Config(webClient string) *config.Config {
	return &config.Config{
		KeycloakIssuer:       i.Issuer,
		KeycloakInternalBase: i.Server.URL,
		KeycloakRealm:        Realm,
		KeycloakWebClient:    webClient,
	}
}

// Verifier builds a real auth.Verifier pointed at this provider. webClient is
// the azp the tokens must carry; empty disables that pin.
func (i *IDP) Verifier(t *testing.T, webClient string) *auth.Verifier {
	t.Helper()
	v, err := auth.NewVerifier(context.Background(), i.Config(webClient))
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}
