package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"qomranote/backend/internal/auth"
	"qomranote/backend/internal/auth/authtest"
	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
)

const webClient = "qomra-web"

// ---- auth middleware --------------------------------------------------------
//
// This is the seam where an anonymous request becomes a Principal that every
// ACL check downstream trusts without re-deriving. It had no tests at all.

// echoWith mounts one handler behind the middleware under test.
func echoWith(v *auth.Verifier, required bool, h echo.HandlerFunc) *echo.Echo {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = errorHandler(zap.NewNop())
	e.GET("/probe", h, authMiddleware(v, required))
	return e
}

// seen records the principal the handler was actually given.
func seen(into **domain.Principal) echo.HandlerFunc {
	return func(c echo.Context) error {
		*into = principal(c)
		return c.NoContent(http.StatusOK)
	}
}

func TestAuthMiddleware_Required(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, webClient)

	t.Run("a valid bearer reaches the handler as a principal", func(t *testing.T) {
		var got *domain.Principal
		e := echoWith(v, true, seen(&got))
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+idp.Sign(t, authtest.Claims{
			Sub: "u-1", Email: "a@example.com", Azp: webClient,
		}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
		}
		if got == nil || got.Sub != "u-1" {
			t.Fatalf("handler saw %+v, want the token's subject", got)
		}
	})

	t.Run("no credential is 401, in the error envelope", func(t *testing.T) {
		e := echoWith(v, true, func(c echo.Context) error {
			t.Error("handler ran for an unauthenticated request")
			return nil
		})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("body = %s, want the {error:{code,message}} envelope", rec.Body)
		}
	})

	t.Run("a token for another client is 401", func(t *testing.T) {
		e := echoWith(v, true, func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+idp.Sign(t, authtest.Claims{Azp: "other"}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	// The header is the ONLY accepted place for a bearer. A query-string
	// credential lands in proxy logs, browser history and Referer headers, and
	// "it also works in the query string, just for convenience" is exactly how
	// that gets reintroduced.
	t.Run("a bearer in the query string is not a credential", func(t *testing.T) {
		token := idp.Sign(t, authtest.Claims{Sub: "u-1", Azp: webClient})
		for _, param := range []string{"access_token", "token", "bearer", "authorization"} {
			e := echoWith(v, true, func(c echo.Context) error {
				t.Errorf("handler ran for a request authenticated via ?%s=", param)
				return nil
			})
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe?"+param+"="+token, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("?%s= gave status %d, want 401", param, rec.Code)
			}
		}
	})

	t.Run("an Authorization header without the Bearer scheme is not a credential", func(t *testing.T) {
		token := idp.Sign(t, authtest.Claims{Sub: "u-1", Azp: webClient})
		for _, header := range []string{token, "bearer " + token, "Basic " + token, "Token " + token} {
			e := echoWith(v, true, func(c echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.Header.Set(echo.HeaderAuthorization, header)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("Authorization: %.20q gave status %d, want 401", header, rec.Code)
			}
		}
	})
}

func TestAuthMiddleware_Optional(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, webClient)

	// Anonymous is allowed through with an EMPTY principal, not a nil one and
	// not a partly-filled one. Share-link reads depend on this; so does the
	// resolver's ability to tell "nobody" from "somebody".
	t.Run("no credential becomes an anonymous principal", func(t *testing.T) {
		var got *domain.Principal
		e := echoWith(v, false, seen(&got))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got == nil {
			t.Fatal("principal was nil; every handler dereferences it")
		}
		if got.Sub != "" || got.Email != "" {
			t.Fatalf("anonymous principal carried identity: %+v", got)
		}
	})

	// A token that fails verification must NOT be half-trusted. It degrades to
	// anonymous — which is a strictly smaller authority, never a larger one.
	t.Run("an invalid credential becomes anonymous, not authenticated", func(t *testing.T) {
		other := authtest.NewIDP(t)
		forged := other.Sign(t, authtest.Claims{Sub: "attacker", Azp: webClient, Issuer: idp.Issuer})

		var got *domain.Principal
		e := echoWith(v, false, seen(&got))
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+forged)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if got == nil || got.Sub != "" {
			t.Fatalf("a forged token produced principal %+v", got)
		}
	})

	t.Run("a valid credential still authenticates", func(t *testing.T) {
		var got *domain.Principal
		e := echoWith(v, false, seen(&got))
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+idp.Sign(t, authtest.Claims{Sub: "u-9", Azp: webClient}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if got == nil || got.Sub != "u-9" {
			t.Fatalf("principal = %+v, want u-9", got)
		}
	})
}

// Provenance is stamped for the anonymous caller too. A share-link export is
// the row with no subject to name, so the address and the agent are the only
// things left that locate it — §9's "from where" is not an authenticated-only
// requirement.
func TestAuthMiddleware_StampsOriginOnAnonymousRequests(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, webClient)

	var got *domain.Principal
	e := echoWith(v, false, seen(&got))
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = "203.0.113.7:41234"
	req.Header.Set("User-Agent", "Mozilla/5.0 (probe)")
	e.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("principal was nil")
	}
	if got.Sub != "" {
		t.Fatalf("the request authenticated as %q; this case must stay anonymous", got.Sub)
	}
	if got.IP != "203.0.113.7" {
		t.Errorf("IP = %q, want the caller's address", got.IP)
	}
	if got.UserAgent != "Mozilla/5.0 (probe)" {
		t.Errorf("UserAgent = %q, want the request's", got.UserAgent)
	}
}

