package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// ---- account & settings (the Settings dialog surface) ----

// UpdateMe patches the caller's profile (name / email / avatar). Email and
// name changes write through to Keycloak when the admin client is configured.
func (h *Handlers) UpdateMe(c echo.Context) error {
	var patch service.ProfilePatch
	if err := c.Bind(&patch); err != nil {
		return domain.ErrValidation
	}
	u, err := h.Account.UpdateProfile(c.Request().Context(), principal(c), patch)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

// GetSettings returns the caller's effective settings.
func (h *Handlers) GetSettings(c echo.Context) error {
	s, err := h.Account.Settings(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s)
}

// UpdateSettings merge-patches the caller's settings; absent fields keep
// their current value, invalid values snap back to defaults.
func (h *Handlers) UpdateSettings(c echo.Context) error {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 64<<10))
	if err != nil || len(body) == 0 {
		return domain.ErrValidation
	}
	if !json.Valid(body) {
		return domain.ErrValidation
	}
	s, err := h.Account.UpdateSettings(c.Request().Context(), principal(c), body)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// ChangePassword verifies the current password and sets the new one.
func (h *Handlers) ChangePassword(c echo.Context) error {
	var req changePasswordRequest
	if err := c.Bind(&req); err != nil || req.CurrentPassword == "" || req.NewPassword == "" {
		return domain.ErrValidation
	}
	if err := h.Account.ChangePassword(c.Request().Context(), principal(c), req.CurrentPassword, req.NewPassword); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ExportMyData streams the caller's full data bundle (privacy tab).
func (h *Handlers) ExportMyData(c echo.Context) error {
	export, err := h.Account.ExportData(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="qomranote-export.json"`)
	return c.JSON(http.StatusOK, export)
}

type deleteAccountRequest struct {
	// Confirm must be the literal string "DELETE" — a server-side guard
	// against accidental calls, mirroring the typed confirmation in the UI.
	Confirm string `json:"confirm"`
}

// DeleteMe permanently deletes the account and everything it owns.
func (h *Handlers) DeleteMe(c echo.Context) error {
	var req deleteAccountRequest
	if err := c.Bind(&req); err != nil || req.Confirm != "DELETE" {
		return domain.ErrValidation
	}
	if err := h.Account.DeleteAccount(c.Request().Context(), principal(c)); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
