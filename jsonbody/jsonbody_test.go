package jsonbody

import (
	"encoding/json"
	stderrors "errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/example/go-fiber-api/errors"
)

type tag struct {
	Name string `json:"name"`
}

type address struct {
	Street string `json:"street"`
}

type base struct {
	ID string `json:"id"`
}

type body struct {
	base
	Title   string          `json:"title"`
	Done    *bool           `json:"done,omitempty"`
	Raw     json.RawMessage `json:"raw"`
	Any     any             `json:"any"`
	Address *address        `json:"address"`
	Tags    []tag           `json:"tags"`
	Meta    map[string]any  `json:"meta"`
	ByName  map[string]tag  `json:"by_name"`
	Skipped string          `json:"-"`
	Plain   string
}

type field struct{ Path, Message string }

// fields reads the 422's message and details.fields, or fails.
func fields(t *testing.T, err error) (string, []field) {
	t.Helper()
	var apiErr *errors.ApiError
	if !stderrors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Code != "VALIDATION_ERROR" {
		t.Fatalf("err = %v, want a 422", err)
	}
	b, _ := json.Marshal(apiErr.Details)
	var details struct {
		Fields []field `json:"fields"`
	}
	if err := json.Unmarshal(b, &details); err != nil {
		t.Fatal(err)
	}
	return apiErr.Message, details.Fields
}

func TestValidBody(t *testing.T) {
	var v body
	data := `{"id":"x","title":"T","done":true,"raw":{"Anything":1},"any":{"Else":[1]},` +
		`"address":{"street":"Main"},"tags":[{"name":"a"},{"name":"b"}],"meta":{"Free":{"Form":1}},` +
		`"by_name":{"k":{"name":"c"}},"Plain":"p"}`
	if err := Decode([]byte(data), &v); err != nil {
		t.Fatalf("err = %v", err)
	}
	if v.ID != "x" || v.Title != "T" || v.Address.Street != "Main" || len(v.Tags) != 2 || v.ByName["k"].Name != "c" || v.Plain != "p" {
		t.Fatalf("v = %+v", v)
	}
}

func TestUnknownFields(t *testing.T) {
	for _, tc := range []struct {
		data  string
		paths []string
	}{
		{`{"Title":"T"}`, []string{"Title"}},
		{`{"title":"T","extra":1}`, []string{"extra"}},
		{`{"b":1,"title":"T","a":2}`, []string{"b", "a"}},
		{`{"ID":"x"}`, []string{"ID"}},
		{`{"Skipped":"s"}`, []string{"Skipped"}},
		{`{"plain":"p"}`, []string{"plain"}},
		{`{"address":{"Street":"Main"}}`, []string{"address.Street"}},
		{`{"tags":[{"name":"a"},{"Name":"b","x":1}]}`, []string{"tags[1].Name", "tags[1].x"}},
		{`{"by_name":{"k":{"nope":1}}}`, []string{"by_name.k.nope"}},
		// The unknown fields, not the wrong type that encoding/json would match to title.
		{`{"Title":5,"zzz":true}`, []string{"Title", "zzz"}},
		// A repeated key is listed once, where it first appears.
		{`{"x":1,"title":"T","y":2,"x":3}`, []string{"x", "y"}},
	} {
		var v body
		message, got := fields(t, Decode([]byte(tc.data), &v))
		if len(got) != len(tc.paths) {
			t.Fatalf("%s: fields %+v, want %q", tc.data, got, tc.paths)
		}
		for i, p := range tc.paths {
			if got[i].Path != p || got[i].Message != p+" isn't a field." {
				t.Fatalf("%s: fields[%d] = %+v, want %q", tc.data, i, got[i], p)
			}
		}
		if message != tc.paths[0]+" isn't a field." {
			t.Fatalf("%s: message %q", tc.data, message)
		}
		if !reflect.DeepEqual(v, body{}) {
			t.Fatalf("%s: wrote %+v", tc.data, v)
		}
	}
}

// Not JSON, or a known field of the wrong type, is a 422 at body with the fallback sentence.
func TestBadBody(t *testing.T) {
	for _, data := range []string{``, `{`, `{"title":"T"} x`, `{"title":5}`, `{"tags":{"name":"a"}}`} {
		var v body
		message, got := fields(t, Decode([]byte(data), &v))
		if message != errors.FallbackValidation || len(got) != 1 || got[0].Path != "body" {
			t.Fatalf("%q: %q %+v", data, message, got)
		}
	}
}

func TestNullLeavesValue(t *testing.T) {
	v := body{Title: "kept"}
	if err := Decode([]byte(`null`), &v); err != nil || v.Title != "kept" {
		t.Fatalf("err %v, v %+v", err, v)
	}
}

// Read checks the Content-Type, then decodes as Decode does.
func TestRead(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: errors.FiberErrorHandler})
	app.Post("/", func(c fiber.Ctx) error {
		var v body
		if err := Read(c, &v); err != nil {
			return err
		}
		return c.JSON(v)
	})
	for _, tc := range []struct {
		contentType, data string
		status            int
		path              string
	}{
		{"application/json", `{"title":"T"}`, 200, ""},
		{"text/plain", `{"title":"T"}`, 422, "body"},
		{"application/json", `{"Title":"T"}`, 422, "Title"},
	} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(tc.data))
		req.Header.Set("Content-Type", tc.contentType)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status {
			t.Fatalf("%s %s: status %d", tc.contentType, tc.data, res.StatusCode)
		}
		if tc.path == "" {
			continue
		}
		var out struct {
			Error struct {
				Details struct {
					Fields []field `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if f := out.Error.Details.Fields; len(f) != 1 || f[0].Path != tc.path {
			t.Fatalf("%s %s: fields %+v", tc.contentType, tc.data, f)
		}
	}
}
