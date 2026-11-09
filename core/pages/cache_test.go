package pages

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"fullstack"
	"fullstack/view"
	"fullstack/web"
)

func cacheSite(t *testing.T, o Options) (*web.Router, map[string]int) {
	t.Helper()
	fsys := fstest.MapFS{
		"views/pages/index.html":  page(`home {{.N}}`),
		"views/pages/about.html":  page(`about {{.N}}`),
		"views/pages/form.html":   page(`<form><input type="hidden" name="_csrf" value="{{.N}}"></form>`),
		"views/pages/go.html":     page(`never`),
		"views/pages/cookie.html": page(`cookie`),
	}
	v, err := view.New(fsys, view.Options{Dir: "views"})
	if err != nil {
		t.Fatal(err)
	}
	r := web.NewRouter()
	r.SetRenderer(v)
	calls := map[string]int{}
	load := func(name string) Loader {
		return func(c *web.Ctx) (any, error) {
			calls[name]++
			return map[string]any{"N": calls[name]}, nil
		}
	}
	o.Dir = "views/pages"
	o.Loaders = Loaders{"index": load("index"), "about": load("about"), "form": load("form"),
		"go": func(c *web.Ctx) (any, error) { calls["go"]++; return nil, c.Redirect(0, "/") },
		"cookie": func(c *web.Ctx) (any, error) {
			calls["cookie"]++
			http.SetCookie(c.W, &http.Cookie{Name: "x", Value: "1"})
			return nil, nil
		},
	}
	if _, err := Register(r, fsys, o); err != nil {
		t.Fatal(err)
	}
	return r, calls
}

func TestCache(t *testing.T) {
	store := NewStore(0)
	guest := true
	r, calls := cacheSite(t, Options{
		Store: store,
		Guest: func(c *web.Ctx) bool { return guest },
		Cache: map[string]Cache{
			"index":  {TTL: time.Hour},
			"about":  {Public: true, TTL: 50 * time.Millisecond, Vary: func(c *web.Ctx) string { return c.R.Header.Get("Accept-Language") }},
			"form":   {Public: true},
			"go":     {Public: true},
			"cookie": {Public: true},
		},
	})
	w := get(r, "GET", "/")
	etag := w.Header().Get("ETag")
	if w.Body.String() != "home 1" || etag == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal(w.Body.String(), w.Header())
	}
	if w := get(r, "GET", "/"); w.Body.String() != "home 1" || calls["index"] != 1 {
		t.Fatal("not kept", w.Body.String())
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("If-None-Match", "W/"+etag)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal(w.Code)
	}
	// page data (another representation) is kept apart
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set(web.PackHeader, "v9")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "home 2" {
		t.Fatal(w.Body.String())
	}
	// a user gets the page rendered for them
	guest = false
	if w := get(r, "GET", "/"); w.Body.String() != "home 3" || w.Header().Get("ETag") != "" {
		t.Fatal(w.Body.String())
	}
	guest = true
	store.Purge("index")
	if w := get(r, "GET", "/"); w.Body.String() != "home 4" {
		t.Fatal("purge", w.Body.String())
	}

	// public, varied, expiring
	guest = false
	get(r, "GET", "/about")
	if w := get(r, "GET", "/about"); w.Body.String() != "about 1" {
		t.Fatal(w.Body.String())
	}
	req = httptest.NewRequest("GET", "/about", nil)
	req.Header.Set("Accept-Language", "uk")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Body.String() != "about 2" {
		t.Fatal("vary", w.Body.String())
	}
	time.Sleep(60 * time.Millisecond)
	if w := get(r, "GET", "/about"); w.Body.String() != "about 3" {
		t.Fatal("ttl", w.Body.String())
	}

	// never kept: a CSRF field, a redirect, a cookie
	get(r, "GET", "/form")
	if w := get(r, "GET", "/form"); calls["form"] != 2 || w.Header().Get("ETag") != "" {
		t.Fatal("CSRF page kept")
	}
	get(r, "GET", "/go")
	if w := get(r, "GET", "/go"); calls["go"] != 2 || w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatal("redirect", w.Code, calls["go"])
	}
	get(r, "GET", "/cookie")
	if w := get(r, "GET", "/cookie"); calls["cookie"] != 2 || !strings.Contains(w.Header().Get("Set-Cookie"), "x=1") {
		t.Fatal("cookie page kept")
	}
	if store.Len() == 0 {
		t.Fatal("nothing kept")
	}
	store.Purge()
	if store.Len() != 0 {
		t.Fatal(store.Len())
	}
	// without Guest, only public pages
	r2, calls2 := cacheSite(t, Options{Cache: map[string]Cache{"index": {}}})
	get(r2, "GET", "/")
	get(r2, "GET", "/")
	if calls2["index"] != 2 {
		t.Fatal("kept without Guest")
	}
}

