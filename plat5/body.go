package plat5

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// Field is one member of a request body: absent (!Set), null, a T, or of the wrong JSON
// type (Wrong). Decoding a Field never fails, so a body's every value can be checked, and
// the handler says what each case means.
type Field[T any] struct {
	Set, Null, Wrong bool
	Value            T
}

// UnmarshalJSONFrom reads the member's value. A T is decoded as encoding/json/v2 does,
// never coerced: 5 isn't a string.
func (f *Field[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}
	f.Set = true
	if value.Kind() == 'n' {
		f.Null = true
		return nil
	}
	f.Wrong = json.Unmarshal(value, &f.Value) != nil
	return nil
}

// ReadBody decodes the request's body into v, a pointer to a struct of Fields: see
// DecodeBody. A body not sent as application/json (with any parameters) is a 400.
func ReadBody(c fiber.Ctx, v any) error {
	mediaType, _, _ := strings.Cut(c.Get(fiber.HeaderContentType), ";")
	if !strings.EqualFold(strings.TrimSpace(mediaType), fiber.MIMEApplicationJSON) {
		return InvalidRequest()
	}
	return DecodeBody(c.Body(), v)
}

// The JSON a body may be: a repeated key is allowed, the last one counting, as JSON.parse
// has it.
var bodyOptions = jsontext.AllowDuplicateNames(true)

// DecodeBody decodes data into v, a pointer to a struct of Fields. Data that's empty or
// isn't JSON is a 400. JSON that isn't an object is a 422 at body. A key v has no field
// for, matched exactly ({"Title": …} isn't title), is a 422 "<key> isn't a field.", for the
// first one, before any value is checked.
func DecodeBody(data []byte, v any) error {
	if !jsontext.Value(data).IsValid(bodyOptions) {
		return InvalidRequest()
	}
	if jsontext.Value(data).Kind() != '{' {
		return Invalid("body", "")
	}
	err := json.Unmarshal(data, v, bodyOptions, json.RejectUnknownMembers(true))
	var serr *json.SemanticError
	if errors.As(err, &serr) && errors.Is(serr.Err, json.ErrUnknownName) {
		name := serr.JSONPointer.LastToken()
		return Invalid(name, name+" isn't a field.")
	}
	if err != nil {
		return Invalid("body", "")
	}
	return nil
}
