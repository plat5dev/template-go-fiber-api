package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
)

const (
	memberHeader = "X-Plat5-Member-Id"
	maxTitleLen  = 255
	taskColumns  = `id, project_id, title, status, assignee_id, created_by, created_at, updated_at`
)

type Task struct {
	ID         string  `json:"id"`
	ProjectID  string  `json:"project_id"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	AssigneeID *string `json:"assignee_id"`
	CreatedBy  string  `json:"created_by"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

type taskStore struct{ db *sql.DB }

func newTaskStore(db *sql.DB) *taskStore { return &taskStore{db: db} }

func scanTask(row interface{ Scan(...any) error }, t *Task) error {
	return row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Status, &t.AssigneeID, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
}

func (s *taskStore) list(ctx context.Context, projectID string, p page) ([]Task, bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE project_id = ? AND id > ? ORDER BY id LIMIT ?`,
		projectID, p.startingAfter, p.limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	list := []Task{}
	for rows.Next() {
		var t Task
		if err := scanTask(rows, &t); err != nil {
			return nil, false, err
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	list, more := trimPage(list, p)
	return list, more, nil
}

func (s *taskStore) find(ctx context.Context, projectID, taskID string) (*Task, error) {
	var t Task
	err := scanTask(s.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE project_id = ? AND id = ?`, projectID, taskID), &t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *taskStore) insert(ctx context.Context, t *Task) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tasks (`+taskColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Status, t.AssigneeID, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *taskStore) update(ctx context.Context, t *Task) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET title = ?, status = ?, assignee_id = ?, updated_at = ? WHERE project_id = ? AND id = ?`,
		t.Title, t.Status, t.AssigneeID, t.UpdatedAt, t.ProjectID, t.ID)
	return rowAffected(res, err)
}

func (s *taskStore) delete(ctx context.Context, projectID, taskID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = ? AND id = ?`, projectID, taskID)
	return rowAffected(res, err)
}

func deleteProjectTasks(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string) error {
	_, err := exec.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = ?`, projectID)
	return err
}

func rowAffected(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HTTP handlers.

// defaultTaskAssigneePermission is the permission a task's assignee must hold
// on the project (matches services/api.yml's read permission).
const defaultTaskAssigneePermission = "project:read"

type taskHandler struct {
	store              *taskStore
	access             *accessClient
	assigneePermission string
}

func newTaskHandler(store *taskStore, access *accessClient, assigneePermission string) *taskHandler {
	if assigneePermission == "" {
		assigneePermission = defaultTaskAssigneePermission
	}
	return &taskHandler{store: store, access: access, assigneePermission: assigneePermission}
}

type createTaskRequest struct {
	Title      *string `json:"title"`
	Status     *string `json:"status"`
	AssigneeID *string `json:"assignee_id"`
}

type patchTaskRequest struct {
	Title      *string          `json:"title"`
	Status     *string          `json:"status"`
	AssigneeID optional[string] `json:"assignee_id"`
}

func validateTitle(raw string, bad *fieldErrors) string {
	t := strings.TrimSpace(raw)
	switch {
	case t == "":
		bad.add("title", "Title is required.")
	case utf8.RuneCountInString(t) > maxTitleLen:
		bad.add("title", "Title is too long.")
	}
	return t
}

func validateStatus(s string, bad *fieldErrors) string {
	if s == "todo" || s == "in_progress" || s == "done" {
		return s
	}
	bad.add("status", "Status must be todo, in progress, or done.")
	return ""
}

func (h *taskHandler) checkAssignee(c fiber.Ctx, assigneeID *string) error {
	if assigneeID == nil {
		return nil
	}
	allowed, err := h.access.evaluate(
		c.Context(),
		c.Get("X-Request-ID"),
		*assigneeID,
		"project",
		c.Params("project_id"),
		h.assigneePermission,
	)
	if err != nil {
		return errUnavailable(err)
	}
	if !allowed {
		return errInvalid("assignee_id", "That person can't see this project.")
	}
	return nil
}

func (h *taskHandler) list(c fiber.Ctx) error {
	p, err := readPage(c)
	if err != nil {
		return err
	}
	items, more, err := h.store.list(c.Context(), c.Params("project_id"), p)
	if err != nil {
		return errDB(err)
	}
	return c.JSON(fiber.Map{"tasks": items, "has_more": more})
}

func (h *taskHandler) create(c fiber.Ctx) error {
	memberID := c.Get(memberHeader)
	if memberID == "" {
		return errInvalidRequest()
	}

	var req createTaskRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}

	var bad fieldErrors
	t := &Task{
		ID:        newID(),
		ProjectID: c.Params("project_id"),
		Status:    "todo",
		CreatedBy: memberID,
	}
	if req.Title == nil {
		bad.add("title", "Title is required.")
	} else {
		t.Title = validateTitle(*req.Title, &bad)
	}
	if req.Status != nil {
		t.Status = validateStatus(*req.Status, &bad)
	}
	if req.AssigneeID != nil {
		if !isID(*req.AssigneeID) {
			bad.add("assignee_id", "")
		} else {
			t.AssigneeID = req.AssigneeID
		}
	}
	if err := bad.err(); err != nil {
		return err
	}
	if err := h.checkAssignee(c, t.AssigneeID); err != nil {
		return err
	}

	now := timestamp(time.Now())
	t.CreatedAt = now
	t.UpdatedAt = now
	if err := h.store.insert(c.Context(), t); err != nil {
		return errDB(err)
	}
	setAuditDetails(c, auditDetails{
		"task_id":     t.ID,
		"title":       t.Title,
		"status":      t.Status,
		"assignee_id": t.AssigneeID,
	})
	setAuditCreated(c, t.ID)
	return c.Status(fiber.StatusCreated).JSON(t)
}