func TestStoreLimit(t *testing.T) {
	s := NewStore(10)
	s.put(&kept{key: "a", page: "a", body: []byte("123456")})
	s.put(&kept{key: "b", page: "b", body: []byte("123456")})
	s.put(&kept{key: "b", page: "b", body: []byte("12")})
	s.put(&kept{key: "big", body: make([]byte, 11)})
	if s.get("a") != nil || s.get("b") == nil || s.Len() != 1 {
		t.Fatal(s.Len())
	}
}

func TestPrerender(t *testing.T) {
	fsys := fstest.MapFS{
		"views/pages/index.html":      page(`home`),
		"views/pages/docs/intro.html": page(`intro {{.N}}`),
		"views/pages/notes/[id].html": page(`note`),
		"views/pages/private.html":    page(`private`),
	}
	t.Setenv("APP_ENV", "testing")
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	app.Stdout = &out
	var site []Page
	app.AddCommand(PrerenderCommand(&site))
	n := 0
	app.Register(provider(func(a *fullstack.App) error {
		v, err := view.New(fsys, view.Options{Dir: "views"})
		if err != nil {
			return err
		}
		a.Router.SetRenderer(v)
		site, err = Register(a.Router, fsys, Options{
			Loaders: Loaders{"docs/intro": func(c *web.Ctx) (any, error) { n++; return map[string]any{"N": n}, nil }},
			Cache:   map[string]Cache{"index": {Public: true}, "docs/intro": {Public: true}, "notes/[id]": {Public: true}},
		})
		return err
	}))
	dir := t.TempDir()
	if code, _ := app.Console([]string{"pages:prerender", dir}); code != 0 || !strings.Contains(out.String(), "Prerendered 2 pages") {
		t.Fatal(code, out.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "docs/intro/index.html"))
	if string(b) != "intro 1" {
		t.Fatal(string(b))
	}
	if _, err := os.Stat(filepath.Join(dir, "private/index.html")); err == nil {
		t.Fatal("private page prerendered")
	}
	// served as they are
	r := web.NewRouter()
	v, _ := view.New(fsys, view.Options{Dir: "views"})
	r.SetRenderer(v)
	if _, err := Register(r, fsys, Options{Prerendered: os.DirFS(dir),
		Loaders: Loaders{"docs/intro": func(c *web.Ctx) (any, error) { n++; return map[string]any{"N": n}, nil }},
		Cache:   map[string]Cache{"index": {Public: true}, "docs/intro": {Public: true}}}); err != nil {
		t.Fatal(err)
	}
	if w := get(r, "GET", "/docs/intro"); w.Body.String() != "intro 1" || w.Header().Get("ETag") == "" || n != 1 {
		t.Fatal(w.Body.String(), n)
	}
	// a failing page stops the command
	app2, _ := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	app2.Stdout, app2.Stderr = io.Discard, io.Discard
	bad := []Page{{Name: "x", Pattern: "/missing", Cache: &Cache{Public: true}}}
	app2.AddCommand(PrerenderCommand(&bad))
	if code, _ := app2.Console([]string{"pages:prerender", t.TempDir()}); code != 1 {
		t.Fatal(code)
	}
}

type provider func(*fullstack.App) error

func (p provider) Register(*fullstack.App) error { return nil }
func (p provider) Boot(a *fullstack.App) error   { return p(a) }
