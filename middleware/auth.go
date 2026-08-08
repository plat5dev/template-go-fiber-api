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
	MembershipIDHeader   = "X-Membership-Id"

	UserIDKey         = "user_id"
	OrganizationIDKey = "organization_id"
	MembershipIDKey   = "membership_id"
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

// RequireOrg validates org + membership headers (gateway-injected). Missing → INTERNAL_ERROR.
func RequireOrg() fiber.Handler {
	return func(c fiber.Ctx) error {
		orgID := c.Get(OrganizationIDHeader)
		membershipID := c.Get(MembershipIDHeader)
		if orgID == "" || membershipID == "" {
			span := trace.SpanFromContext(c.Context())
			span.SetAttributes(attribute.String("error.kind", errors.KindInternal.String()))
			return errors.InternalError()
		}
		c.Locals(OrganizationIDKey, orgID)
		c.Locals(MembershipIDKey, membershipID)
		span := trace.SpanFromContext(c.Context())
		span.SetAttributes(
			attribute.String("organization.id", orgID),
			attribute.String("membership.id", membershipID),
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

func GetMembershipID(c fiber.Ctx) string {
	if v, ok := c.Locals(MembershipIDKey).(string); ok {
		return v
	}
	return ""
}
