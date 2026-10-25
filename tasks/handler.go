package tasks

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

// MemberHeader is the caller's member id in the project's tree, from the front door. A
// create records it as the task's author, and answers 400 without it.
const MemberHeader = "X-Plat5-Member-Id"

type Handler struct {
	store *Store
	telem *telemetry.Telemetry
}

func NewHandler(store *Store, telem *telemetry.Telemetry) *Handler {
	return &Handler{store: store, telem: telem}
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

// Every handler scopes its lookups by the project id in the path, and checks nothing about
// the caller or the project: the front door decided who may call, on which project.

func (h *Handler) List(c fiber.Ctx) error {
	projectID := c.Params("project_id")
	page, err := pagination.Parse(c)
	if err != nil {
		return err
	}
	list, more, err := h.store.ListByProject(c.Context(), projectID, page)
	if err != nil {
		return errors.InternalError()
	}
	return c.JSON(fiber.Map{"tasks": list, "has_more": more})
}

func (h *Handler) Create(c fiber.Ctx) error {
	projectID := c.Params("project_id")
	memberID := c.Get(MemberHeader)
	if memberID == "" {
		return errors.InvalidRequestError()
	}
	var body createBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.FieldError("body", "")
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		return errors.FieldError("title", "Title is required.")
	}
	if len(title) > 255 {
		return errors.FieldError("title", "Title is too long.")
	}
	status := "todo"
	if body.Status != nil {
		if !validStatus[*body.Status] {
			return errors.FieldError("status", "Status must be todo, in progress, or done.")
		}
		status = *body.Status
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	t := &Task{
		ID:        strings.ToLower(ulid.Make().String()),
		ProjectID: projectID,
		Title:     title,
		Status:    status,
		CreatedBy: memberID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.store.Insert(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	audit.Set(c, audit.Details{"task_id": t.ID, "title": t.Title, "status": t.Status})
	audit.SetCreated(c, t.ID)
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *Handler) Get(c fiber.Ctx) error {
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	t, err := h.store.FindInProject(c.Context(), projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	return c.JSON(t)
}

func (h *Handler) Update(c fiber.Ctx) error {
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	var body updateBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.FieldError("body", "")
	}
	t, err := h.store.FindInProject(c.Context(), projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	before := *t
	if body.Title != nil {
		title := strings.TrimSpace(*body.Title)
		if title == "" {
			return errors.FieldError("title", "Title is required.")
		}
		if len(title) > 255 {
			return errors.FieldError("title", "Title is too long.")
		}
		t.Title = title
	}
	if body.Status != nil {
		if !validStatus[*body.Status] {
			return errors.FieldError("status", "Status must be todo, in progress, or done.")
		}
		t.Status = *body.Status
	}
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.store.Update(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	changes := audit.Details{}
	changes.Changed("title", before.Title, t.Title)
	changes.Changed("status", before.Status, t.Status)
	audit.Set(c, changes)
	return c.JSON(t)
}

func (h *Handler) Delete(c fiber.Ctx) error {
	projectID := c.Params("project_id")
	taskID := c.Params("task_id")
	t, err := h.store.FindInProject(c.Context(), projectID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	if err := h.store.Delete(c.Context(), projectID, taskID); err != nil {
		return errors.InternalError()
	}
	// No audit details: the route and the task id in its path say what happened.
	return c.SendStatus(fiber.StatusNoContent)
}
