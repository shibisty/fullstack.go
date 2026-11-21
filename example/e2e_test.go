package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"fullstack"
	"fullstack/browser"
	auth "fullstack/passport"
	"fullstack/web"
	"passport"
	"passport/local"
)

// TestBrowser drives the example in a headless Chrome (skipped without
// one): the frontend built by esbuild in the process, navigation without
// reloads, pages rendered in the browser from data, forms, islands.
func TestBrowser(t *testing.T) {
	t.Setenv("APP_NAME", "Example")
	t.Setenv("APP_ENV", "local") // the frontend is built in the process, as with gtr dev
	t.Setenv("ASSETS_LIVE", "false")
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Register(assetsProvider(), &auth.Provider{Strategies: []passport.Strategy{local.New(verifyUser)}}, Routes{}); err != nil {
		t.Fatal(err)
	}
	h, err := app.Handler()
	if err != nil {
		t.Fatal(err)
	}
	var asData atomic.Int32 // pages answered with their data, rendered by the browser
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(web.PackHeader) != "" {
			asData.Add(1)
		}
		h.ServeHTTP(w, r)
	})
	t.Cleanup(func() { app.Shutdown(context.Background()) })

	p := browser.New(t, counted).Page()
	p.Visit("/").Mark()
	p.WaitText("[data-note-count]", "1 note (via the API)")
	p.Eval(`new Promise((ok) => { const check = () => performance.getEntriesByName(location.origin + "/__fullstack/templates").length ? ok(true) : setTimeout(check, 20); check(); })`, nil)

	p.Click(`a[href="/login"]`).WaitPath("/login").WaitText("h1", "Log in")
	p.Fill(`input[name="email"]`, "ann@example.com").Fill(`input[name="password"]`, "wrong").Click("form button")
	p.WaitText("main", "do not match")
	p.Fill(`input[name="password"]`, "secret").Click("form button")
	p.WaitPath("/").WaitText("nav", "Log out")

	p.Fill(`input[name="text"]`, "from the e2e test").Click(`form[action="/notes"] button`)
	p.WaitText("main", "from the e2e test").WaitText("[data-note-count]", "2 notes")
	// The island mounts again with the new note after the form's page swap:
	// type into it only then, or the typing goes to the old one.
	p.WaitText(".note-search", "from the e2e test")
	p.Fill(".note-search input", "e2e")
	// The matching note was listed before the filter ran too: wait for the
	// list to shrink to it, not for its text.
	p.Eval(`new Promise((ok) => { const check = () => document.querySelectorAll(".note-search li").length === 1 ? ok(true) : setTimeout(check, 20); check(); })`, nil)
	if got := p.Texts(".note-search li"); len(got) != 1 || !strings.HasPrefix(got[0], "from the e2e test") {
		t.Fatal("island filter:", got)
	}
	if p.Title() != "Example" {
		t.Fatal(p.Title())
	}
	if p.Reloaded() {
		t.Fatal("a full page load: navigation did not swap pages in place")
	}
	if asData.Load() < 3 {
		t.Fatalf("%d pages came as data", asData.Load())
	}
}
