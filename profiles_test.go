package main

import (
	"net/http"
	"strings"
	"testing"
)

func (a *testApp) doProfile(method, body string, opts ...reqOpt) *http.Response {
	a.t.Helper()
	base := []reqOpt{
		withoutHeader("X-Plat5-Member-Id"),
		withoutHeader("X-Plat5-Project-Id"),
		withHeader("X-Plat5-User-Id", testUser),
	}
	return a.do(method, profilePath, body, append(base, opts...)...)
}

func TestProfileLazyGetAndPut(t *testing.T) {
	a := newTestApp(t)

	// First GET lazily creates an empty profile; second GET returns the same row.
	first := decodeBody[Profile](t, a.doProfile("GET", ""))
	if first.UserID != testUser || first.Bio != "" || first.CreatedAt == "" || first.UpdatedAt != first.CreatedAt {
		t.Fatalf("first get: %+v", first)
	}
	if again := decodeBody[Profile](t, a.doProfile("GET", "")); again != first {
		t.Fatalf("second get: %+v != %+v", again, first)
	}

	// Validation: bio required, <= 2000 runes, wrong type rejected.
	for _, payload := range []string{`{}`, `{"bio":null}`, `{"bio":5}`, `{"bio":"` + strings.Repeat("é", 2001) + `"}`} {
		res := a.doProfile("PUT", payload)
		body := decodeBody[errorBody](t, res)
		if res.StatusCode != http.StatusUnprocessableEntity || body.Error.Details.Fields[0].Path != "bio" {
			t.Fatalf("invalid bio %s: %d %+v", payload, res.StatusCode, body.Error)
		}
	}

	// 2000 runes and whitespace-only / empty bios are accepted verbatim.
	for _, bio := range []string{strings.Repeat("😀", 2000), "  spaced  ", ""} {
		res := a.doProfile("PUT", `{"bio":"`+bio+`"}`)
		got := decodeBody[Profile](t, res)
		if res.StatusCode != http.StatusOK || got.Bio != bio {
			t.Fatalf("bio %q: status=%d got=%q", bio, res.StatusCode, got.Bio)
		}
	}
}

func TestProfilePutAuditHeaders(t *testing.T) {
	a := newTestApp(t)

	// New profile (before == nil) -> from: null
	res := a.doProfile("PUT", `{"bio":"<hello>"}`)
	if got := res.Header.Get(auditDetailsHeader); got != `{"bio":{"from":null,"to":"<hello>"}}` {
		t.Fatalf("first put audit = %q", got)
	}

	// No-op PUT -> no audit header
	res = a.doProfile("PUT", `{"bio":"<hello>"}`)
	if got := res.Header.Get(auditDetailsHeader); got != "" {
		t.Fatalf("no-op put audit = %q, want empty", got)
	}

	// Update -> diff from old to new
	res = a.doProfile("PUT", `{"bio":"updated"}`)
	if got := res.Header.Get(auditDetailsHeader); got != `{"bio":{"from":"<hello>","to":"updated"}}` {
		t.Fatalf("update put audit = %q", got)
	}
}
