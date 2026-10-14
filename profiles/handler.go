package profiles

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/telemetry"
)

type Handler struct {
	store *Store
	telem *telemetry.Telemetry
}

func NewHandler(store *Store, telem *telemetry.Telemetry) *Handler {
	return &Handler{store: store, telem: telem}
}

type upsertBody struct {
	DisplayName string  `json:"display_name"`
	Bio         *string `json:"bio"`
}

func (h *Handler) Get(c fiber.Ctx) error {
	userID := c.Params("user_id")
	p, err := h.store.FindByUserID(c.Context(), userID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		p = &Profile{
			UserID:      userID,
			DisplayName: "Anonymous",
			Bio:         "",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := h.store.Insert(c.Context(), p); err != nil {
			return errors.InternalError()
		}
	}
	return c.JSON(p)
}

func (h *Handler) Upsert(c fiber.Ctx) error {
	userID := c.Params("user_id")
	var body upsertBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.ValidationError("Request validation failed", nil)
	}
	displayName := strings.TrimSpace(body.DisplayName)
	if displayName == "" || len(displayName) > 255 {
		return errors.ValidationError("display_name is required (max 255)", map[string]any{
			"fields": []map[string]string{{"path": "display_name", "message": "required, max 255"}},
		})
	}
	bio := ""
	if body.Bio != nil {
		if len(*body.Bio) > 2000 {
			return errors.ValidationError("bio max 2000", nil)
		}
		bio = *body.Bio
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	existing, err := h.store.FindByUserID(c.Context(), userID)
	if err != nil {
		return errors.InternalError()
	}
	if existing != nil {
		before := *existing
		existing.DisplayName = displayName
		if body.Bio != nil {
			existing.Bio = bio
		}
		existing.UpdatedAt = now
		if err := h.store.Update(c.Context(), existing); err != nil {
			return errors.InternalError()
		}
		changes := audit.Details{}
		changes.Changed("display_name", before.DisplayName, existing.DisplayName)
		changes.Changed("bio", before.Bio, existing.Bio)
		audit.Set(c, changes)
		return c.JSON(existing)
	}
	p := &Profile{
		UserID:      userID,
		DisplayName: displayName,
		Bio:         bio,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.store.Insert(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	// A new profile: each field changed from nothing. The profile's id is the user's, which
	// the path already carries, so there is no created id.
	changes := audit.Details{"display_name": audit.Change{From: nil, To: p.DisplayName}}
	if p.Bio != "" {
		changes["bio"] = audit.Change{From: nil, To: p.Bio}
	}
	audit.Set(c, changes)
	return c.JSON(p)
}
