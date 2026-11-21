package apptest

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"fullstack"
	"fullstack/web"
	"passport"
	"passport/httpauth"
	"passport/jwt"
	"passport/memorystore"
)

// fakeT records failures instead of failing the real test.
type fakeT struct {
	testing.TB
	errors []string
	fatal  bool
}

type fatalStop struct{}

func (f *fakeT) Helper() {}
func (f *fakeT) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	f.fatal = true
	panic(fatalStop{})
}
func (f *fakeT) Fatal(args ...any) { f.Fatalf("%s", fmt.Sprint(args...)) }

// expectFail runs fn with a fakeT and checks that it reported want.
func expectFail(t *testing.T, want string, fn func(ft *fakeT)) {
	t.Helper()
	ft := &fakeT{}
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(fatalStop); !ok {
					panic(v)
				}
			}
		}()
		fn(ft)
	}()
	if len(ft.errors) == 0 || !strings.Contains(strings.Join(ft.errors, "\n"), want) {
		t.Errorf("want a failure with %q, got %q", want, ft.errors)
	}
}

func newApp(t testing.TB, withAuth bool) *fullstack.App {
	app, err := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	r := app.Router
	if withAuth {
		iss, _ := jwt.New(strings.Repeat("s", 32), "test")
		a := passport.New().UseSessionStore(memorystore.New()).UseTokenIssuer(iss)
		fullstack.Set(app, a)
		r.Use(httpauth.Middleware(a), httpauth.CSRF)
	}
	r.Get("/", func(c *web.Ctx) error {
		name := "guest"
		if id, ok := httpauth.FromContext(c.Context()); ok {
			name = id.Name + " via " + httpauth.GuardFrom(c.Context())
		}
		c.W.Header().Set("X-App", "demo")
		return c.Text(200, "<hello> "+name)
	})
	r.Post("/form", func(c *web.Ctx) error { return c.Text(200, "got "+c.R.FormValue("a")) })
	r.Post("/json", func(c *web.Ctx) error {
		var in map[string]any
		if err := c.DecodeJSON(&in); err != nil {
			return err
		}
		if in["name"] == "" {
			return &web.HTTPError{Status: 422, Message: "invalid", Fields: map[string]any{"errors": map[string]any{"name": []string{"required"}}}}
		}
		return c.JSON(201, map[string]any{"data": []any{map[string]any{"id": 7, "name": in["name"]}}, "meta": map[string]any{"total": 1}})
	})
	r.Put("/json", func(c *web.Ctx) error { return c.JSON(200, map[string]string{"method": c.R.Method}) })
	r.Patch("/json", func(c *web.Ctx) error { return c.JSON(200, map[string]string{"method": c.R.Method}) })
	r.Delete("/thing", func(c *web.Ctx) error { return c.NoContent() })
	r.Get("/go", func(c *web.Ctx) error { return c.Redirect(0, "/") })
	r.Get("/cookie", func(c *web.Ctx) error {
		http.SetCookie(c.W, &http.Cookie{Name: "flavor", Value: "oat"})
		return c.Text(200, "set")
	})
	r.Get("/forget", func(c *web.Ctx) error {
		http.SetCookie(c.W, &http.Cookie{Name: "flavor", MaxAge: -1})
		return c.Text(200, "forgot")
	})
	r.Get("/echo-header", func(c *web.Ctx) error { return c.Text(200, c.R.Header.Get("X-Test")) })
	return app
}

func TestClient(t *testing.T) {
	c := New(t, newApp(t, true))
	c.Get("/").AssertOK().AssertSee("<hello>", "guest").AssertDontSee("Ann").AssertHeader("X-App", "demo").AssertSuccessful().AssertCookie(httpauth.CSRFCookieName)
	if c.Cookie(httpauth.CSRFCookieName) == "" {
		t.Fatal("csrf cookie not kept")
	}
	c.Post("/form", url.Values{"a": {"1"}}).AssertOK().AssertSee("got 1")
	c.WithoutCSRF().Post("/form", url.Values{"a": {"1"}}).AssertForbidden()
	r := c.PostJSON("/json", map[string]any{"name": "Ann"}).AssertCreated().
		AssertJSONPath("data.0.name", "Ann").AssertJSONPath("data.0.id", 7).AssertJSONPath("meta", map[string]int{"total": 1}).
		AssertJSONCount("data", 1).AssertJSONCount("meta", 1).AssertJSONMissing("data.1").AssertJSONMissing("nope.x")
	var out struct {
		Data []struct{ ID int } `json:"data"`
	}
	r.DecodeJSON(&out)
	if out.Data[0].ID != 7 || r.Status() != 201 || !strings.Contains(r.Text(), "Ann") {
		t.Fatal(out)
	}
	if v, ok := r.JSONPath(""); !ok || v == nil {
		t.Fatal("whole document")
	}
	c.PostJSON("/json", map[string]any{"name": ""}).AssertUnprocessable().AssertJSONValidationErrors("name")
	c.PutJSON("/json", nil).AssertOK().AssertJSONPath("method", "PUT")
	c.PatchJSON("/json", nil).AssertJSONPath("method", "PATCH")
	c.Delete("/thing").AssertNoContent()
	c.Get("/go").AssertRedirect("/").AssertRedirect()
	c.Get("/nope").AssertNotFound()
	c.GetJSON("/nope").AssertNotFound().AssertJSONPath("error", "Not Found")
	c.Get("/cookie").AssertOK()
	if c.Cookie("flavor") != "oat" {
		t.Fatal("cookie")
	}
	c.Get("/forget")
	if c.Cookie("flavor") != "" {
		t.Fatal("deleted cookie kept")
	}
	c.SetCookie("flavor", "rye")
	if c.Cookie("flavor") != "rye" {
		t.Fatal("SetCookie")
	}
	c.WithHeader("X-Test", "yes").Get("/echo-header").AssertSee("yes")
	c.Get("/echo-header").AssertDontSee("yes") // the header was only on that client
	c.Do("POST", "/form", strings.NewReader("a=2"), "application/x-www-form-urlencoded").AssertSee("got 2")
}

