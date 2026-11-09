package plat5

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTimestamp(t *testing.T) {
	for in, want := range map[time.Time]string{
		time.Date(2026, 10, 28, 0, 0, 0, 100_000_000, time.UTC):                      "2026-10-28T00:00:00.100Z",
		time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC):                                "2026-10-28T00:00:00.000Z",
		time.Date(2026, 10, 27, 20, 0, 0, 123_456_789, time.FixedZone("x", -4*3600)): "2026-10-28T00:00:00.123Z",
	} {
		if got := Timestamp(in); got != want {
			t.Errorf("Timestamp(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestIDs(t *testing.T) {
	prev := NewID()
	for range 1000 {
		id := NewID()
		if !IsID(id) || id <= prev {
			t.Fatalf("%s after %s", id, prev)
		}
		prev = id
	}
	for _, bad := range []string{"", "01JD8X3K7Q0000000000000000", "81jd8x3k7q0000000000000000", "01jd8x3k7q000000000000000", "01jd8x3k7q000000000000000i"} {
		if IsID(bad) {
			t.Errorf("IsID(%q)", bad)
		}
	}
}

type body struct {
	Title Field[string] `json:"title"`
	Count Field[int]    `json:"count"`
}

func TestDecodeBody(t *testing.T) {
	for _, tc := range []struct {
		data string
		want body
	}{
		{`{}`, body{}},
		{`{"title":"a","count":2}`, body{Title: Field[string]{Set: true, Value: "a"}, Count: Field[int]{Set: true, Value: 2}}},
		{`{"title":null}`, body{Title: Field[string]{Set: true, Null: true}}},
		{`{"title":5,"count":"2"}`, body{Title: Field[string]{Set: true, Wrong: true}, Count: Field[int]{Set: true, Wrong: true}}},
		{` {"title":"a","title":"b"} `, body{Title: Field[string]{Set: true, Value: "b"}}},
	} {
		var got body
		if err := DecodeBody([]byte(tc.data), &got); err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v; want %+v", tc.data, got, err, tc.want)
		}
	}
	for data, want := range map[string]string{
		``:                     "Malformed request.",
		`{`:                    "Malformed request.",
		"{\"title\":\"\xff\"}": "Malformed request.",
		`[]`:                   FallbackValidation,
		`null`:                 FallbackValidation,
		`{"Title":"a"}`:        "Title isn't a field.",
		`{"count":"x","n":1}`:  "n isn't a field.",
	} {
		var e *Error
		if err := DecodeBody([]byte(data), &body{}); !errors.As(err, &e) || e.Message != want {
			t.Errorf("%q: %v, want %s", data, err, want)
		}
	}
}

func TestAuditDetails(t *testing.T) {
	got, err := EncodeAuditDetails(Details{"b": "\x7f", "a": Change{nil, "é😀<"}})
	if want := `{"a":{"from":null,"to":"\u00e9\ud83d\ude00\u003c"},"b":"\u007f"}`; err != nil || got != want {
		t.Fatalf("%s, %v; want %s", got, err, want)
	}
	if _, err := EncodeAuditDetails(Details{"bio": strings.Repeat("x", AuditMaxBytes)}); err == nil {
		t.Fatal("over the cap: no error")
	}
	d := Details{}
	same, a, b := "x", "x", "y"
	d.Changed("same", "x", "x")
	d.ChangedNullable("both", &same, &a)
	d.ChangedNullable("null", nil, nil)
	if len(d) != 0 {
		t.Fatalf("unchanged recorded: %v", d)
	}
	d.Changed("title", "x", "y")
	d.ChangedNullable("assignee", &a, &b)
	d.ChangedNullable("cleared", &a, nil)
	if len(d) != 3 {
		t.Fatalf("changes %v", d)
	}
}
