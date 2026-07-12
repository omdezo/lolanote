// Package http adapts the services to Echo: routing, auth middleware,
// error mapping, and the WebSocket upgrade endpoint.
package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

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
	e.Use(requestLogger(log))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: cfg.CORSOriginList(),
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{echo.HeaderAuthorization, echo.HeaderContentType, "X-Share-Token", "X-Client-Id"},
	}))
	e.Use(echomw.BodyLimit("64M")) // local-driver uploads flow through the API

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
// Tokens arrive via Authorization header or, for WebSocket/anchor contexts,
// a ?token= query parameter.
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
			c.Set(principalKey, p)
			return next(c)
		}
	}
}

func bearerToken(c echo.Context) string {
	if h := c.Request().Header.Get(echo.HeaderAuthorization); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return c.QueryParam("token")
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
			status, message = http.StatusForbidden, "forbidden"
		case errors.Is(err, domain.ErrUnauthorized):
			status, message = http.StatusUnauthorized, "unauthorized"
		case errors.Is(err, domain.ErrConflict):
			status, message = http.StatusConflict, "conflict"
		case errors.Is(err, domain.ErrHomeBoard):
			status, message = http.StatusBadRequest, domain.ErrHomeBoard.Error()
		case errors.Is(err, domain.ErrValidation):
			status, message = http.StatusBadRequest, err.Error()
		default:
			log.Error("unhandled error", zap.Error(err), zap.String("path", c.Request().URL.Path))
		}
		_ = c.JSON(status, map[string]any{"error": map[string]any{
			"code": status, "message": message,
		}})
	}
}