func TestActingAs(t *testing.T) {
	c := New(t, newApp(t, true))
	c.ActingAs(&passport.Identity{ProviderUserID: "1", Name: "Ann"}).Get("/").AssertSee("Ann via web")
	c.Logout().Get("/").AssertSee("guest")
	c.ActingAsAPI(&passport.Identity{ProviderUserID: "2", Name: "Bot"}).Get("/").AssertSee("Bot via api")
	c.WithToken("bad").Get("/").AssertSee("guest")

	expectFail(t, "needs passport", func(ft *fakeT) {
		New(ft, newApp(t, false)).ActingAs(&passport.Identity{})
	})
	expectFail(t, "made with New", func(ft *fakeT) {
		NewHandler(ft, http.NotFoundHandler()).ActingAsAPI(&passport.Identity{})
	})
	noTokens, _ := fullstack.New(fullstack.Options{EnvFiles: []string{}, LogOutput: io.Discard})
	fullstack.Set(noTokens, passport.New())
	expectFail(t, "ActingAsAPI", func(ft *fakeT) { New(ft, noTokens).ActingAsAPI(&passport.Identity{}) })
	expectFail(t, "ActingAs:", func(ft *fakeT) { New(ft, noTokens).ActingAs(&passport.Identity{}) })
}

type badProvider struct{}

func (badProvider) Register(*fullstack.App) error { return nil }
func (badProvider) Boot(*fullstack.App) error     { return fmt.Errorf("boot broke") }

func TestFailures(t *testing.T) {
	app := newApp(t, false)
	h, _ := app.Handler()
	c := NewHandler(t, h)
	cases := map[string]func(ft *fakeT){
		"status 200, want 201":       func(ft *fakeT) { c.with(ft).Get("/").AssertCreated() },
		"want 2xx":                   func(ft *fakeT) { c.with(ft).Get("/nope").AssertSuccessful() },
		"want a redirect":            func(ft *fakeT) { c.with(ft).Get("/").AssertRedirect("/") },
		`redirect to "/", want "/x"`: func(ft *fakeT) { c.with(ft).Get("/go").AssertRedirect("/x") },
		`does not contain "zzz"`:     func(ft *fakeT) { c.with(ft).Get("/").AssertSee("zzz") },
		`the body contains "guest"`:  func(ft *fakeT) { c.with(ft).Get("/").AssertDontSee("guest") },
		`header X-App is "demo"`:     func(ft *fakeT) { c.with(ft).Get("/").AssertHeader("X-App", "other") },
		`no cookie "x"`:              func(ft *fakeT) { c.with(ft).Get("/").AssertCookie("x") },
		"not JSON":                   func(ft *fakeT) { var v any; c.with(ft).Get("/").DecodeJSON(&v) },
		`no JSON value at "a"`:       func(ft *fakeT) { c.with(ft).Get("/").AssertJSONPath("a", 1) },
		`is "x", want ["x"]`: func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONPath("data.0.name", []string{"x"})
		},
		`want "y"`: func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONPath("data.0.name", "y")
		},
		"encoding": func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONPath("data", func() {})
		},
		"has a value":         func(ft *fakeT) { c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONMissing("data") },
		"has 1 items, want 2": func(ft *fakeT) { c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONCount("data", 2) },
		"has 1 keys, want 3":  func(ft *fakeT) { c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONCount("meta", 3) },
		"not an array": func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONCount("data.0.id", 1)
		},
		`no JSON value at "zz"`: func(ft *fakeT) { c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONCount("zz", 1) },
		"no validation errors": func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": "x"}).AssertJSONValidationErrors("name")
		},
		`no validation error for "email"`: func(ft *fakeT) {
			c.with(ft).PostJSON("/json", map[string]any{"name": ""}).AssertJSONValidationErrors("name", "email")
		},
		"encoding the JSON body": func(ft *fakeT) { c.with(ft).PostJSON("/json", func() {}) },
		"booting":                func(ft *fakeT) { a := newApp(t, false); a.Register(badProvider{}); New(ft, a) },
	}
	for want, fn := range cases {
		expectFail(t, want, fn)
	}
	// A long body is cut in the failure message.
	expectFail(t, "…", func(ft *fakeT) {
		r := NewHandler(ft, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, strings.Repeat("x", 600)) }))
		r.Get("/").AssertNotFound()
	})
	for _, a := range []func(r *Response) *Response{
		(*Response).AssertOK, (*Response).AssertBadRequest, (*Response).AssertUnauthorized, (*Response).AssertForbidden,
	} {
		expectFail(t, "want", func(ft *fakeT) { a(c.with(ft).Get("/nope")) })
	}
}

// with returns a copy of the client reporting to ft.
func (c *Client) with(ft *fakeT) *Client {
	n := c.clone()
	n.t = ft
	return n
}
