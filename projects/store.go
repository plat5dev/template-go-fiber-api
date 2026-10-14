package projects

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
	"github.com/example/go-fiber-api/pagination"
)

type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListByOrg returns one page of the org's projects in id order, and whether there are more.
func (s *Store) ListByOrg(ctx context.Context, orgID string, page pagination.Page) ([]Project, bool, error) {
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, name, description, created_at, updated_at
		FROM projects WHERE organization_id = ? AND id > ? ORDER BY id LIMIT ?`,
		orgID, page.StartingAfter, page.Limit+1)
	metrics.RecordDBOperation("projects.list", time.Since(start), err)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]Project, 0)
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, false, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	out, hasMore := pagination.Trim(out, page)
	return out, hasMore, nil
}

func (s *Store) FindInOrg(ctx context.Context, orgID, projectID string) (*Project, error) {
	start := time.Now()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, name, description, created_at, updated_at
		FROM projects WHERE id = ? AND organization_id = ?`, projectID, orgID)
	var p Project
	err := row.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	metrics.RecordDBOperation("projects.find", time.Since(start), err)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) Insert(ctx context.Context, p *Project) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, organization_id, name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.OrganizationID, p.Name, p.Description, p.CreatedAt, p.UpdatedAt)
	metrics.RecordDBOperation("projects.insert", time.Since(start), err)
	return err
}

func (s *Store) Update(ctx context.Context, p *Project) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE projects SET name = ?, description = ?, updated_at = ?
		WHERE id = ? AND organization_id = ?`,
		p.Name, p.Description, p.UpdatedAt, p.ID, p.OrganizationID)
	metrics.RecordDBOperation("projects.update", time.Since(start), err)
	return err
}

func (s *Store) Delete(ctx context.Context, orgID, projectID string) error {
	start := time.Now()
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND organization_id = ?`, projectID, orgID)
	metrics.RecordDBOperation("projects.delete", time.Since(start), err)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
