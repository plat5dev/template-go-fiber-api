// Package profiles serves each person's own profile, the same in every tree, by user id.
package profiles

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/example/go-fiber-api/telemetry"
)

type Profile struct {
	UserID    string `json:"user_id"`
	Bio       string `json:"bio"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db} }

const columns = `user_id, bio, created_at, updated_at`

func scan(row *sql.Row) (*Profile, error) {
	var p Profile
	if err := row.Scan(&p.UserID, &p.Bio, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// Get is the person's profile, made empty if they have none yet, in one statement: a
// first GET racing another makes one profile.
func (s *Store) Get(ctx context.Context, userID, now string) (_ *Profile, err error) {
	defer telemetry.DB("profiles.get", time.Now(), &err)
	// The no-op update returns the row that's there.
	return scan(s.db.QueryRowContext(ctx, `INSERT INTO profiles (`+columns+`) VALUES (?, '', ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET user_id = excluded.user_id
		RETURNING `+columns, userID, now, now))
}

// Put sets the person's bio, making their profile if need be, and says what the bio was
// (nil for a new profile). The read and the upsert are one transaction.
func (s *Store) Put(ctx context.Context, userID, bio, now string) (_ *Profile, before *string, err error) {
	defer telemetry.DB("profiles.put", time.Now(), &err)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var old string
	switch err := tx.QueryRowContext(ctx, `SELECT bio FROM profiles WHERE user_id = ?`, userID).Scan(&old); {
	case err == nil:
		before = &old
	case !errors.Is(err, sql.ErrNoRows):
		return nil, nil, err
	}
	p, err := scan(tx.QueryRowContext(ctx, `INSERT INTO profiles (`+columns+`) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET bio = excluded.bio, updated_at = excluded.updated_at
		RETURNING `+columns, userID, bio, now, now))
	if err != nil {
		return nil, nil, err
	}
	return p, before, tx.Commit()
}

// Delete deletes the person's profile, with their account, in db or a transaction.
// Deleting none is fine, so it can run again.
func Delete(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, userID string) (err error) {
	defer telemetry.DB("profiles.delete", time.Now(), &err)
	_, err = db.ExecContext(ctx, `DELETE FROM profiles WHERE user_id = ?`, userID)
	return err
}
