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
		if userID := c.Get(UserIDHeader); userID != "" {
			span.SetAttributes(attribute.String("user.id", userID))
		}
		if orgID := c.Get(OrganizationIDHeader); orgID != "" {
			span.SetAttributes(attribute.String("organization.id", orgID))
		}
		if membershipID := c.Get(MembershipIDHeader); membershipID != "" {
			span.SetAttributes(attribute.String("membership.id", membershipID))
		}
		if status >= 500 {
			kind := errors.KindInternal.String()
			if apiErr, ok := err.(*errors.ApiError); ok && apiErr.Kind != "" {
				kind = apiErr.Kind.String()
			}
			span.SetAttributes(attribute.String("error.kind", kind))
			span.SetStatus(codes.Error, "request failed")
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
	if userID := c.Get(UserIDHeader); userID != "" {
		ctx = ctx.Str("user_id", userID)
	}
	if orgID := c.Get(OrganizationIDHeader); orgID != "" {
		ctx = ctx.Str("organization_id", orgID)
	}
	if membershipID := c.Get(MembershipIDHeader); membershipID != "" {
		ctx = ctx.Str("membership_id", membershipID)
	}

	return ctx.Logger()
}
