package tasks

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/pagination"
)

// Task is authored data: CreatedBy is the member who made it, and stays theirs if they
// leave the tree (plat5 docs/concepts/model.md#where-a-field-goes).
type Task struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	CreatedBy     string `json:"created_by"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const columns = `id, environment_id, title, status, created_by, created_at, updated_at`

func scan(row interface{ Scan(...any) error }, t *Task) error {
	return row.Scan(&t.ID, &t.EnvironmentID, &t.Title, &t.Status, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
}

// ListByEnvironment returns one page of the environment's tasks in id order, and whether
// there are more.
func (s *Store) ListByEnvironment(ctx context.Context, environmentID string, page pagination.Page) ([]Task, bool, error) {
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+columns+`
		FROM tasks WHERE environment_id = ? AND id > ? ORDER BY id LIMIT ?`,
		environmentID, page.StartingAfter, page.Limit+1)
	metrics.RecordDBOperation("tasks.list", time.Since(start), err)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]Task, 0)
	for rows.Next() {
		var t Task
		if err := scan(rows, &t); err != nil {
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

func (s *Store) FindInEnvironment(ctx context.Context, environmentID, taskID string) (*Task, error) {
	start := time.Now()
	row := s.db.QueryRowContext(ctx, `
		SELECT `+columns+`
		FROM tasks WHERE id = ? AND environment_id = ?`,
		taskID, environmentID)
	var t Task
	err := scan(row, &t)
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
		INSERT INTO tasks (`+columns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.EnvironmentID, t.Title, t.Status, t.CreatedBy, t.CreatedAt, t.UpdatedAt)
	metrics.RecordDBOperation("tasks.insert", time.Since(start), err)
	return err
}

func (s *Store) Update(ctx context.Context, t *Task) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET title = ?, status = ?, updated_at = ?
		WHERE id = ? AND environment_id = ?`,
		t.Title, t.Status, t.UpdatedAt, t.ID, t.EnvironmentID)
	metrics.RecordDBOperation("tasks.update", time.Since(start), err)
	return err
}

func (s *Store) Delete(ctx context.Context, environmentID, taskID string) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM tasks WHERE id = ? AND environment_id = ?`,
		taskID, environmentID)
	metrics.RecordDBOperation("tasks.delete", time.Since(start), err)
	return err
}

// DeleteByEnvironment deletes every task of an environment, for its purge. Deleting none
// is fine, so it can run again for the same environment.
func (s *Store) DeleteByEnvironment(ctx context.Context, environmentID string) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE environment_id = ?`, environmentID)
	metrics.RecordDBOperation("tasks.delete_by_environment", time.Since(start), err)
	return err
}
