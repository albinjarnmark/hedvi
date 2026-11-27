// Package apierr defines Resend-compatible error responses:
// {"statusCode": 422, "name": "validation_error", "message": "…"}.
package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status  int    `json:"statusCode"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Name, e.Message) }

func New(status int, name, format string, a ...any) *Error {
	return &Error{Status: status, Name: name, Message: fmt.Sprintf(format, a...)}
}

var (
	ErrMissingKey = New(401, "missing_api_key", "Missing API key in the authorization header. Include `Authorization: Bearer hv_...`.")
	ErrInvalidKey = New(403, "invalid_api_key", "API key is invalid or has been revoked.")
	ErrRestricted = New(401, "restricted_api_key", "This API key can only send emails.")
	ErrNotFound   = New(404, "not_found", "The requested resource does not exist.")
	ErrInternal   = New(500, "internal_server_error", "Unexpected error. The request outcome may be uncertain. Retry only with the original Idempotency-Key and payload within 24 hours. If no key was supplied or it has expired, check the email log before resending.")
)

// Write renders err; unknown errors become a generic 500 without details.
func Write(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = ErrInternal
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e)
}
