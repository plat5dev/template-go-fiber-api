package profiles

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
)

type Profile struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Bio         string `json:"bio"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) FindByUserID(ctx context.Context, userID string) (*Profile, error) {
	start := time.Now()
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, display_name, bio, created_at, updated_at
		FROM profiles WHERE user_id = ?`, userID)
	var p Profile
	err := row.Scan(&p.UserID, &p.DisplayName, &p.Bio, &p.CreatedAt, &p.UpdatedAt)
	metrics.RecordDBOperation("profiles.find", time.Since(start), err)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) Insert(ctx context.Context, p *Profile) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO profiles (user_id, display_name, bio, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		p.UserID, p.DisplayName, p.Bio, p.CreatedAt, p.UpdatedAt)
	metrics.RecordDBOperation("profiles.insert", time.Since(start), err)
	return err
}

func (s *Store) Update(ctx context.Context, p *Profile) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET display_name = ?, bio = ?, updated_at = ?
		WHERE user_id = ?`,
		p.DisplayName, p.Bio, p.UpdatedAt, p.UserID)
	metrics.RecordDBOperation("profiles.update", time.Since(start), err)
	return err
}
