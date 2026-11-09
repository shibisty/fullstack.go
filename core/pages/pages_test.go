package pages

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"fullstack/view"
	"fullstack/web"
)

func page(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

var site = fstest.MapFS{
	"views/layouts/app.html":           page(`{{define "layout"}}<main>{{block "content" .}}{{end}}</main>{{end}}`),
	"views/pages/index.html":           page(`{{define "content"}}home {{.User}} {{len .Notes}}{{end}}`),
	"views/pages/about.html":           page(`about`),
	"views/pages/notes/index.html":     page(`notes {{.Flash}}`),
	"views/pages/notes/[note].html":    page(`note {{.Params.note}} {{.Data}}`),
	"views/pages/notes/new.html":       page(`new note`),
	"views/pages/docs/[...path].html":  page(`doc {{.Params.path}}`),
	"views/pages/(auth)/login.html":    page(`login`),
	"views/pages/_form.html":           page(`{{define "form"}}form{{end}}`),
	"views/pages/_drafts/secret.html":  page(`secret`),
	"views/pages/notes/readme.txt":     page(`not a page`),
	"views/pages/users/[id]/edit.html": page(`edit {{.Params.id}}`),
	"views/pages/redirect.html":        page(`never`),
}

func newSite(t *testing.T, o Options) (*web.Router, []Page) {
	t.Helper()
	v, err := view.New(site, view.Options{Dir: "views"})
	if err != nil {
		t.Fatal(err)
	}
	r := web.NewRouter()
	r.SetRenderer(v)
	list, err := Register(r, site, o)
	if err != nil {
		t.Fatal(err)
	}
	return r, list
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func TestRoutes(t *testing.T) {
	r, list := newSite(t, Options{
		Shared: func(c *web.Ctx) map[string]any { return map[string]any{"User": "ann", "Flash": "-"} },
		Loaders: Loaders{
			"index": func(c *web.Ctx) (any, error) { return map[string]any{"Notes": []int{1, 2}}, nil },
			"notes/[note]": func(c *web.Ctx) (any, error) {
				if c.R.PathValue("note") == "9" {
					return nil, web.ErrNotFound
				}
				return struct{ Title string }{"t" + c.R.PathValue("note")}, nil
			},
			"notes/index": func(c *web.Ctx) (any, error) { return map[string]any{"Flash": "saved"}, nil },
			"redirect":    func(c *web.Ctx) (any, error) { return nil, c.Redirect(0, "/about") },
		},
		Actions: Actions{"notes/index": func(c *web.Ctx) error { return c.Text(201, "created") }},
	})
	var got []string
	for _, p := range list {
		got = append(got, p.Name+" "+p.Pattern+" "+p.View)
	}
	want := []string{
		"(auth)/login /login pages/(auth)/login",
		"about /about pages/about",
		"docs/[...path] /docs/{path...} pages/docs/[...path]",
		"index / pages/index",
		"notes/[note] /notes/{note} pages/notes/[note]",
		"notes/index /notes pages/notes/index",
		"notes/new /notes/new pages/notes/new",
		"redirect /redirect pages/redirect",
		"users/[id]/edit /users/{id}/edit pages/users/[id]/edit",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/", 200, "<main>home ann 2</main>"},
		{"GET", "/about", 200, "about"},
		{"GET", "/notes", 200, "notes saved"},
		{"POST", "/notes", 201, "created"},
		{"GET", "/notes/new", 200, "new note"},
		{"GET", "/notes/5", 200, "note 5 {t5}"},
		{"GET", "/notes/9", 404, ""},
		{"GET", "/docs/a/b/c", 200, "doc a/b/c"},
		{"GET", "/login", 200, "login"},
		{"GET", "/users/7/edit", 200, "edit 7"},
		{"GET", "/redirect", 303, ""},
		{"GET", "/_form", 404, ""},
		{"GET", "/secret", 404, ""},
		{"GET", "/nope", 404, ""},
		{"POST", "/about", 405, ""},
	} {
		w := get(r, c.method, c.path)
		if w.Code != c.code || (c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, w.Body.String())
		}
	}
	// Routes are named after their pages.
	if u, err := r.URL("notes/[note]", "note", 3); err != nil || u != "/notes/3" {
		t.Fatal(u, err)
	}
	if len(r.Routes()) != 11 { // 9 pages, an action and the template pack
		t.Fatal(r.Routes())
	}
}

func TestOptions(t *testing.T) {
	// Another folder and view prefix, no names, middleware.
	fsys := fstest.MapFS{"web/site/index.html": page(`hi`), "web/site/a/b.html": page(`b`)}
	v, _ := view.New(fsys, view.Options{Dir: "web"})
	r := web.NewRouter()
	r.SetRenderer(v)
	var seen int
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { seen++; next.ServeHTTP(w, req) })
	}
	list, err := Register(r, fsys, Options{Dir: "web/site/", Name: func(string) string { return "" }, Middleware: []web.Middleware{mw},
		PageMiddleware: map[string][]web.Middleware{"a/b": {mw}}})
	if err != nil || len(list) != 2 || list[0].View != "site/a/b" {
		t.Fatal(list, err)
	}
	if w := get(r, "GET", "/a/b"); w.Body.String() != "b" || seen != 2 {
		t.Fatal(w.Body.String(), seen)
	}
	if w := get(r, "GET", "/"); w.Body.String() != "hi" || seen != 3 {
		t.Fatal(w.Body.String(), seen)
	}
	if _, err := r.URL("index"); err == nil {
		t.Fatal("named")
	}
	// In a group: under its prefix.
	g := web.NewRouter()
	g.SetRenderer(v)
	g.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	empty := ""
	var gl []Page
	g.Group("/app", func(sub *web.Router) {
		gl, err = Register(sub, fsys, Options{Dir: "web/site", View: &empty})
	})
	if err != nil || gl[1].View != "index" {
		t.Fatal(gl, err)
	}
	if w := get(g, "GET", "/app/a/b"); w.Code != 500 { // the view is "a/b" now: not in the engine
		t.Fatal(w.Code)
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"are both /a":           {"p/a.html": page(""), "p/a/index.html": page("")},
		"are both /x/{z}":       {"p/x/[y].html": page(""), "p/x/[z].html": page("")},
		"must be the last part": {"p/[...a]/b.html": page("")},
		"twice":                 {"p/[a]/[a].html": page("")},
		"is not a path segment": {"p/a[b].html": page("")},
		"no folder":             {"q/a.html": page("")},
	}
	for want, fsys := range cases {
		if _, err := Find(fsys, Options{Dir: "p"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	fsys := fstest.MapFS{"p/a.html": page("")}
	_, err := Register(web.NewRouter(), fsys, Options{Dir: "p", Loaders: Loaders{"b": nil}})
	if err == nil || !strings.Contains(err.Error(), `"b" has a loader, an action, middleware or a cache policy but no file p/b.html (pages: a)`) {
		t.Fatal(err)
	}
	_, err = Register(web.NewRouter(), fsys, Options{Dir: "p", Actions: Actions{"c": nil}})
	if err == nil || !strings.Contains(err.Error(), `"c"`) {
		t.Fatal(err)
	}
	_, err = Register(web.NewRouter(), fsys, Options{Dir: "p", PageMiddleware: map[string][]web.Middleware{"d": nil}})
	if err == nil || !strings.Contains(err.Error(), `"d"`) {
		t.Fatal(err)
	}
}