// The header is attacker-controlled and every audit row the request writes
// carries a copy, so the bound is applied at the boundary and not hoped for
// downstream. The cut lands on a rune boundary: the value is stored as a BSON
// string.
func TestAuthMiddleware_BoundsTheUserAgent(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, webClient)

	var got *domain.Principal
	e := echoWith(v, false, seen(&got))
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	// Multi-byte runes, so a naive byte slice would land mid-rune.
	req.Header.Set("User-Agent", strings.Repeat("é", 4096))
	e.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("principal was nil")
	}
	if len(got.UserAgent) > maxUserAgentBytes {
		t.Errorf("UserAgent kept %d bytes, want at most %d", len(got.UserAgent), maxUserAgentBytes)
	}
	if !utf8.ValidString(got.UserAgent) {
		t.Error("the truncation cut through a rune, so the stored value is not valid UTF-8")
	}
}

func TestAuthMiddleware_ShareToken(t *testing.T) {
	idp := authtest.NewIDP(t)
	v := idp.Verifier(t, webClient)

	cases := []struct {
		name   string
		header string
		query  string
		want   string
	}{
		{"from the header", "tok-header", "", "tok-header"},
		{"from the query when there is no header", "", "tok-query", "tok-query"},
		// The header is the deliberate channel; a query parameter is what a
		// pasted URL carries. If both are present the header wins, so a link
		// someone was sent cannot silently override the app's own state.
		{"the header wins over the query", "tok-header", "tok-query", "tok-header"},
		{"absent when neither is given", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *domain.Principal
			e := echoWith(v, false, seen(&got))
			url := "/probe"
			if tc.query != "" {
				url += "?shareToken=" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tc.header != "" {
				req.Header.Set("X-Share-Token", tc.header)
			}
			e.ServeHTTP(httptest.NewRecorder(), req)

			if got == nil || got.ShareToken != tc.want {
				t.Fatalf("ShareToken = %q, want %q", got.ShareToken, tc.want)
			}
		})
	}
}

// ---- error handler ----------------------------------------------------------

func TestErrorHandler_MapsDomainSentinels(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{domain.ErrNotFound, http.StatusNotFound},
		{domain.ErrForbidden, http.StatusForbidden},
		{domain.ErrUnauthorized, http.StatusUnauthorized},
		{domain.ErrConflict, http.StatusConflict},
		{domain.ErrValidation, http.StatusBadRequest},
		{domain.ErrHomeBoard, http.StatusBadRequest},
		{domain.ErrUnavailable, http.StatusNotImplemented},
		// Wrapped, because services wrap: errors.Is has to be doing the work,
		// not equality.
		{fmt.Errorf("resolving board: %w", domain.ErrForbidden), http.StatusForbidden},
		{fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", domain.ErrNotFound)), http.StatusNotFound},
		{echo.NewHTTPError(http.StatusTeapot, "short and stout"), http.StatusTeapot},
	}
	handle := errorHandler(zap.NewNop())
	for _, tc := range cases {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", nil), rec)
		handle(tc.err, c)
		if rec.Code != tc.status {
			t.Errorf("%v → %d, want %d", tc.err, rec.Code, tc.status)
		}
		if !strings.Contains(rec.Body.String(), `"code":`) {
			t.Errorf("%v → body %s, want the error envelope", tc.err, rec.Body)
		}
	}
}

// An error nobody mapped is a bug in this service, and its text is written for
// us — a Mongo URI, a wrapped filesystem path, an internal id. It gets logged
// and the caller is told nothing.
func TestErrorHandler_DoesNotLeakUnmappedErrors(t *testing.T) {
	handle := errorHandler(zap.NewNop())
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", nil), rec)

	handle(errors.New("dial mongodb://user:hunter2@10.0.0.4:27017 failed"), c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	for _, secret := range []string{"hunter2", "mongodb", "10.0.0.4"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body)
		}
	}
}

// A response already on the wire cannot be rewritten; writing a second body
// would corrupt it. Streaming exports and file serving both hit this.
func TestErrorHandler_LeavesACommittedResponseAlone(t *testing.T) {
	handle := errorHandler(zap.NewNop())
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", nil), rec)
	_ = c.String(http.StatusOK, "already sent")

	handle(domain.ErrForbidden, c)

	if body := rec.Body.String(); body != "already sent" {
		t.Fatalf("body = %q, want the committed response untouched", body)
	}
}

