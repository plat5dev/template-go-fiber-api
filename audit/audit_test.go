package audit

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestChangedSkipsEqual(t *testing.T) {
	d := Details{}
	d.Changed("name", "Launch", "Launch")
	d.Changed("status", "todo", "done")
	if _, ok := d["name"]; ok {
		t.Fatal("unchanged field recorded")
	}
	if got := d["status"]; got != (Change{From: "todo", To: "done"}) {
		t.Fatalf("status = %v", got)
	}
}

func TestEncodeIsVisibleASCII(t *testing.T) {
	got, err := Encode(Details{"name": Change{From: "café", To: "🚀"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":{"from":"caf\u00e9","to":"\ud83d\ude80"}}`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestEncodeOverCap(t *testing.T) {
	if _, err := Encode(Details{"bio": strings.Repeat("a", MaxBytes)}); err == nil {
		t.Fatal("want an error over the cap")
	}
}

func TestSet(t *testing.T) {
	app := fiber.New()
	app.Post("/changed", func(c fiber.Ctx) error {
		d := Details{}
		d.Changed("name", "a", "b")
		Set(c, d)
		SetCreated(c, "01JD8Y2N5R7T9V1X3Z5B7D9F1H")
		return c.SendStatus(fiber.StatusOK)
	})
	app.Post("/unchanged", func(c fiber.Ctx) error {
		d := Details{}
		d.Changed("name", "a", "a")
		Set(c, d)
		return c.SendStatus(fiber.StatusOK)
	})
	app.Post("/too-big", func(c fiber.Ctx) error {
		Set(c, Details{"bio": strings.Repeat("a", MaxBytes)})
		return c.SendStatus(fiber.StatusOK)
	})

	res, err := app.Test(httptest.NewRequest("POST", "/changed", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Header.Get(DetailsHeader); got != `{"name":{"from":"a","to":"b"}}` {
		t.Fatalf("details = %q", got)
	}
	if got := res.Header.Get(CreatedHeader); got != "01JD8Y2N5R7T9V1X3Z5B7D9F1H" {
		t.Fatalf("created = %q", got)
	}

	for _, path := range []string{"/unchanged", "/too-big"} {
		res, err := app.Test(httptest.NewRequest("POST", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Header.Get(DetailsHeader); got != "" {
			t.Fatalf("%s: details = %q, want none", path, got)
		}
	}
}
