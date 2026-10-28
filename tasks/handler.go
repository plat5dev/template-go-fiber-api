package tasks

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/oklog/ulid/v2"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/audit"
	"github.com/example/go-fiber-api/errors"
	"github.com/example/go-fiber-api/pagination"
	"github.com/example/go-fiber-api/telemetry"
)

const (
	// MemberHeader is the caller's member id in the environment's tree, from the front
	// door. A create records it as the task's author, and answers 400 without it.
	MemberHeader = "X-Plat5-Member-Id"
	// ProjectHeader is the environment's project id, from the front door. A write that sets
	// an assignee checks them on it, and answers 400 without it.
	ProjectHeader = "X-Plat5-Project-Id"
)

type Handler struct {
	store              *Store
	telem              *telemetry.Telemetry
	access             *access.Client
	assigneePermission string
}

// NewHandler checks each assignee with access: they must hold assigneePermission on the
// environment's project.
func NewHandler(store *Store, telem *telemetry.Telemetry, access *access.Client, assigneePermission string) *Handler {
	return &Handler{store: store, telem: telem, access: access, assigneePermission: assigneePermission}
}

// AssigneeID is raw, to tell an absent assignee_id from null: see parseAssignee.
type createBody struct {
	Title      string          `json:"title"`
	Status     *string         `json:"status"`
	AssigneeID json.RawMessage `json:"assignee_id"`
}

type updateBody struct {
	Title      *string         `json:"title"`
	Status     *string         `json:"status"`
	AssigneeID json.RawMessage `json:"assignee_id"`
}

var validStatus = map[string]bool{"todo": true, "in_progress": true, "done": true}

// parseAssignee reads a body's assignee_id: absent (set is false), null (id is nil), or a
// member id. Anything else is a 422 at assignee_id.
func parseAssignee(raw json.RawMessage) (set bool, id *string, err error) {
	if raw == nil {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return true, nil, errors.FieldError("assignee_id", "")
	}
	if _, err := ulid.ParseStrict(s); err != nil || s != strings.ToLower(s) {
		return true, nil, errors.FieldError("assignee_id", "")
	}
	return true, &s, nil
}

// checkAssignee asks identity whether the assignee holds the assignee permission on the
// environment's project. The caller may assign, but the assignee isn't the caller, so the
// front door decided nothing about them
// (plat5 docs/guides/write-a-service.md#checking-access-outside-a-request). No answer is
// no: a 503, so nothing is written.
func (h *Handler) checkAssignee(c fiber.Ctx, assigneeID string) error {
	projectID := c.Get(ProjectHeader)
	if projectID == "" {
		return errors.InvalidRequestError()
	}
	allowed, err := h.access.Evaluate(c.Context(), c.Get("X-Request-ID"), assigneeID, "project", projectID, h.assigneePermission)
	if err != nil {
		return errors.ServiceUnavailableError()
	}
	if !allowed {
		return errors.FieldError("assignee_id", "That person can't see this project.")
	}
	return nil
}

// Every handler scopes its lookups by the environment id in the path, and checks nothing
// about the caller or the environment: the front door decided who may call, on which
// environment. A write that sets an assignee checks the assignee, in this order: the body,
// the task (on an update), the project header, then identity's answer, before writing.

func (h *Handler) List(c fiber.Ctx) error {
	environmentID := c.Params("environment_id")
	page, err := pagination.Parse(c)
	if err != nil {
		return err
	}
	list, more, err := h.store.ListByEnvironment(c.Context(), environmentID, page)
	if err != nil {
		return errors.InternalError()
	}
	return c.JSON(fiber.Map{"tasks": list, "has_more": more})
}

func (h *Handler) Create(c fiber.Ctx) error {
	environmentID := c.Params("environment_id")
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
	_, assigneeID, err := parseAssignee(body.AssigneeID)
	if err != nil {
		return err
	}
	if assigneeID != nil {
		if err := h.checkAssignee(c, *assigneeID); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	t := &Task{
		ID:            strings.ToLower(ulid.Make().String()),
		EnvironmentID: environmentID,
		Title:         title,
		Status:        status,
		AssigneeID:    assigneeID,
		CreatedBy:     memberID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := h.store.Insert(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	audit.Set(c, audit.Details{"task_id": t.ID, "title": t.Title, "status": t.Status, "assignee_id": t.AssigneeID})
	audit.SetCreated(c, t.ID)
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *Handler) Get(c fiber.Ctx) error {
	environmentID := c.Params("environment_id")
	taskID := c.Params("task_id")
	t, err := h.store.FindInEnvironment(c.Context(), environmentID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	return c.JSON(t)
}

func (h *Handler) Update(c fiber.Ctx) error {
	environmentID := c.Params("environment_id")
	taskID := c.Params("task_id")
	var body updateBody
	if err := c.Bind().Body(&body); err != nil {
		return errors.FieldError("body", "")
	}
	var title string
	if body.Title != nil {
		title = strings.TrimSpace(*body.Title)
		if title == "" {
			return errors.FieldError("title", "Title is required.")
		}
		if len(title) > 255 {
			return errors.FieldError("title", "Title is too long.")
		}
	}
	if body.Status != nil && !validStatus[*body.Status] {
		return errors.FieldError("status", "Status must be todo, in progress, or done.")
	}
	assigneeSet, assigneeID, err := parseAssignee(body.AssigneeID)
	if err != nil {
		return err
	}
	t, err := h.store.FindInEnvironment(c.Context(), environmentID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	// null unassigns, with nothing to check.
	if assigneeID != nil {
		if err := h.checkAssignee(c, *assigneeID); err != nil {
			return err
		}
	}
	before := *t
	if body.Title != nil {
		t.Title = title
	}
	if body.Status != nil {
		t.Status = *body.Status
	}
	if assigneeSet {
		t.AssigneeID = assigneeID
	}
	t.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.store.Update(c.Context(), t); err != nil {
		return errors.InternalError()
	}
	changes := audit.Details{}
	changes.Changed("title", before.Title, t.Title)
	changes.Changed("status", before.Status, t.Status)
	changes.ChangedNullable("assignee_id", before.AssigneeID, t.AssigneeID)
	audit.Set(c, changes)
	return c.JSON(t)
}

func (h *Handler) Delete(c fiber.Ctx) error {
	environmentID := c.Params("environment_id")
	taskID := c.Params("task_id")
	t, err := h.store.FindInEnvironment(c.Context(), environmentID, taskID)
	if err != nil {
		return errors.InternalError()
	}
	if t == nil {
		return errors.NotFoundError("task", taskID)
	}
	if err := h.store.Delete(c.Context(), environmentID, taskID); err != nil {
		return errors.InternalError()
	}
	// No audit details: the route and the task id in its path say what happened.
	return c.SendStatus(fiber.StatusNoContent)
}
