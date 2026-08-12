package middleware

import (
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/example/go-fiber-api/errors"
)

const (
	UserIDHeader         = "X-User-Id"
	OrganizationIDHeader = "X-Organization-Id"
	MemberIDHeader       = "X-Member-Id"

	UserIDKey         = "user_id"
	OrganizationIDKey = "organization_id"
	MemberIDKey       = "member_id"
)

// RequireUserID validates X-User-Id (gateway-injected). Missing → INTERNAL_ERROR.
func RequireUserID() fiber.Handler {
	return func(c fiber.Ctx) error {
		userID := c.Get(UserIDHeader)
		if userID == "" {
			span := trace.SpanFromContext(c.Context())
			span.SetAttributes(attribute.String("error.kind", errors.KindInternal.String()))
			return errors.InternalError()
		}
		c.Locals(UserIDKey, userID)
		span := trace.SpanFromContext(c.Context())
		span.SetAttributes(attribute.String("user.id", userID))
		return c.Next()
	}
}

// RequireOrg validates org + member headers (gateway-injected). Missing → INTERNAL_ERROR.
func RequireOrg() fiber.Handler {
	return func(c fiber.Ctx) error {
		orgID := c.Get(OrganizationIDHeader)
		memberID := c.Get(MemberIDHeader)
		if orgID == "" || memberID == "" {
			span := trace.SpanFromContext(c.Context())
			span.SetAttributes(attribute.String("error.kind", errors.KindInternal.String()))
			return errors.InternalError()
		}
		c.Locals(OrganizationIDKey, orgID)
		c.Locals(MemberIDKey, memberID)
		span := trace.SpanFromContext(c.Context())
		span.SetAttributes(
			attribute.String("organization.id", orgID),
			attribute.String("member.id", memberID),
		)
		return c.Next()
	}
}

func GetUserID(c fiber.Ctx) string {
	if v, ok := c.Locals(UserIDKey).(string); ok {
		return v
	}
	return ""
}

func GetOrganizationID(c fiber.Ctx) string {
	if v, ok := c.Locals(OrganizationIDKey).(string); ok {
		return v
	}
	return ""
}

func GetMemberID(c fiber.Ctx) string {
	if v, ok := c.Locals(MemberIDKey).(string); ok {
		return v
	}
	return ""
}
