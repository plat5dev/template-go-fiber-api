package tasks

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/access"
	"github.com/example/go-fiber-api/plat5"
)

// MemberHeader is the caller's member id in the project's tree, from the front door. A
// create records it as the task's author, and answers 400 without it.
const MemberHeader = "X-Plat5-Member-Id"

// Handler serves a project's tasks. Every handler scopes its lookups by the project id in
// the path, and checks nothing about the caller or the project: the front door decided who
// may call, on which project. An assignee isn't the caller, so a write that sets one asks
// identity, after the body and (on an update) the task, before writing.
type Handler struct {
	store              *Store
	access             *access.Client
	assigneePermission string
}

// NewHandler checks each assignee with access: they must hold assigneePermission on the
// project.
func NewHandler(store *Store, access *access.Client, assigneePermission string) *Handler {
	return &Handler{store: store, access: access, assigneePermission: assigneePermission}
}

// body is a create's or an update's.
type body struct {
	Title      plat5.Field[string] `json:"title"`
	Status     plat5.Field[string] `json:"status"`
	AssigneeID plat5.Field[string] `json:"assignee_id"`
}

// maxTitle is a title's longest, in characters (code points), once trimmed.
const maxTitle = 255

// title is a title as sent, trimmed, or a wrong field.
func title(f plat5.Field[string], bad *plat5.FieldErrors) string {
	t := strings.TrimSpace(f.Value)
	switch {
	case f.Null || f.Wrong:
		bad.Add("title", "")
	case t == "":
		bad.Add("title", "Title is required.")
	case utf8.RuneCountInString(t) > maxTitle:
		bad.Add("title", "Title is too long.")
	}
	return t
}

// status is a status as sent, or a wrong field.
func status(f plat5.Field[string], bad *plat5.FieldErrors) string {
	if s := f.Value; !f.Null && !f.Wrong && (s == "todo" || s == "in_progress" || s == "done") {
		return s
	}
	bad.Add("status", "Status must be todo, in progress, or done.")
	return ""
}

// assignee is a member id, nil for null, or a wrong field.
func assignee(f plat5.Field[string], bad *plat5.FieldErrors) *string {
	switch {
	case f.Null:
		return nil
	case f.Wrong || !plat5.IsID(f.Value):
		bad.Add("assignee_id", "")
	}
	return &f.Value
}

// checkAssignee asks identity whether the assignee holds the assignee permission on the
// project in the path. No answer is no: a 503, so nothing is written.
func (h *Handler) checkAssignee(c fiber.Ctx, assigneeID *string) error {
	if assigneeID == nil {
		return nil
	}
	allowed, err := h.access.Evaluate(c.Context(), c.Get("X-Request-ID"), *assigneeID, "project", c.Params("project_id"), h.assigneePermission)
	if err != nil {
		return plat5.Unavailable(err)
	}
	if !allowed {
		return plat5.Invalid("assignee_id", "That person can't see this project.")
	}
	return nil
}

func (h *Handler) List(c fiber.Ctx) error {
	page, err := plat5.ReadPage(c)
	if err != nil {
		return err
	}
	list, more, err := h.store.List(c.Context(), c.Params("project_id"), page)
	if err != nil {
		return plat5.DB(err)
	}
	return c.JSON(fiber.Map{"tasks": list, "has_more": more})
}

func (h *Handler) Create(c fiber.Ctx) error {
	memberID := c.Get(MemberHeader)
	if memberID == "" {
		return plat5.InvalidRequest()
	}
	var b body
	if err := plat5.ReadBody(c, &b); err != nil {
		return err
	}
	var bad plat5.FieldErrors
	t := &Task{ID: plat5.NewID(), ProjectID: c.Params("project_id"), Status: "todo", CreatedBy: memberID}
	if b.Title.Set {
		t.Title = title(b.Title, &bad)
	} else {
		bad.Add("title", "Title is required.")
	}
	if b.Status.Set {
		t.Status = status(b.Status, &bad)
	}
	if b.AssigneeID.Set {
		t.AssigneeID = assignee(b.AssigneeID, &bad)
	}
	if err := bad.Err(); err != nil {
		return err
	}
	if err := h.checkAssignee(c, t.AssigneeID); err != nil {
		return err
	}
	t.CreatedAt = plat5.Timestamp(time.Now())
	t.UpdatedAt = t.CreatedAt
	if err := h.store.Insert(c.Context(), t); err != nil {
		return plat5.DB(err)
	}
	plat5.SetAuditDetails(c, plat5.Details{"task_id": t.ID, "title": t.Title, "status": t.Status, "assignee_id": t.AssigneeID})
	plat5.SetAuditCreated(c, t.ID)
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *Handler) Get(c fiber.Ctx) error {
	t, err := h.store.Find(c.Context(), c.Params("project_id"), c.Params("task_id"))
	if err != nil {
		return plat5.DB(err)
	}
	if t == nil {
		return plat5.NotFound("task", c.Params("task_id"))
	}
	return c.JSON(t)
}

// Update sets the fields sent. An absent or null title or status leaves it as it is; a
// null assignee_id unassigns.
func (h *Handler) Update(c fiber.Ctx) error {
	var b body
	if err := plat5.ReadBody(c, &b); err != nil {
		return err
	}
	var bad plat5.FieldErrors
	var newTitle, newStatus string
	var newAssignee *string
	if b.Title.Set && !b.Title.Null {
		newTitle = title(b.Title, &bad)
	}
	if b.Status.Set && !b.Status.Null {
		newStatus = status(b.Status, &bad)
	}
	if b.AssigneeID.Set {
		newAssignee = assignee(b.AssigneeID, &bad)
	}
	if err := bad.Err(); err != nil {
		return err
	}

	taskID := c.Params("task_id")
	t, err := h.store.Find(c.Context(), c.Params("project_id"), taskID)
	if err != nil {
		return plat5.DB(err)
	}
	if t == nil {
		return plat5.NotFound("task", taskID)
	}
	if err := h.checkAssignee(c, newAssignee); err != nil {
		return err
	}

	before := *t
	if newTitle != "" {
		t.Title = newTitle
	}
	if newStatus != "" {
		t.Status = newStatus
	}
	if b.AssigneeID.Set {
		t.AssigneeID = newAssignee
	}
	t.UpdatedAt = plat5.Timestamp(time.Now())
	updated, err := h.store.Update(c.Context(), t)
	if err != nil {
		return plat5.DB(err)
	}
	// Deleted since the read: the 404 the read would answer now.
	if !updated {
		return plat5.NotFound("task", taskID)
	}
	changes := plat5.Details{}
	changes.Changed("title", before.Title, t.Title)
	changes.Changed("status", before.Status, t.Status)
	changes.ChangedNullable("assignee_id", before.AssigneeID, t.AssigneeID)
	plat5.SetAuditDetails(c, changes)
	return c.JSON(t)
}

// Delete sends no audit details: the route and the task id in its path say what happened.
func (h *Handler) Delete(c fiber.Ctx) error {
	taskID := c.Params("task_id")
	deleted, err := h.store.Delete(c.Context(), c.Params("project_id"), taskID)
	if err != nil {
		return plat5.DB(err)
	}
	if !deleted {
		return plat5.NotFound("task", taskID)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
