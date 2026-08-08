package projects

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
)

type Project struct {
	ID                    string `json:"id"`
	OrganizationID        string `json:"organization_id"`
	Name                  string `json:"name"`
	Description           string `json:"description"`
	CreatedByMembershipID string `json:"created_by_membership_id"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) ListByOrg(ctx context.Context, orgID string) ([]Project, error) {
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, name, description, created_by_membership_id, created_at, updated_at
		FROM projects WHERE organization_id = ? ORDER BY created_at DESC`, orgID)
	metrics.RecordDBOperation("projects.list", time.Since(start), err)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Project, 0)
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.CreatedByMembershipID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) FindInOrg(ctx context.Context, orgID, projectID string) (*Project, error) {
	start := time.Now()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, name, description, created_by_membership_id, created_at, updated_at
		FROM projects WHERE id = ? AND organization_id = ?`, projectID, orgID)
	var p Project
	err := row.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Description, &p.CreatedByMembershipID, &p.CreatedAt, &p.UpdatedAt)
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
		INSERT INTO projects (id, organization_id, name, description, created_by_membership_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.OrganizationID, p.Name, p.Description, p.CreatedByMembershipID, p.CreatedAt, p.UpdatedAt)
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