func (h *taskHandler) get(c fiber.Ctx) error {
	t, err := h.store.find(c.Context(), c.Params("project_id"), c.Params("task_id"))
	if err != nil {
		return errDB(err)
	}
	if t == nil {
		return errNotFound("task", c.Params("task_id"))
	}
	return c.JSON(t)
}

func (h *taskHandler) update(c fiber.Ctx) error {
	var req patchTaskRequest
	if err := decodeJSON(c, &req); err != nil {
		return err
	}

	var (
		bad       fieldErrors
		hasTitle  bool
		newTitle  string
		hasStatus bool
		newStatus string
	)
	if req.Title != nil {
		hasTitle = true
		newTitle = validateTitle(*req.Title, &bad)
	}
	if req.Status != nil {
		hasStatus = true
		newStatus = validateStatus(*req.Status, &bad)
	}
	if req.AssigneeID.Set {
		if req.AssigneeID.Invalid || (req.AssigneeID.Value != nil && !isID(*req.AssigneeID.Value)) {
			bad.add("assignee_id", "")
		}
	}
	if err := bad.err(); err != nil {
		return err
	}

	taskID := c.Params("task_id")
	t, err := h.store.find(c.Context(), c.Params("project_id"), taskID)
	if err != nil {
		return errDB(err)
	}
	if t == nil {
		return errNotFound("task", taskID)
	}
	if req.AssigneeID.Set && req.AssigneeID.Value != nil {
		if err := h.checkAssignee(c, req.AssigneeID.Value); err != nil {
			return err
		}
	}

	before := *t
	if hasTitle {
		t.Title = newTitle
	}
	if hasStatus {
		t.Status = newStatus
	}
	if req.AssigneeID.Set {
		t.AssigneeID = req.AssigneeID.Value
	}

	changes := auditDetails{}
	changes.changed("title", before.Title, t.Title)
	changes.changed("status", before.Status, t.Status)
	changes.changedNullable("assignee_id", before.AssigneeID, t.AssigneeID)
	if len(changes) == 0 {
		return c.JSON(t)
	}

	t.UpdatedAt = timestamp(time.Now())
	updated, err := h.store.update(c.Context(), t)
	if err != nil {
		return errDB(err)
	}
	if !updated {
		return errNotFound("task", taskID)
	}
	setAuditDetails(c, changes)
	return c.JSON(t)
}

func (h *taskHandler) delete(c fiber.Ctx) error {
	taskID := c.Params("task_id")
	deleted, err := h.store.delete(c.Context(), c.Params("project_id"), taskID)
	if err != nil {
		return errDB(err)
	}
	if !deleted {
		return errNotFound("task", taskID)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
