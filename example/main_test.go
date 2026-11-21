package main

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fullstack"
	auth "fullstack/passport"
	apptest "fullstack/testing"
	"passport"
	"passport/local"
)

func newApp(t *testing.T) *fullstack.App {
	t.Setenv("APP_NAME", "Example")
	t.Setenv("APP_ENV", "testing") // no frontend build needed
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Register(assetsProvider(), &auth.Provider{Strategies: []passport.Strategy{local.New(verifyUser)}}, Routes{}); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestPagesAndAPI(t *testing.T) {
	c := apptest.New(t, newApp(t))
	c.Get("/").AssertOK().AssertSee("Hello from fullstack", "<title>Example</title>").AssertDontSee("Log out")
	c.GetJSON("/api/notes").AssertOK().AssertJSONCount("", 1).AssertJSONPath("0.text", "Hello from fullstack")
	c.GetJSON("/api/notes/1").AssertOK().AssertJSONPath("id", 1)
	c.GetJSON("/api/notes/9").AssertNotFound()
	c.PostJSON("/api/notes", map[string]any{"text": ""}).AssertUnprocessable().AssertJSONValidationErrors("text")
	c.PostJSON("/api/notes", map[string]any{"text": "via api"}).AssertCreated().AssertJSONPath("text", "via api")
}

func TestLogin(t *testing.T) {
	c := apptest.New(t, newApp(t))
	c.Get("/login").AssertOK() // sets the CSRF cookie
	c.Post("/notes", url.Values{"text": {"x"}}).AssertUnauthorized()
	c.WithoutCSRF().Post("/login", url.Values{"email": {"ann@example.com"}, "password": {"secret"}}).AssertForbidden()
	c.Post("/login", url.Values{"email": {"ann@example.com"}, "password": {"wrong"}}).AssertUnprocessable().AssertSee("do not match")
	c.Post("/login", url.Values{"email": {"ann@example.com"}, "password": {"secret"}}).AssertRedirect("/")
	c.Post("/notes", url.Values{"text": {"second"}}).AssertRedirect("/")
	c.Get("/").AssertSee("second", "Log out", "Ann")
	c.Get("/login").AssertRedirect("/") // guests only
}

func TestAssetsBuild(t *testing.T) {
	app := newApp(t)
	var out strings.Builder
	app.Stdout, app.Stderr = &out, &out
	dir := t.TempDir()
	p := assetsProvider()
	p.OutDir = "build-test-" + filepath.Base(dir) // a scratch output inside the example
	t.Cleanup(func() { os.RemoveAll(p.OutDir) })
	app.Register(p)
	if code, _ := app.Console([]string{"assets:build"}); code != 0 {
		t.Fatal(out.String())
	}
	data, err := os.ReadFile(filepath.Join(p.OutDir, "manifest.json"))
	if err != nil || !strings.Contains(string(data), "resources/js/app.ts") {
		t.Fatal(err, string(data))
	}
}

func TestActingAs(t *testing.T) {
	c := apptest.New(t, newApp(t))
	c.Get("/")
	c.ActingAs(&passport.Identity{ProviderUserID: "7", Name: "Zoe"}).Post("/notes", url.Values{"text": {"from Zoe"}}).AssertRedirect()
	c.Get("/").AssertSee("Zoe", "from Zoe")
}
