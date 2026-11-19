package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"
)

const (
	fallbackValidation = "That doesn't look right."
	defaultPageLimit   = 50
	maxPageLimit       = 100

	auditDetailsHeader = "X-Plat5-Audit-Details"
	auditCreatedHeader = "X-Plat5-Audit-Created"
	auditMaxBytes      = 4096
)

// apiError renders in Plat5's standard error envelope.
type apiError struct {
	Status    int
	Type      string
	Code      string
	Message   string
	Details   any
	errorType string
	cause     error
}

func (e *apiError) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

func (e *apiError) Unwrap() error { return e.cause }

type fieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type fieldErrors []fieldError

func (f *fieldErrors) add(path, message string) {
	if message == "" {
		message = fallbackValidation
	}
	*f = append(*f, fieldError{Path: path, Message: message})
}

func (f fieldErrors) err() error {
	if len(f) == 0 {
		return nil
	}
	return errValidation(f...)
}

func errInvalidRequest() *apiError {
	return &apiError{
		Status:  fiber.StatusBadRequest,
		Type:    "invalid_request_error",
		Code:    "INVALID_REQUEST",
		Message: "Malformed request.",
	}
}

func errValidation(fields ...fieldError) *apiError {
	msg := fallbackValidation
	if len(fields) > 0 && fields[0].Message != "" {
		msg = fields[0].Message
	}
	if fields == nil {
		fields = []fieldError{}
	}
	return &apiError{
		Status:  fiber.StatusUnprocessableEntity,
		Type:    "invalid_request_error",
		Code:    "VALIDATION_ERROR",
		Message: msg,
		Details: map[string]any{"fields": fields},
	}
}

func errInvalid(path, message string) *apiError {
	if message == "" {
		message = fallbackValidation
	}
	return errValidation(fieldError{Path: path, Message: message})
}

func errNotFound(resource string, id any) *apiError {
	return &apiError{
		Status:  fiber.StatusNotFound,
		Type:    "invalid_request_error",
		Code:    "NOT_FOUND",
		Message: "Resource not found.",
		Details: struct {
			Resource string `json:"resource"`
			ID       any    `json:"id"`
		}{resource, id},
	}
}

func errInternal(errorType string, cause error) *apiError {
	return &apiError{
		Status:    fiber.StatusInternalServerError,
		Type:      "api_error",
		Code:      "INTERNAL_ERROR",
		Message:   "An unexpected error occurred.",
		errorType: errorType,
		cause:     cause,
	}
}

func errDB(cause error) *apiError { return errInternal("db", cause) }

func errUnavailable(cause error) *apiError {
	return &apiError{
		Status:    fiber.StatusServiceUnavailable,
		Type:      "api_error",
		Code:      "SERVICE_UNAVAILABLE",
		Message:   "Service temporarily unavailable.",
		errorType: "network",
		cause:     cause,
	}
}

type errorEnvelope struct {
	Error struct {
		Type      string  `json:"type"`
		Code      string  `json:"code"`
		Message   string  `json:"message"`
		RequestID *string `json:"request_id"`
		Details   any     `json:"details"`
	} `json:"error"`
}

func errorHandler(c fiber.Ctx, err error) error {
	var e *apiError
	var fe *fiber.Error
	switch {
	case errors.As(err, &e):
	case errors.As(err, &fe) && fe.Code == fiber.StatusMethodNotAllowed:
		return fiber.DefaultErrorHandler(c, err)
	case errors.As(err, &fe) && fe.Code == fiber.StatusNotFound:
		e = errNotFound("resource", nil)
	default:
		e = errInternal("internal", err)
	}

	var body errorEnvelope
	body.Error.Type = e.Type
	body.Error.Code = e.Code
	body.Error.Message = e.Message
	body.Error.Details = e.Details
	if id := c.Get("X-Request-ID"); id != "" {
		body.Error.RequestID = &id
	}
	return c.Status(e.Status).JSON(body)
}

