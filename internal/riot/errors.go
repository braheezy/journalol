package riot

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidArgument is returned when a request cannot safely be constructed.
var ErrInvalidArgument = errors.New("invalid Riot API argument")

// ErrorKind is a stable, body-independent classification of a Riot API
// failure. Riot does not guarantee a JSON schema for non-success responses.
type ErrorKind string

const (
	ErrorBadRequest       ErrorKind = "bad_request"
	ErrorUnauthorized     ErrorKind = "unauthorized"
	ErrorForbidden        ErrorKind = "forbidden"
	ErrorNotFound         ErrorKind = "not_found"
	ErrorRateLimited      ErrorKind = "rate_limited"
	ErrorServer           ErrorKind = "server_error"
	ErrorHTTP             ErrorKind = "http_error"
	ErrorNetwork          ErrorKind = "network_error"
	ErrorCanceled         ErrorKind = "canceled"
	ErrorMalformedJSON    ErrorKind = "malformed_json"
	ErrorResponseTooLarge ErrorKind = "response_too_large"
)

// APIError is a sanitized Riot API failure. It intentionally contains neither
// the request URL nor Riot's response body, because both can contain user data
// or untrusted text. RetryAfter is populated from Riot's Retry-After header.
type APIError struct {
	Operation  string
	Kind       ErrorKind
	StatusCode int
	RetryAfter time.Duration
	Retryable  bool
	cause      error
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}

	message := "Riot API request failed"
	switch e.Kind {
	case ErrorBadRequest:
		message = "Riot rejected the request"
	case ErrorUnauthorized, ErrorForbidden:
		message = "Riot API credentials or routing need attention"
	case ErrorNotFound:
		message = "Riot data was not found"
	case ErrorRateLimited:
		message = "Riot API rate limit reached"
	case ErrorServer:
		message = "Riot API is temporarily unavailable"
	case ErrorNetwork:
		message = "Riot API network request failed"
	case ErrorCanceled:
		message = "Riot API request canceled"
	case ErrorMalformedJSON:
		message = "Riot API returned malformed JSON"
	case ErrorResponseTooLarge:
		message = "Riot API response exceeded the safety limit"
	}

	prefix := "riot"
	if e.Operation != "" {
		prefix += " " + e.Operation
	}
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Kind == ErrorRateLimited && e.RetryAfter > 0 {
		message += fmt.Sprintf("; retry after %s", e.RetryAfter.Round(time.Second))
	}
	return prefix + ": " + message
}

// Unwrap preserves context cancellation/deadline matching without including a
// transport error's URL in the user-facing error text.
func (e *APIError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func errorKindForStatus(statusCode int) ErrorKind {
	switch statusCode {
	case 400:
		return ErrorBadRequest
	case 401:
		return ErrorUnauthorized
	case 403:
		return ErrorForbidden
	case 404:
		return ErrorNotFound
	case 429:
		return ErrorRateLimited
	default:
		if statusCode >= 500 && statusCode <= 599 {
			return ErrorServer
		}
		return ErrorHTTP
	}
}
