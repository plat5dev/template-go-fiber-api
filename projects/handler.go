package projects

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"

	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/pagination"
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
	orgID := c.Params("organization_id")
	page, err := pagination.Parse(c)
	if err != nil {
		return err
	}
	list, more, err := h.store.ListByOrg(c.Context(), orgID, page)
	if err != nil {
		return errors.InternalError()
	}
	return c.JSON(fiber.Map{"projects": list, "has_more": more})
}

func (h *Handler) Create(c fiber.Ctx) error {
	orgID := c.Params("organization_id")
	var body createBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.FieldError("body", "")
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return errors.FieldError("name", "Name is required.")
	}
	if len(name) > 255 {
		return errors.FieldError("name", "Name is too long.")
	}
	desc := ""
	if body.Description != nil {
		if len(*body.Description) > 2000 {
			return errors.FieldError("description", "Description is too long.")
		}
		desc = *body.Description
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	p := &Project{
		ID:             ulid.Make().String(),
		OrganizationID: orgID,
		Name:           name,
		Description:    desc,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.store.Insert(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	audit.Set(c, audit.Details{"project_id": p.ID, "name": p.Name})
	audit.SetCreated(c, p.ID)
	return c.Status(fiber.StatusCreated).JSON(p)
}

func (h *Handler) Get(c fiber.Ctx) error {
	orgID := c.Params("organization_id")
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
	orgID := c.Params("organization_id")
	projectID := c.Params("project_id")
	var body updateBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.FieldError("body", "")
	}
	p, err := h.store.FindInOrg(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		return errors.NotFoundError("project", projectID)
	}
	before := *p
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			return errors.FieldError("name", "Name is required.")
		}
		if len(name) > 255 {
			return errors.FieldError("name", "Name is too long.")
		}
		p.Name = name
	}
	if body.Description != nil {
		if len(*body.Description) > 2000 {
			return errors.FieldError("description", "Description is too long.")
		}
		p.Description = *body.Description
	}
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.store.Update(c.Context(), p); err != nil {
		return errors.InternalError()
	}
	changes := audit.Details{}
	changes.Changed("name", before.Name, p.Name)
	changes.Changed("description", before.Description, p.Description)
	audit.Set(c, changes)
	return c.JSON(p)
}

func (h *Handler) Delete(c fiber.Ctx) error {
	orgID := c.Params("organization_id")
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
	// No audit details: the route and the project id in its path say what happened.
	return c.SendStatus(fiber.StatusNoContent)
}
