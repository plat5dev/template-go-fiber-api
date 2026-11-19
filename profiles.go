package main

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
)

const (
	maxBioLen      = 2000
	profileColumns = `user_id, bio, created_at, updated_at`
)

type Profile struct {
	UserID    string `json:"user_id"`
	Bio       string `json:"bio"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type profileStore struct{ db *sql.DB }

func newProfileStore(db *sql.DB) *profileStore { return &profileStore{db: db} }

func scanProfile(row *sql.Row) (*Profile, error) {
	var p Profile
	if err := row.Scan(&p.UserID, &p.Bio, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// get lazily creates an empty profile on first read.
func (s *profileStore) get(ctx context.Context, userID, now string) (*Profile, error) {
	return scanProfile(s.db.QueryRowContext(ctx,
		`INSERT INTO profiles (`+profileColumns+`) VALUES (?, '', ?, ?)
		 ON CONFLICT (user_id) DO UPDATE SET user_id = excluded.user_id
		 RETURNING `+profileColumns, userID, now, now))
}

// put upserts the bio and returns the prior bio (nil when newly created).
func (s *profileStore) put(ctx context.Context, userID, bio, now string) (*Profile, *string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var before *string
	var old string
	switch err := tx.QueryRowContext(ctx, `SELECT bio FROM profiles WHERE user_id = ?`, userID).Scan(&old); {
	case err == nil:
		before = &old
	case !errors.Is(err, sql.ErrNoRows):
		return nil, nil, err
	}

	p, err := scanProfile(tx.QueryRowContext(ctx,
		`INSERT INTO profiles (`+profileColumns+`) VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id) DO UPDATE SET bio = excluded.bio, updated_at = excluded.updated_at
		 RETURNING `+profileColumns, userID, bio, now, now))
	if err != nil {
		return nil, nil, err
	}
	return p, before, tx.Commit()
}

func deleteProfile(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, userID string) error {
	_, err := exec.ExecContext(ctx, `DELETE FROM profiles WHERE user_id = ?`, userID)
	return err
}

// HTTP handlers.

type profileHandler struct{ store *profileStore }

func newProfileHandler(store *profileStore) *profileHandler { return &profileHandler{store: store} }

func (h *profileHandler) get(c fiber.Ctx) error {
	p, err := h.store.get(c.Context(), c.Params("user_id"), timestamp(time.Now()))
	if err != nil {
		return errDB(err)
	}
	return c.JSON(p)
}

func (h *profileHandler) put(c fiber.Ctx) error {
	var req struct {
		Bio *string `json:"bio"`
	}
	if err := decodeJSON(c, &req); err != nil {
		return err
	}
	if req.Bio == nil {
		return errInvalid("bio", "Bio is required.")
	}
	if utf8.RuneCountInString(*req.Bio) > maxBioLen {
		return errInvalid("bio", "Bio is too long.")
	}

	p, before, err := h.store.put(c.Context(), c.Params("user_id"), *req.Bio, timestamp(time.Now()))
	if err != nil {
		return errDB(err)
	}
	if before == nil {
		setAuditDetails(c, auditDetails{"bio": auditChange{From: nil, To: p.Bio}})
	} else {
		changes := auditDetails{}
		changes.changed("bio", *before, p.Bio)
		setAuditDetails(c, changes)
	}
	return c.JSON(p)
}
