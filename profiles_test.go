package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/example/go-fiber-api/plat5"
	"github.com/example/go-fiber-api/profiles"
)

const profilePath = "/users/" + user + "/profile"

func (a *testApp) profile(method, body string, opts ...opt) *http.Response {
	a.t.Helper()
	return a.do(method, profilePath, body, append([]opt{without("X-Plat5-Member-Id"), without("X-Plat5-Project-Id"), header("X-Plat5-User-Id", user)}, opts...)...)
}

// GET makes an empty profile on the first call, and answers the same one after.
func TestProfileGet(t *testing.T) {
	a := newTestApp(t)
	first := decode[profiles.Profile](t, a.profile("GET", ""))
	if first.UserID != user || first.Bio != "" || !millis.MatchString(first.CreatedAt) || first.UpdatedAt != first.CreatedAt {
		t.Fatalf("first: %+v", first)
	}
	if again := decode[profiles.Profile](t, a.profile("GET", "")); again != first {
		t.Fatalf("again: %+v", again)
	}
}

func TestBio(t *testing.T) {
	a := newTestApp(t)
	expectField(t, a.profile("PUT", `{}`), "absent", "bio", "Bio is required.")
	expectField(t, a.profile("PUT", `{"bio":null}`), "null", "bio", fallback)
	expectField(t, a.profile("PUT", `{"bio":5}`), "number", "bio", fallback)
	expectField(t, a.profile("PUT", `{"bio":"`+strings.Repeat("é", 2001)+`"}`), "long", "bio", "Bio is too long.")
	expectField(t, a.profile("PUT", `["hi"]`), "array", "body", fallback)
	for _, bio := range []string{strings.Repeat("😀", 2000), "  spaced  ", ""} {
		res := a.profile("PUT", `{"bio":`+quote(bio)+`}`)
		if got := decode[profiles.Profile](t, res); res.StatusCode != http.StatusOK || got.Bio != bio {
			t.Fatalf("%q: %d %q", bio, res.StatusCode, got.Bio)
		}
	}
}

func TestProfileBodyMustBeJSON(t *testing.T) {
	a := newTestApp(t)
	expectMalformed(t, a.profile("PUT", `{"bio":"hi"}`, without("Content-Type")), "no type")
	expectMalformed(t, a.profile("PUT", `{"bio":"hi"}`, header("Content-Type", "text/plain")), "text")
	expectMalformed(t, a.profile("PUT", ""), "empty")
	expectMalformed(t, a.profile("PUT", "{"), "not JSON")
	for _, tc := range [][2]string{{`{"Bio":"hi"}`, "Bio"}, {`{"bio":"hi","name":"x"}`, "name"}, {`{"BIO":5,"bio":"` + strings.Repeat("x", 2001) + `"}`, "BIO"}} {
		expectField(t, a.profile("PUT", tc[0]), tc[0], tc[1], tc[1]+" isn't a field.")
	}
	// Nothing was written: the profile is still new.
	if got := decode[profiles.Profile](t, a.profile("GET", "")); got.Bio != "" {
		t.Fatalf("bio %q", got.Bio)
	}
}

// PUT is 200 whether the profile is new or not; a new one's bio changed from null.
func TestProfilePutAudit(t *testing.T) {
	a := newTestApp(t)
	for _, tc := range []struct{ body, want string }{
		{`{"bio":""}`, `{"bio":{"from":null,"to":""}}`},
		{`{"bio":"<hi>"}`, `{"bio":{"from":"","to":"\u003chi\u003e"}}`},
		{`{"bio":"<hi>"}`, ``},
	} {
		res := a.profile("PUT", tc.body)
		if res.StatusCode != http.StatusOK || res.Header.Get(plat5.AuditDetailsHeader) != tc.want {
			t.Fatalf("%s: %d %q, want %q", tc.body, res.StatusCode, res.Header.Get(plat5.AuditDetailsHeader), tc.want)
		}
	}
	first := decode[profiles.Profile](t, a.profile("GET", ""))
	if first.Bio != "<hi>" {
		t.Fatalf("bio %q", first.Bio)
	}
}
