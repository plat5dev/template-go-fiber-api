package profiles

import (
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/plat5"
)

// Handler serves the profile of the user in the path, put there by the front door. No name
// here: the person's name is identity's (GET /user), shown in every tree.
type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store} }

// maxBio is a bio's longest, in characters (code points).
const maxBio = 2000

// Get answers the profile, made empty on the person's first call.
func (h *Handler) Get(c fiber.Ctx) error {
	p, err := h.store.Get(c.Context(), c.Params("user_id"), plat5.Timestamp(time.Now()))
	if err != nil {
		return plat5.DB(err)
	}
	return c.JSON(p)
}

// Put sets the bio, kept as sent: not trimmed, and "" is a bio.
func (h *Handler) Put(c fiber.Ctx) error {
	var b struct {
		Bio plat5.Field[string] `json:"bio"`
	}
	if err := plat5.ReadBody(c, &b); err != nil {
		return err
	}
	switch {
	case !b.Bio.Set:
		return plat5.Invalid("bio", "Bio is required.")
	case b.Bio.Null || b.Bio.Wrong:
		return plat5.Invalid("bio", "")
	case utf8.RuneCountInString(b.Bio.Value) > maxBio:
		return plat5.Invalid("bio", "Bio is too long.")
	}
	p, before, err := h.store.Put(c.Context(), c.Params("user_id"), b.Bio.Value, plat5.Timestamp(time.Now()))
	if err != nil {
		return plat5.DB(err)
	}
	// A new profile's bio changed from nothing. Its id is the user's, which the path
	// already carries, so there's no created id.
	if before == nil {
		plat5.SetAuditDetails(c, plat5.Details{"bio": plat5.Change{From: nil, To: p.Bio}})
	} else {
		changes := plat5.Details{}
		changes.Changed("bio", *before, p.Bio)
		plat5.SetAuditDetails(c, changes)
	}
	return c.JSON(p)
}
