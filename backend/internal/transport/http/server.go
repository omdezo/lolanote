// Package http adapts the services to Echo: routing, auth middleware,
// error mapping, and the WebSocket upgrade endpoint.
package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"go.uber.org/zap"

	"qomranote/backend/internal/auth"
	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
)

// Server owns the Echo instance.
type Server struct {
	echo *echo.Echo
	cfg  *config.Config
	log  *zap.Logger
}

// NewServer builds the Echo app with logging, recovery, CORS, and body limits.
func NewServer(cfg *config.Config, log *zap.Logger, h *Handlers) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = errorHandler(log)

	e.Use(echomw.RequestID())
	e.Use(echomw.Recover())
	// Registered here so that every middleware that can short-circuit is inside
	// it. One that short-circuits never reaches the middleware registered after
	// it, so whatever metrics sits behind becomes the traffic the series never
	// show — precisely the traffic worth alerting on. Behind the rate limiter
	// that was throttled requests; behind CORS it was every preflight, and
	// OPTIONS appeared in the exposition zero times however many arrived.
	//
	// Recover alone stays outside, and has to: it must be outermost to catch a
	// panic raised anywhere below it, including in this middleware. The metrics
	// middleware therefore records from a defer, so a request unwinding past it
	// on its way to that recovery is still counted. RequestID above short-
	// circuits nothing, so nothing hides behind it.
	e.Use(MetricsMiddleware())
	e.Use(requestLogger(log))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: cfg.CORSOriginList(),
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete, http.MethodOptions},
		// Idempotency-Key is listed or the browser strips it on the preflight and
		// the retry protection it buys would be dead on arrival. X-Share-Password
		// was missing for the same reason and nobody noticed, because the compose
		// deployment puts the API behind the web container's nginx and every call
		// is same-origin — so the omission only bites a split-origin deployment,
		// where it now takes out every read on a password-protected board rather
		// than just the entry point.
		AllowHeaders: []string{
			echo.HeaderAuthorization, echo.HeaderContentType,
			"X-Share-Token", "X-Share-Password", "X-Client-Id", "Idempotency-Key",
		},
	}))
	e.Use(echomw.BodyLimit("64M")) // local-driver uploads flow through the API
	// Token-bucket per CREDENTIAL, falling back to client IP.
	//
	// Keying on the IP was fine for a browser app in front of one process and
	// wrong everywhere else. Behind a reverse proxy every user shares one
	// address unless XFF is trusted, so the limiter either throttled a whole
	// tenant together or effectively nobody — and a public credential makes the
	// address meaningless. The credential is the thing being rate-limited, so it
	// is the thing the bucket should be keyed on.
	e.Use(echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: 50, Burst: 120, ExpiresIn: 3 * time.Minute,
		}),
		IdentifierExtractor: credentialKey,
	}))

	registerRoutes(e, h)
	return &Server{echo: e, cfg: cfg, log: log}
}

// Start blocks serving HTTP until the context is canceled, then drains.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", zap.String("addr", s.cfg.HTTPAddr))
		if err := s.echo.Start(s.cfg.HTTPAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.echo.Shutdown(shutdownCtx)
	}
}

// ---- middleware ----

const principalKey = "qomra.principal"

// authMiddleware verifies the bearer token and stashes the Principal.
// Tokens are accepted from the Authorization header ONLY — query-string
// credentials leak into access logs and referrers (WebSockets use the
// single-use ticket exchange instead).
func authMiddleware(verifier *auth.Verifier, required bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			raw := bearerToken(c)
			p, err := verifier.VerifyToken(c.Request().Context(), raw)
			if err != nil {
				if required {
					return echo.NewHTTPError(http.StatusUnauthorized, "invalid or missing token")
				}
				p = &domain.Principal{} // anonymous (view links need no account)
			}
			if st := c.Request().Header.Get("X-Share-Token"); st != "" {
				p.ShareToken = st
			} else if st := c.QueryParam("shareToken"); st != "" {
				p.ShareToken = st
			}
			// The password a share link may require, stamped where every other
			// credential is stamped. The client has sent this header on every
			// request since password links shipped; exactly one handler read it,
			// so the password gated the entry point and nothing behind it.
			p.SharePassword = c.Request().Header.Get("X-Share-Password")
			// Provenance, stamped where the credential is resolved and nowhere
			// else. The id echo already minted was going nowhere; the source is
			// a property of HOW the caller authenticated, so this is the only
			// place in the system that can know it.
			p.RequestID = requestIDOf(c)
			p.Source = domain.SourceWeb
			// Stamped on the anonymous principal too. An unauthenticated action
			// — a share-link read, an export off a view link — is exactly the one
			// whose audit row has no subject to name, so the address is all the
			// row has left to locate it by.
			p.IP = c.RealIP()
			p.UserAgent = boundedUserAgent(c.Request().UserAgent())
			c.Set(principalKey, p)
			return next(c)
		}
	}
}

// credentialKey identifies WHO is being rate-limited.
//
// The bearer token is hashed rather than used raw: a limiter store is a map
// that outlives the request, and putting live credentials in it would make an
// in-memory data structure a secret store. Hashing also makes the key stable
// across a token refresh only within its own lifetime, which is the right
// granularity — a refresh is a new credential.
//
// It runs before authMiddleware, so it cannot ask for a verified principal;
// that is fine and deliberate. An UNVERIFIED token still identifies a caller
// well enough to bucket them, and a caller presenting garbage tokens falls back
// to their address, which is exactly where the old behaviour was right.
func credentialKey(c echo.Context) (string, error) {
	if tok := bearerToken(c); tok != "" {
		return "t:" + hashCredential(tok), nil
	}
	if st := c.Request().Header.Get("X-Share-Token"); st != "" {
		return "s:" + hashCredential(st), nil
	}
	return "ip:" + c.RealIP(), nil
}

