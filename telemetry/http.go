package telemetry

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/contrib/v3/otel"
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Typed is an error that names its error_type: auth, network, db, io, internal or
// validation. A 5xx from any other error is internal.
type Typed interface {
	error
	ErrorType() string
}

// The ids the front door sends, logged as named and set on spans with the plat5. prefix
// (plat5 docs/reference/headers.md).
var callerHeaders = [...]struct{ header, field string }{
	{"X-Plat5-Project-Id", "node_id"},
	{"X-Plat5-Member-Id", "member_id"},
	{"X-Plat5-User-Id", "user_id"},
}

const errorTypeLocal = "telemetry.error_type"

// Middleware is the API's HTTP telemetry, first in the chain: the HTTP server span
// (Fiber's otel middleware: named {method} {route}, url.query redacted, Error on a 5xx
// only), then one log line and the request metrics per request. A handler's error is
// answered by the app's error handler here, so the line has the status sent.
func Middleware() []any {
	return []any{
		otel.New(
			otel.WithoutMetrics(true),
			// The trace stays this side of the gateway: none on the response.
			otel.WithResponsePropagators(propagation.NewCompositeTextMapPropagator()),
			// After the middleware's own error.type (the status code), so this one wins.
			otel.WithCustomResponseAttributes(func(c fiber.Ctx) []attribute.KeyValue {
				if errorType, ok := c.Locals(errorTypeLocal).(string); ok {
					return []attribute.KeyValue{attribute.String("error.type", errorType)}
				}
				return nil
			}),
		),
		logRequest,
	}
}

func logRequest(c fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	if err != nil {
		if herr := c.App().ErrorHandler(c, err); herr != nil {
			_ = c.SendStatus(fiber.StatusInternalServerError)
		}
	}
	status := c.Response().StatusCode()
	duration := time.Since(start)
	route := Route(c)
	observeRequest(route, c.Method(), status, duration)

	ctx := c.Context()
	span := trace.SpanFromContext(ctx)
	attrs := []slog.Attr{
		slog.String("route", route),
		slog.String("method", c.Method()),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(duration.Microseconds())/1000),
	}
	if sc := span.SpanContext(); sc.IsValid() {
		attrs = append(attrs, slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	if id := c.Get("X-Request-ID"); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
		span.SetAttributes(attribute.String("request_id", id))
	}
	for _, h := range callerHeaders {
		if id := c.Get(h.header); id != "" {
			attrs = append(attrs, slog.String(h.field, id))
			span.SetAttributes(attribute.String("plat5."+h.field, id))
		}
	}

	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
		errorType, message := "internal", "request failed"
		var typed Typed
		if errors.As(err, &typed) {
			errorType = typed.ErrorType()
		}
		if err != nil {
			message = err.Error()
		}
		c.Locals(errorTypeLocal, errorType)
		attrs = append(attrs, slog.String("error_type", errorType), slog.String("error_message", message))
	case status >= 400:
		level = slog.LevelWarn
	}
	slog.LogAttrs(ctx, level, "request completed", attrs...)
	return nil
}

// Route is the matched route's template, or unknown when no route matched (a middleware
// alone isn't a match).
func Route(c fiber.Ctx) string {
	if !c.Matched() {
		return "unknown"
	}
	return c.Route().Path
}
