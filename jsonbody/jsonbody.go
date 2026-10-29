// Package jsonbody reads a request's JSON body: a body sent as JSON, or a 422 at body.
//
// A body counts as JSON only with Content-Type application/json (any parameters, such as
// charset) or an application/…+json type. Without a Content-Type, or with any other, the
// body is a 422 at body whatever it holds, empty included: a client that forgot the header
// never gets a write that silently changed nothing. Bind failures use the fallback sentence
// (plat5 docs/reference/error-copy.md).
package jsonbody

import (
	"encoding/json"
	"mime"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/errors"
)

// IsJSON says whether contentType names JSON: application/json or application/…+json.
func IsJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" ||
		(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))
}

// Read decodes the request's body into v. A body not sent as JSON, an empty body, or one
// that isn't a JSON value of v's shape (malformed, trailing data, a field of the wrong
// type) is a 422 at body. A JSON null leaves v as it is.
func Read(c fiber.Ctx, v any) error {
	if !IsJSON(c.Get(fiber.HeaderContentType)) {
		return errors.FieldError("body", "")
	}
	if err := json.Unmarshal(c.Body(), v); err != nil {
		return errors.FieldError("body", "")
	}
	return nil
}
