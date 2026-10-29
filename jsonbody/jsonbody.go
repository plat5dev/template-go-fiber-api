// Package jsonbody reads a request's JSON body: a body sent as JSON, or a 422.
//
// A body counts as JSON only with Content-Type application/json (any parameters, such as
// charset) or an application/…+json type. Without a Content-Type, or with any other, the
// body is a 422 at body whatever it holds, empty included: a client that forgot the header
// never gets a write that silently changed nothing. Bind failures use the fallback sentence
// (plat5 docs/reference/error-copy.md).
//
// Keys match exactly. encoding/json alone matches them without regard to case and drops
// what it doesn't know; Read first checks every key of the body against the target's json
// names, so {"Title": …} is an unknown field, not title.
package jsonbody

import (
	"bytes"
	"encoding"
	"encoding/json"
	"mime"
	"reflect"
	"strconv"
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

// Read decodes the request's body into v: see Decode. A body not sent as JSON is a 422 at
// body.
func Read(c fiber.Ctx, v any) error {
	if !IsJSON(c.Get(fiber.HeaderContentType)) {
		return errors.FieldError("body", "")
	}
	return Decode(c.Body(), v)
}

// Decode decodes data, a body sent as JSON, into v. An empty body, or one that isn't a JSON
// value (malformed, trailing data), is a 422 at body. Then a key v has no field for, by its
// exact json name, is a 422 listing every such key in the order they appear (see Unknown),
// each "<path> isn't a field.", the first one's the message. Then a value of the wrong type
// is a 422 at body. A JSON null leaves v as it is.
func Decode(data []byte, v any) error {
	if !json.Valid(data) {
		return errors.FieldError("body", "")
	}
	if unknown := Unknown(data, reflect.TypeOf(v)); len(unknown) > 0 {
		fields := make([]map[string]string, len(unknown))
		for i, path := range unknown {
			fields[i] = map[string]string{"path": path, "message": path + " isn't a field."}
		}
		return errors.ValidationError(fields[0]["message"], map[string]any{"fields": fields})
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errors.FieldError("body", "")
	}
	return nil
}

// Unknown lists the paths of the keys in data, valid JSON, that t has no field for by exact
// json name, in the order they appear. A nested key's path is its parent's, a dot and the
// key (address.Street); an array item's is its parent's and its index (tags[0].Name). A map
// takes any keys, and its values are checked as the map's element type (meta.key.Field). A
// field that decodes itself (json.Unmarshaler, such as json.RawMessage, or
// encoding.TextUnmarshaler) or is an interface takes anything.
func Unknown(data []byte, t reflect.Type) []string {
	var all []string
	walk(data, t, "", &all)
	// A key repeated in the body is listed once, where it first appears.
	var out []string
	seen := map[string]bool{}
	for _, p := range all {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func decodesItself(t reflect.Type) bool {
	return t.Implements(jsonUnmarshaler) || t.Implements(textUnmarshaler)
}

func walk(data []byte, t reflect.Type, path string, out *[]string) {
	for t != nil && t.Kind() == reflect.Pointer {
		if decodesItself(t) {
			return
		}
		t = t.Elem()
	}
	if t == nil || t.Kind() == reflect.Interface || decodesItself(reflect.PointerTo(t)) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		names := fieldNames(t)
		eachMember(data, func(key string, value []byte) {
			p := join(path, key)
			ft, ok := names[key]
			if !ok {
				*out = append(*out, p)
				return
			}
			walk(value, ft, p, out)
		})
	case reflect.Map:
		eachMember(data, func(key string, value []byte) {
			walk(value, t.Elem(), join(path, key), out)
		})
	case reflect.Slice, reflect.Array:
		eachItem(data, func(i int, value []byte) {
			walk(value, t.Elem(), path+"["+strconv.Itoa(i)+"]", out)
		})
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// fieldNames maps each json name a struct decodes, exactly as written, to its field's type,
// promoting the fields of untagged embedded structs as encoding/json does.
func fieldNames(t reflect.Type) map[string]reflect.Type {
	names := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				for k, v := range fieldNames(et) {
					if _, ok := names[k]; !ok {
						names[k] = v
					}
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		names[name] = f.Type
	}
	return names
}

// eachMember calls fn with each key and raw value of data, if it is an object, in order.
func eachMember(data []byte, fn func(key string, value []byte)) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return
		}
		fn(key, value)
	}
}

// eachItem calls fn with each index and raw item of data, if it is an array, in order.
func eachItem(data []byte, fn func(i int, value []byte)) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return
	}
	for i := 0; dec.More(); i++ {
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return
		}
		fn(i, value)
	}
}
