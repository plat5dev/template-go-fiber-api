// Package plat5 is how the API speaks Plat5: the error envelope (plat5
// docs/reference/errors.md), request bodies, list parameters (docs/reference/lists.md),
// ids and times, and audit details (docs/concepts/audit.md#what-a-service-adds).
package plat5

import (
	"errors"

	"github.com/gofiber/fiber/v3"
)

// Error is an answer in Plat5's error envelope. Each fallback sentence is plat5
// docs/reference/error-copy.md's.
type Error struct {
	Status  int
	Type    string // invalid_request_error or api_error
	Code    string
	Message string
	Details any // nil is null

	errorType string // a 5xx's error_type
	cause     error  // a 5xx's cause, logged, never answered
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// ErrorType is a 5xx's error_type, for its log line and span.
func (e *Error) ErrorType() string { return e.errorType }

// FallbackValidation is a 422's sentence when nothing more specific applies.
const FallbackValidation = "That doesn't look right."

// InvalidRequest is a 400: a request malformed before its values are read, such as a body
// not sent as JSON, or a create without X-Plat5-Member-Id.
func InvalidRequest() *Error {
	return &Error{Status: fiber.StatusBadRequest, Type: "invalid_request_error", Code: "INVALID_REQUEST", Message: "Malformed request."}
}

// FieldError is one wrong value in a 422, at its path.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Validation is a 422 naming each wrong field; its message is the first one's.
func Validation(fields ...FieldError) *Error {
	return &Error{
		Status: fiber.StatusUnprocessableEntity, Type: "invalid_request_error", Code: "VALIDATION_ERROR",
		Message: fields[0].Message, Details: map[string]any{"fields": fields},
	}
}

// Invalid is a 422 for the field at path; an empty message is the fallback sentence.
func Invalid(path, message string) *Error {
	if message == "" {
		message = FallbackValidation
	}
	return Validation(FieldError{path, message})
}

// FieldErrors gathers a body's wrong fields, in the order they're checked.
type FieldErrors []FieldError

// Add names path as wrong; an empty message is the fallback sentence.
func (f *FieldErrors) Add(path, message string) {
	if message == "" {
		message = FallbackValidation
	}
	*f = append(*f, FieldError{path, message})
}

// Err is the 422 naming them, or nil when there are none.
func (f FieldErrors) Err() error {
	if len(f) == 0 {
		return nil
	}
	return Validation(f...)
}

// NotFound is a 404 for the resource with id (nil is null).
func NotFound(resource string, id any) *Error {
	return &Error{
		Status: fiber.StatusNotFound, Type: "invalid_request_error", Code: "NOT_FOUND", Message: "Resource not found.",
		Details: struct {
			Resource string `json:"resource"`
			ID       any    `json:"id"`
		}{resource, id},
	}
}

// Internal is a 500 for an unexpected cause. errorType is the log's error_type.
func Internal(errorType string, cause error) *Error {
	return &Error{
		Status: fiber.StatusInternalServerError, Type: "api_error", Code: "INTERNAL_ERROR",
		Message: "An unexpected error occurred.", errorType: errorType, cause: cause,
	}
}

// DB is the 500 for a database that failed.
func DB(cause error) *Error { return Internal("db", cause) }

// Unavailable is a 503: something the answer needs didn't answer, such as identity.
func Unavailable(cause error) *Error {
	return &Error{
		Status: fiber.StatusServiceUnavailable, Type: "api_error", Code: "SERVICE_UNAVAILABLE",
		Message: "Service temporarily unavailable.", errorType: "network", cause: cause,
	}
}

// envelope is the answer's body, its keys in this order.
type envelope struct {
	Error struct {
		Type      string  `json:"type"`
		Code      string  `json:"code"`
		Message   string  `json:"message"`
		RequestID *string `json:"request_id"`
		Details   any     `json:"details"`
	} `json:"error"`
}

// ErrorHandler answers every error in the envelope, with the request's X-Request-ID (null
// without one). An unknown route, or a known path in a method it doesn't serve, is the
// 404 NOT_FOUND; anything else that isn't an *Error is the 500, never its own text.
func ErrorHandler(c fiber.Ctx, err error) error {
	var e *Error
	var fe *fiber.Error
	switch {
	case errors.As(err, &e):
	case errors.As(err, &fe) && (fe.Code == fiber.StatusNotFound || fe.Code == fiber.StatusMethodNotAllowed):
		c.Response().Header.Del(fiber.HeaderAllow)
		e = NotFound("resource", nil)
	default:
		e = Internal("internal", err)
	}
	var body envelope
	body.Error.Type, body.Error.Code, body.Error.Message, body.Error.Details = e.Type, e.Code, e.Message, e.Details
	if id := c.Get("X-Request-ID"); id != "" {
		body.Error.RequestID = &id
	}
	return c.Status(e.Status).JSON(body)
}
