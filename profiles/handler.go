package profiles

import (
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

// No name here: the person's name is identity's (GET /user), shown in every org.
type upsertBody struct {
	Bio *string `json:"bio"`
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
			UserID:    userID,
			Bio:       "",
			CreatedAt: now,
			UpdatedAt: now,
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
		return errors.FieldError("body", "")
	}
	if body.Bio == nil {
		return errors.FieldError("bio", "Bio is required.")
	}
	if len(*body.Bio) > 2000 {
		return errors.FieldError("bio", "Bio is too long.")
	}
	bio := *body.Bio

	now := time.Now().UTC().Format(time.RFC3339Nano)
	existing, err := h.store.FindByUserID(c.Context(), userID)
	if err != nil {
		return errors.InternalError()
	}
	if existing != nil {
		before := *existing
		existing.Bio = bio
		existing.UpdatedAt = now
		if err := h.store.Update(c.Context(), existing); err != nil {
			return errors.InternalError()
		}
		changes := audit.Details{}
		changes.Changed("bio", before.Bio, existing.Bio)
		audit.Set(c, changes)
		return c.JSON(existing)
	}
	p := &Profile{
		UserID:    userID,
		Bio:       bio,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.store.Insert(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	// A new profile: each field changed from nothing. The profile's id is the user's, which
	// the path already carries, so there is no created id.
	audit.Set(c, audit.Details{"bio": audit.Change{From: nil, To: p.Bio}})
	return c.JSON(p)
}