// ---- route coverage ---------------------------------------------------------

// Routes that are reachable without a bearer, each for a stated reason. A route
// arrives here only by being added deliberately — which is the point.
var publicRoutes = map[string]string{
	"GET /healthz": "liveness probe",
	"GET /readyz":  "readiness probe, used by compose and orchestrators",
	// Prometheus scrape target. Network-gated, not route-gated (§II.0.4): the
	// deployment keeps it off the public reverse proxy and reachable only from
	// the scrape network. A bearer on this route would break the scraper and
	// buy nothing the network boundary does not already buy.
	"GET /metrics": "prometheus scrape target, network-gated",
	// <img src> cannot send an Authorization header. Keys are unguessable
	// ObjectIds and PUT is authenticated.
	"GET /api/v1/blob/*": "local blob driver read",
	// Share links: the ACL resolver is the real gate, and it rejects a caller
	// with neither an account nor a matching share token.
	"GET /api/v1/shared/:token":         "resolve a share link",
	"GET /api/v1/boards/:id":            "share-link board read",
	"GET /api/v1/boards/:id/children":   "share-link board read",
	"GET /api/v1/boards/:id/unsorted":   "share-link board read",
	"GET /api/v1/boards/:id/childstats": "share-link board read",
	"GET /api/v1/boards/:id/export":     "share-link board read",
	// The stable address of an uploaded file. <img src> cannot send a bearer,
	// and a share-link holder is legitimately allowed to see a board's pictures
	// — so the ACL check lives in UploadService.Resolve, which fails closed and
	// signs a short-lived URL per request instead of storing a credential.
	"GET /api/v1/attachments/:id/blob": "share-link attachment read",
	"GET /ws":                          "single-use ticket in place of a bearer",
}

// Every route on the API surface must reject an unauthenticated caller, except
// the handful above.
//
// The failure this catches is not a wrong decision — it is a route registered
// on the bare Echo instance, or on the optional-auth group, instead of the
// authenticated one. That is a one-character difference at the call site and
// nothing else in the build notices it.
func TestRoutes_EveryApiRouteRejectsAnonymousCallers(t *testing.T) {
	e := newTestRouter(t)

	var unguarded []string
	for _, r := range e.Routes() {
		if r.Method == http.MethodConnect || strings.HasPrefix(r.Name, "github.com/labstack/echo") {
			continue // Echo's own 405/404 stubs
		}
		key := r.Method + " " + r.Path
		if _, public := publicRoutes[key]; public {
			continue
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(r.Method, concretePath(r.Path), nil))
		if rec.Code != http.StatusUnauthorized {
			unguarded = append(unguarded, fmt.Sprintf("%s → %d", key, rec.Code))
		}
	}
	sort.Strings(unguarded)
	if len(unguarded) > 0 {
		t.Errorf("%d route(s) answered an anonymous caller with something other than 401:\n  %s",
			len(unguarded), strings.Join(unguarded, "\n  "))
	}
}

// And the allowlist must not outlive the routes it describes: an entry for a
// route that no longer exists is a stale exemption waiting to be reused.
func TestRoutes_PublicAllowlistHasNoStaleEntries(t *testing.T) {
	e := newTestRouter(t)
	registered := map[string]bool{}
	for _, r := range e.Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for key, why := range publicRoutes {
		if !registered[key] {
			t.Errorf("allowlist exempts %q (%s) but no such route is registered", key, why)
		}
	}
}

// newTestRouter registers the real route table. The services behind it are nil:
// these tests assert that requests are refused BEFORE any handler runs, so a
// route that reaches a handler fails loudly rather than passing quietly.
func newTestRouter(t *testing.T) *echo.Echo {
	t.Helper()
	idp := authtest.NewIDP(t)
	h := &Handlers{Verifier: idp.Verifier(t, webClient), Log: zap.NewNop()}
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = errorHandler(zap.NewNop())
	registerRoutes(e, h)
	return e
}

var pathParam = regexp.MustCompile(`:[a-zA-Z]+|\*`)

// concretePath turns "/api/v1/boards/:id/share/editors/:sub" into a real URL.
func concretePath(pattern string) string {
	return pathParam.ReplaceAllString(pattern, "x")
}

// The CORS allowlist is configuration, not a wildcard. A deployment that ships
// with "*" would let any page on the internet drive this API with the user's
// cookies-equivalent — here, their bearer.
func TestCORSOriginsAreExplicit(t *testing.T) {
	cfg := &config.Config{CORSOrigins: "https://app.example.com, https://staging.example.com"}
	got := cfg.CORSOriginList()
	want := []string{"https://app.example.com", "https://staging.example.com"}
	if len(got) != len(want) {
		t.Fatalf("origins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origins = %v, want %v (whitespace must be trimmed)", got, want)
		}
	}
}
