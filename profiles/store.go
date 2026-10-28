package profiles

import (
	"context"
	"database/sql"
	"time"

	"github.com/example/go-fiber-api/metrics"
)

type Profile struct {
	UserID    string `json:"user_id"`
	Bio       string `json:"bio"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
		SELECT user_id, bio, created_at, updated_at
		FROM profiles WHERE user_id = ?`, userID)
	var p Profile
	err := row.Scan(&p.UserID, &p.Bio, &p.CreatedAt, &p.UpdatedAt)
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
		INSERT INTO profiles (user_id, bio, created_at, updated_at)
		VALUES (?, ?, ?, ?)`,
		p.UserID, p.Bio, p.CreatedAt, p.UpdatedAt)
	metrics.RecordDBOperation("profiles.insert", time.Since(start), err)
	return err
}

func (s *Store) Update(ctx context.Context, p *Profile) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET bio = ?, updated_at = ?
		WHERE user_id = ?`,
		p.Bio, p.UpdatedAt, p.UserID)
	metrics.RecordDBOperation("profiles.update", time.Since(start), err)
	return err
}

// Delete deletes the person's profile, with their account. Deleting none is fine, so it can
// run again for the same person.
func (s *Store) Delete(ctx context.Context, userID string) error {
	start := time.Now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE user_id = ?`, userID)
	metrics.RecordDBOperation("profiles.delete", time.Since(start), err)
	return err
}
