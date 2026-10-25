package middleware

import (
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/telemetry"
)

// HTTPSpanName is `{method} {route}` when a template exists, else `{method}`.
func HTTPSpanName(c fiber.Ctx) string {
	method := c.Method()
	if r := c.Route(); r != nil && r.Path != "" {
		return method + " " + r.Path
	}
	return method
}

type callerID struct {
	field, value string
}

// callerIDs are the ids the front door gave this request (plat5 docs/reference/telemetry.md):
// the user on a user route, the project node and the caller's member on a project route.
// Log fields use them as named; span attributes add the plat5. prefix.
func callerIDs(c fiber.Ctx) []callerID {
	var ids []callerID
	if v := c.Params("user_id"); v != "" {
		ids = append(ids, callerID{"user_id", v})
	}
	if v := c.Params("project_id"); v != "" {
		ids = append(ids, callerID{"node_id", v})
	}
	if v := c.Get("X-Plat5-Member-Id"); v != "" {
		ids = append(ids, callerID{"member_id", v})
	}
	return ids
}

// RequestLogger logs requests and records HTTP metrics.
func RequestLogger(telem *telemetry.Telemetry) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()

		status := resolveStatus(c, err)
		duration := time.Since(start)

		routePattern := "unknown"
		if r := c.Route(); r != nil && r.Path != "" {
			routePattern = r.Path
		}

		metrics.ObserveRequest(routePattern, c.Method(), status, duration)

		span := trace.SpanFromContext(c.Context())
		if requestID := c.Get("X-Request-ID"); requestID != "" {
			span.SetAttributes(attribute.String("request_id", requestID))
		}
		for _, id := range callerIDs(c) {
			span.SetAttributes(attribute.String("plat5."+id.field, id.value))
		}
		if status >= 500 {
			kind := errors.KindInternal.String()
			if apiErr, ok := err.(*errors.ApiError); ok && apiErr.Kind != "" {
				kind = apiErr.Kind.String()
			}
			span.SetAttributes(attribute.String("error.kind", kind))
			span.SetStatus(codes.Error, "")
		}

		logger := buildRequestLogger(c, telem, routePattern, status, duration)

		if err != nil {
			if apiErr, ok := err.(*errors.ApiError); ok && apiErr.Status >= 500 {
				kind := apiErr.Kind.String()
				if kind == "" {
					kind = errors.KindInternal.String()
				}
				logger.Error().
					Str("error_kind", kind).
					Str("error_message", err.Error()).
					Msg("request completed")
			} else if status >= 500 {
				logger.Error().
					Str("error_kind", errors.KindInternal.String()).
					Str("error_message", err.Error()).
					Msg("request completed")
			} else {
				logger.Warn().Msg("request completed")
			}
			return err
		}

		if status >= 500 {
			logger.Error().
				Str("error_kind", errors.KindInternal.String()).
				Str("error_message", "request failed").
				Msg("request completed")
			return nil
		}

		logger.Info().Msg("request completed")
		return nil
	}
}

func resolveStatus(c fiber.Ctx, err error) int {
	if err != nil {
		switch e := err.(type) {
		case *errors.ApiError:
			return e.Status
		case *fiber.Error:
			return e.Code
		default:
			if code := c.Response().StatusCode(); code >= 400 {
				return code
			}
			return fiber.StatusInternalServerError
		}
	}
	return c.Response().StatusCode()
}

func buildRequestLogger(c fiber.Ctx, telem *telemetry.Telemetry, route string, status int, duration time.Duration) zerolog.Logger {
	ctx := telem.LoggerWithContext(c.Context()).With().
		Str("route", route).
		Str("method", c.Method()).
		Int("status", status).
		Float64("duration_ms", float64(duration.Microseconds())/1000.0)

	if requestID := c.Get("X-Request-ID"); requestID != "" {
		ctx = ctx.Str("request_id", requestID)
	}
	for _, id := range callerIDs(c) {
		ctx = ctx.Str(id.field, id.value)
	}

	return ctx.Logger()
}
