package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"qomranote/backend/internal/auth/authtest"
	"qomranote/backend/internal/domain"
)

// VerifyToken is the single gate between an anonymous HTTP request and a
// Principal that every ACL check downstream trusts completely. It had no test.
func TestVerifyToken(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, "qomra-web")
	ctx := context.Background()

	t.Run("accepts a well-formed token from this realm", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{
			Sub: "abc-123", Email: "a@example.com", Name: "Aya", Azp: "qomra-web",
		})
		p, err := v.VerifyToken(ctx, raw)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if p.Sub != "abc-123" || p.Email != "a@example.com" || p.Name != "Aya" {
			t.Fatalf("principal did not carry the claims: %+v", p)
		}
		if p.ExpiresAt.IsZero() {
			t.Error("expiry must reach the principal — a websocket outlives the request " +
				"that opened it, and has nothing else to end on")
		}
	})

	t.Run("falls back to preferred_username when name is absent", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{PreferredUsername: "aya", Azp: "qomra-web"})
		p, err := v.VerifyToken(ctx, raw)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if p.Name != "aya" {
			t.Errorf("name = %q, want the username fallback", p.Name)
		}
	})

	// The azp pin is the reason a service-account token from elsewhere in the
	// realm is not a credential for this API. Nothing tested it.
	t.Run("rejects a token minted for a different client", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{Azp: "some-other-client"})
		if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("rejects a token with no azp at all when the pin is set", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{})
		if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("rejects an expired token", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{Azp: "qomra-web", ExpiresAt: time.Now().Add(-time.Minute)})
		if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("rejects a token claiming a different issuer", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{Azp: "qomra-web", Issuer: "https://evil.example.com/realms/qomra"})
		if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("err = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("rejects a token signed by somebody else", func(t *testing.T) {
		// A second provider with its own key, but claiming to be the first.
		other := authtest.NewIDP(t)
		raw := other.Sign(t, authtest.Claims{Azp: "qomra-web", Issuer: idp.Issuer})
		if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("a token signed with an unknown key verified: %v", err)
		}
	})

	t.Run("rejects an empty and a malformed token", func(t *testing.T) {
		for _, raw := range []string{"", "not-a-jwt", "a.b.c"} {
			if _, err := v.VerifyToken(ctx, raw); !errors.Is(err, domain.ErrUnauthorized) {
				t.Errorf("token %q: err = %v, want ErrUnauthorized", raw, err)
			}
		}
	})
}

// Realm roles are what will separate platform staff from everyone else, so the
// verified token has to be the only place they can come from.
func TestVerifyToken_CarriesRealmRoles(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, "qomra-web")
	ctx := context.Background()

	t.Run("realm_access roles reach the principal", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{
			Azp: "qomra-web", RealmRoles: []string{"platform-admin", "default-roles-qomra"},
		})
		p, err := v.VerifyToken(ctx, raw)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if !p.HasPlatformRole("platform-admin") {
			t.Fatalf("realm roles did not reach the principal: %v", p.PlatformRoles)
		}
		if p.HasPlatformRole("platform-owner") || p.HasPlatformRole("") {
			t.Error("a role the token did not carry read as held")
		}
	})

	// Every request re-verifies, and the resulting principals are handed to
	// long-lived things. Two of them sharing a backing array would mean one
	// caller's slice write showing up in another caller's authorisation.
	t.Run("each verification gets its own slice", func(t *testing.T) {
		raw := idp.Sign(t, authtest.Claims{Azp: "qomra-web", RealmRoles: []string{"platform-admin"}})
		first, err := v.VerifyToken(ctx, raw)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		second, err := v.VerifyToken(ctx, raw)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		first.PlatformRoles[0] = "tampered"
		if !second.HasPlatformRole("platform-admin") {
			t.Fatal("two principals from the same token alias one slice")
		}
	})

	t.Run("a token without realm roles yields none", func(t *testing.T) {
		p, err := v.VerifyToken(ctx, idp.Sign(t, authtest.Claims{Azp: "qomra-web"}))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if len(p.PlatformRoles) != 0 {
			t.Fatalf("PlatformRoles = %v, want empty for an ordinary caller", p.PlatformRoles)
		}
		if p.HasPlatformRole("platform-admin") {
			t.Error("an ordinary caller holds a platform role")
		}
	})
}

// A deployment can leave the web-client id unset. That must mean "any client in
// this realm", not "any token at all" — the signature and issuer still hold.
func TestVerifyToken_UnpinnedStillChecksTheSignature(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, "")
	ctx := context.Background()

	if _, err := v.VerifyToken(ctx, idp.Sign(t, authtest.Claims{Azp: "anything"})); err != nil {
		t.Fatalf("unpinned verifier rejected a valid token: %v", err)
	}
	other := authtest.NewIDP(t)
	forged := other.Sign(t, authtest.Claims{Issuer: idp.Issuer})
	if _, err := v.VerifyToken(ctx, forged); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("unpinned verifier accepted a forged signature: %v", err)
	}
}