func hashCredential(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

// agentRunLimiter is a much tighter bucket for the one route that spends money.
//
// A GET costs a database read; POST /agent/runs costs a model call, and the
// global limiter's 50/s let one credential start fifty of them a second. Rate
// and burst are per credential, and the numbers are set to "a person clicking",
// not "a client syncing".
func agentRunLimiter() echo.MiddlewareFunc {
	return echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate: 0.5, Burst: 5, ExpiresIn: 10 * time.Minute,
		}),
		IdentifierExtractor: credentialKey,
	})
}

// requestIDOf reads the id echo's RequestID middleware put on the response,
// falling back to the client's own header so a caller that supplies one can
// correlate its side of the trace with ours.
func requestIDOf(c echo.Context) string {
	if id := c.Response().Header().Get(echo.HeaderXRequestID); id != "" {
		return id
	}
	return c.Request().Header.Get(echo.HeaderXRequestID)
}

// maxUserAgentBytes bounds what a caller may make the audit log store.
//
// The header is attacker-controlled and every row the request writes carries a
// copy of it, so an unbounded value is unbounded write amplification chosen by
// the client. 256 bytes holds every honest browser string.
const maxUserAgentBytes = 256

// boundedUserAgent truncates on a rune boundary: the value is stored as a BSON
// string and a cut through a multi-byte rune would leave one that is not valid
// UTF-8.
func boundedUserAgent(ua string) string {
	if len(ua) <= maxUserAgentBytes {
		return ua
	}
	cut := maxUserAgentBytes
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

func bearerToken(c echo.Context) string {
	if h := c.Request().Header.Get(echo.HeaderAuthorization); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// principal fetches the verified caller; handlers can rely on it existing on
// authenticated routes.
func principal(c echo.Context) *domain.Principal {
	if p, ok := c.Get(principalKey).(*domain.Principal); ok {
		return p
	}
	return &domain.Principal{}
}

func requestLogger(log *zap.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			status := c.Response().Status
			if httpErr, ok := err.(*echo.HTTPError); ok {
				status = httpErr.Code
			}
			log.Debug("http",
				zap.String("method", c.Request().Method),
				zap.String("path", c.Request().URL.Path),
				zap.Int("status", status),
				zap.Duration("dur", time.Since(start)),
			)
			return err
		}
	}
}

// authoredError is implemented by an application error whose text was WRITTEN
// for the person who will read it, rather than derived from input or from a
// driver. Only those are safe to put on the wire verbatim.
type authoredError interface{ AuthoredMessage() string }

// authoredOr passes an authored refusal through and falls back to the generic
// word for everything else.
//
// The conflict and forbidden arms used to answer with one word each, so the
// agent's eight hand-written refusals — who is holding the board and for how
// long, that the board changed while you were reviewing, that this run is not
// working right now — all reached the browser as "conflict". The frontend could
// then do no better than guess, and every distinct conflict rendered as "Qomra
// is already working on this board" whether or not that was true. The one
// refusal people found legible was the budget one, purely because it happened to
// wrap ErrValidation, whose arm already passed the text through.
//
// The test is the error's TYPE, never the sentinel: a new sentinel of the same
// type is legible the day it is written, and a Mongo duplicate-key error dressed
// as a conflict still gets the generic word. err.Error() rather than the
// interface's own message so a sentinel composed with fmt.Errorf keeps the
// detail that was composed onto it.
func authoredOr(err error, generic string) string {
	var a authoredError
	if errors.As(err, &a) {
		if msg := err.Error(); msg != "" {
			return msg
		}
	}
	return generic
}

// errorHandler maps domain sentinels to HTTP statuses in one place and emits
// the {"error": {code, message}} envelope.
func errorHandler(log *zap.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		status := http.StatusInternalServerError
		message := "internal error"

		var httpErr *echo.HTTPError
		switch {
		case errors.As(err, &httpErr):
			status = httpErr.Code
			if m, ok := httpErr.Message.(string); ok {
				message = m
			}
		case errors.Is(err, domain.ErrNotFound):
			status, message = http.StatusNotFound, "not found"
		case errors.Is(err, domain.ErrForbidden):
			status, message = http.StatusForbidden, authoredOr(err, "forbidden")
		case errors.Is(err, domain.ErrUnauthorized):
			status, message = http.StatusUnauthorized, "unauthorized"
		case errors.Is(err, domain.ErrConflict):
			status, message = http.StatusConflict, authoredOr(err, "conflict")
		case errors.Is(err, domain.ErrHomeBoard):
			status, message = http.StatusBadRequest, domain.ErrHomeBoard.Error()
		case errors.Is(err, domain.ErrValidation):
			status, message = http.StatusBadRequest, err.Error()
		case errors.Is(err, domain.ErrUnavailable):
			status, message = http.StatusNotImplemented, authoredOr(err, domain.ErrUnavailable.Error())
		default:
			log.Error("unhandled error", zap.Error(err), zap.String("path", c.Request().URL.Path))
		}
		_ = c.JSON(status, map[string]any{"error": map[string]any{
			"code": status, "message": message,
		}})
	}
}
