// Package tasks serves a project's tasks.
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/example/go-fiber-api/plat5"
	"github.com/example/go-fiber-api/telemetry"
)

// Task is authored data: CreatedBy is the member who made it, and stays theirs if they
// leave the tree (plat5 docs/concepts/model.md#where-a-field-goes). AssigneeID is a member
// in the same tree, or nil (null).
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

// Store keeps tasks. Every statement is scoped by the project id.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db} }

const columns = `id, project_id, title, status, assignee_id, created_by, created_at, updated_at`

func scan(row interface{ Scan(...any) error }, t *Task) error {
	return row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Status, &t.AssigneeID, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
}

// List is one page of the project's tasks in id order, and whether more follow.
func (s *Store) List(ctx context.Context, projectID string, page plat5.Page) (list []Task, more bool, err error) {
	defer telemetry.DB("tasks.list", time.Now(), &err)
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM tasks
		WHERE project_id = ? AND id > ? ORDER BY id LIMIT ?`, projectID, page.StartingAfter, page.Limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	list = []Task{}
	for rows.Next() {
		var t Task
		if err := scan(rows, &t); err != nil {
			return nil, false, err
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	list, more = plat5.Trim(list, page)
	return list, more, nil
}

// Find is the task with taskID in the project, or nil.
func (s *Store) Find(ctx context.Context, projectID, taskID string) (_ *Task, err error) {
	defer telemetry.DB("tasks.find", time.Now(), &err)
	var t Task
	err = scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM tasks WHERE project_id = ? AND id = ?`, projectID, taskID), &t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) Insert(ctx context.Context, t *Task) (err error) {
	defer telemetry.DB("tasks.insert", time.Now(), &err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO tasks (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Title, t.Status, t.AssigneeID, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	return err
}

// Update writes t's fields to its task in its project, and says whether there was one.
func (s *Store) Update(ctx context.Context, t *Task) (_ bool, err error) {
	defer telemetry.DB("tasks.update", time.Now(), &err)
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET title = ?, status = ?, assignee_id = ?, updated_at = ?
		WHERE project_id = ? AND id = ?`, t.Title, t.Status, t.AssigneeID, t.UpdatedAt, t.ProjectID, t.ID)
	return changedRow(res, err)
}

// Delete deletes the task with taskID in the project, and says whether there was one.
func (s *Store) Delete(ctx context.Context, projectID, taskID string) (_ bool, err error) {
	defer telemetry.DB("tasks.delete", time.Now(), &err)
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = ? AND id = ?`, projectID, taskID)
	return changedRow(res, err)
}

// DeleteProject deletes every task of a project, for its purge, in db or a transaction.
// Deleting none is fine, so it can run again.
func DeleteProject(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, projectID string) (err error) {
	defer telemetry.DB("tasks.delete_project", time.Now(), &err)
	_, err = db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = ?`, projectID)
	return err
}

func changedRow(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
