package projects

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"

	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/middleware"
	"github.com/example/go-fiber-api/telemetry"
)

type Handler struct {
	store *Store
	telem *telemetry.Telemetry
}

func NewHandler(store *Store, telem *telemetry.Telemetry) *Handler {
	return &Handler{store: store, telem: telem}
}

type createBody struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type updateBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) List(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	list, err := h.store.ListByOrg(c.Context(), orgID)
	if err != nil {
		return errors.InternalError()
	}
	return c.JSON(fiber.Map{"projects": list})
}

func (h *Handler) Create(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	membershipID := middleware.GetMembershipID(c)
	var body createBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.ValidationError("Request validation failed", nil)
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > 255 {
		return errors.ValidationError("name is required (max 255)", nil)
	}
	desc := ""
	if body.Description != nil {
		if len(*body.Description) > 2000 {
			return errors.ValidationError("description max 2000", nil)
		}
		desc = *body.Description
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	p := &Project{
		ID:                    ulid.Make().String(),
		OrganizationID:        orgID,
		Name:                  name,
		Description:           desc,
		CreatedByMembershipID: membershipID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := h.store.Insert(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	return c.Status(fiber.StatusCreated).JSON(p)
}

func (h *Handler) Get(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	p, err := h.store.FindInOrg(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		return errors.NotFoundError("project", projectID)
	}
	return c.JSON(p)
}

func (h *Handler) Update(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	var body updateBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.ValidationError("Request validation failed", nil)
	}
	p, err := h.store.FindInOrg(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		return errors.NotFoundError("project", projectID)
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" || len(name) > 255 {
			return errors.ValidationError("name max 255", nil)
		}
		p.Name = name
	}
	if body.Description != nil {
		if len(*body.Description) > 2000 {
			return errors.ValidationError("description max 2000", nil)
		}
		p.Description = *body.Description
	}
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.store.Update(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	return c.JSON(p)
}

func (h *Handler) Delete(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	p, err := h.store.FindInOrg(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		return errors.NotFoundError("project", projectID)
	}
	if err := h.store.Delete(c.Context(), orgID, projectID); err != nil {
		return errors.InternalError()
	}
	return c.SendStatus(fiber.StatusNoContent)
}
