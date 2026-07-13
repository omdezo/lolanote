package domain

import "errors"

// Sentinel errors: services return these; the HTTP layer maps them to status
// codes in one place.
var (
	ErrNotFound     = errors.New("not found")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("conflict")
	ErrValidation   = errors.New("validation failed")
	ErrHomeBoard    = errors.New("the home board cannot be shared, moved, or deleted")
	// ErrUnavailable marks features that need server-side configuration the
	// deployment lacks (e.g. password changes without a Keycloak admin client).
	ErrUnavailable = errors.New("feature not available on this server")
)