// optional distinguishes an omitted JSON field from an explicit null or value on PATCH.
type optional[T any] struct {
	Set     bool
	Invalid bool
	Value   *T
}

func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(data, []byte("null")) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		o.Invalid = true
		return nil
	}
	o.Value = &v
	return nil
}

// decodeJSON validates Content-Type and decodes a single JSON object into dst.
func decodeJSON(c fiber.Ctx, dst any) error {
	mediaType, _, _ := strings.Cut(c.Get(fiber.HeaderContentType), ";")
	if !strings.EqualFold(strings.TrimSpace(mediaType), fiber.MIMEApplicationJSON) {
		return errInvalidRequest()
	}
	body := bytes.TrimSpace(c.Body())
	if len(body) == 0 {
		return errInvalidRequest()
	}
	if body[0] != '{' {
		if json.Valid(body) {
			return errInvalid("body", "")
		}
		return errInvalidRequest()
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var ute *json.UnmarshalTypeError
		if errors.As(err, &ute) {
			path := "body"
			if ute.Field != "" {
				path = jsonFieldName(dst, ute.Field)
			}
			return errInvalid(path, "")
		}
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			name = strings.Trim(name, `"`)
			return errInvalid(name, name+" isn't a field.")
		}
		return errInvalidRequest()
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errInvalidRequest()
	}
	return nil
}

func jsonFieldName(dst any, structField string) string {
	t := reflect.TypeOf(dst)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		if f, ok := t.FieldByName(structField); ok {
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" {
				return tag
			}
		}
	}
	return structField
}

// IDs and timestamps.

func newID() string { return strings.ToLower(ulid.Make().String()) }

func isID(s string) bool {
	_, err := ulid.ParseStrict(s)
	return err == nil && s == strings.ToLower(s)
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// List pagination.

type page struct {
	limit         int
	startingAfter string
}

func readPage(c fiber.Ctx) (page, error) {
	p := page{limit: defaultPageLimit}
	var bad fieldErrors
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			bad.add("limit", "")
		} else {
			p.limit = min(n, maxPageLimit)
		}
	}
	if raw := c.Query("starting_after"); raw != "" {
		if !isID(raw) {
			bad.add("starting_after", "")
		} else {
			p.startingAfter = raw
		}
	}
	return p, bad.err()
}

func trimPage[T any](rows []T, p page) ([]T, bool) {
	if len(rows) > p.limit {
		return rows[:p.limit], true
	}
	return rows, false
}

// Audit response headers.

type auditDetails map[string]any

type auditChange struct {
	From any `json:"from"`
	To   any `json:"to"`
}

func (d auditDetails) changed(field, from, to string) {
	if from != to {
		d[field] = auditChange{From: from, To: to}
	}
}

func (d auditDetails) changedNullable(field string, from, to *string) {
	if (from == nil) != (to == nil) || (from != nil && *from != *to) {
		d[field] = auditChange{From: from, To: to}
	}
}

func setAuditDetails(c fiber.Ctx, d auditDetails) {
	if len(d) == 0 {
		return
	}
	encoded, err := encodeAuditDetails(d)
	if err != nil {
		slog.WarnContext(c.Context(), "audit details not sent", "request_id", c.Get("X-Request-ID"), "error_message", err.Error())
		return
	}
	c.Set(auditDetailsHeader, encoded)
}

func setAuditCreated(c fiber.Ctx, id string) { c.Set(auditCreatedHeader, id) }

// encodeAuditDetails encodes d as a single visible-ASCII JSON object (<= 4096 bytes).
// HTML characters (<, >, &) stay readable; only control and non-ASCII characters are \u-escaped.
func encodeAuditDetails(d auditDetails) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return "", err
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range string(raw) {
		switch {
		case r >= 0x20 && r < utf8.RuneSelf && r != 0x7f:
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	if b.Len() > auditMaxBytes {
		return "", fmt.Errorf("audit details are %d bytes, over %d", b.Len(), auditMaxBytes)
	}
	return b.String(), nil
}
