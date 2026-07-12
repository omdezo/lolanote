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
)
