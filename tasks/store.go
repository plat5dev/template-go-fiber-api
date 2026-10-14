package tasks

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/pagination"
)

type Task struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListByProject returns one page of the project's tasks in id order, and whether there are more.
func (s *Store) ListByProject(ctx context.Context, orgID, projectID string, page pagination.Page) ([]Task, bool, error) {
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, project_id, title, status, created_at, updated_at
		FROM tasks WHERE organization_id = ? AND project_id = ? AND id > ? ORDER BY id LIMIT ?`,
		orgID, projectID, page.StartingAfter, page.Limit+1)
	metrics.RecordDBOperation("tasks.list", time.Since(start), err)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]Task, 0)
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.OrganizationID, &t.ProjectID, &t.Title, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, false, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	out, hasMore := pagination.Trim(out, page)
	return out, hasMore, nil
}

func (s *Store) FindInProject(ctx context.Context, orgID, projectID, taskID string) (*Task, error) {
	start := time.Now()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, project_id, title, status, created_at, updated_at
		FROM tasks WHERE id = ? AND organization_id = ? AND project_id = ?`,
		taskID, orgID, projectID)
	var t Task
	err := row.Scan(&t.ID, &t.OrganizationID, &t.ProjectID, &t.Title, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	metrics.RecordDBOperation("tasks.find", time.Since(start), err)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) Insert(ctx context.Context, t *Task) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, organization_id, project_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.OrganizationID, t.ProjectID, t.Title, t.Status, t.CreatedAt, t.UpdatedAt)
	metrics.RecordDBOperation("tasks.insert", time.Since(start), err)
	return err
}

func (s *Store) Update(ctx context.Context, t *Task) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET title = ?, status = ?, updated_at = ?
		WHERE id = ? AND organization_id = ? AND project_id = ?`,
		t.Title, t.Status, t.UpdatedAt, t.ID, t.OrganizationID, t.ProjectID)
	metrics.RecordDBOperation("tasks.update", time.Since(start), err)
	return err
}

func (s *Store) Delete(ctx context.Context, orgID, projectID, taskID string) error {
	start := time.Now()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM tasks WHERE id = ? AND organization_id = ? AND project_id = ?`,
		taskID, orgID, projectID)
	metrics.RecordDBOperation("tasks.delete", time.Since(start), err)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
