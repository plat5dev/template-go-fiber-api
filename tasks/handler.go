package tasks

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"

	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/middleware"
	"github.com/example/go-fiber-api/projects"
	"github.com/example/go-fiber-api/telemetry"
)

type Handler struct {
	store        *Store
	projectStore *projects.Store
	telem        *telemetry.Telemetry
}

func NewHandler(store *Store, projectStore *projects.Store, telem *telemetry.Telemetry) *Handler {
	return &Handler{store: store, projectStore: projectStore, telem: telem}
}

type createBody struct {
	Title  string  `json:"title"`
	Status *string `json:"status"`
}

type updateBody struct {
	Title  *string `json:"title"`
	Status *string `json:"status"`
}

var validStatus = map[string]bool{"todo": true, "in_progress": true, "done": true}

func (h *Handler) requireProject(c fiber.Ctx, orgID, projectID string) error {
	p, err := h.projectStore.FindInOrg(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	if p == nil {
		return errors.NotFoundError("project", projectID)
	}
	return nil
}

func (h *Handler) List(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	if err := h.requireProject(c, orgID, projectID); err != nil {
		return err
	}
	list, err := h.store.ListByProject(c.Context(), orgID, projectID)
	if err != nil {
		return errors.InternalError()
	}
	return c.JSON(fiber.Map{"tasks": list})
}

func (h *Handler) Create(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	memberID := middleware.GetMemberID(c)
	projectID := c.Params("project_id")
	if err := h.requireProject(c, orgID, projectID); err != nil {
		return err
	}
	var body createBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.ValidationError("Request validation failed", nil)
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len(title) > 255 {
		return errors.ValidationError("title is required (max 255)", nil)
	}
	status := "todo"
	if body.Status != nil {
		if !validStatus[*body.Status] {
			return errors.ValidationError("status must be todo|in_progress|done", nil)
		}
		status = *body.Status
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	t := &Task{
		ID:                ulid.Make().String(),
		OrganizationID:    orgID,
		ProjectID:         projectID,
		Title:             title,
		Status:            status,
		CreatedByMemberID: memberID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := h.store.Insert(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *Handler) Get(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	if err := h.requireProject(c, orgID, projectID); err != nil {
		return err
	}
	t, err := h.store.FindInProject(c.Context(), orgID, projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	return c.JSON(t)
}

func (h *Handler) Update(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	if err := h.requireProject(c, orgID, projectID); err != nil {
		return err
	}
	var body updateBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.ValidationError("Request validation failed", nil)
	}
	t, err := h.store.FindInProject(c.Context(), orgID, projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	if body.Title != nil {
		title := strings.TrimSpace(*body.Title)
		if title == "" || len(title) > 255 {
			return errors.ValidationError("title max 255", nil)
		}
		t.Title = title
	}
	if body.Status != nil {
		if !validStatus[*body.Status] {
			return errors.ValidationError("status must be todo|in_progress|done", nil)
		}
		t.Status = *body.Status
	}
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.store.Update(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	return c.JSON(t)
}

func (h *Handler) Delete(c fiber.Ctx) error {
	orgID := middleware.GetOrganizationID(c)
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	if err := h.requireProject(c, orgID, projectID); err != nil {
		return err
	}
	t, err := h.store.FindInProject(c.Context(), orgID, projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	if err := h.store.Delete(c.Context(), orgID, projectID, taskID); err != nil {
		return errors.InternalError()
	}
	return c.SendStatus(fiber.StatusNoContent)
}
