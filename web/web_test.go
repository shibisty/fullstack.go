package web

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(h http.Handler, method, target string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func quietLogger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

type user struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func TestRouting(t *testing.T) {
	r := NewRouter()
	log, logs := quietLogger()
	r.SetLogger(log)
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, req)
			})
		}
	}
	r.Use(Recover(), RequestID(), AccessLog(log), mark("global"))
	r.Get("/", func(c *Ctx) error { return c.Text(200, "home") }).Name("home")
	r.Post("/echo", func(c *Ctx) error {
		var u user
		if err := c.DecodeJSON(&u); err != nil {
			return err
		}
		return c.JSON(201, u)
	})
	r.Group("/api", func(api *Router) {
		api.Use(mark("api"))
		api.Get("/", func(c *Ctx) error { return c.Text(200, "api root") })
		api.Get("/users/{id}", func(c *Ctx) error {
			id, err := c.ParamInt("id")
			if err != nil {
				return err
			}
			return c.JSON(200, map[string]any{"id": id, "q": c.Query("q")})
		}, mark("route")).Name("users.show")
		api.Group("/admin", func(adm *Router) {
			adm.Delete("/users/{id}", func(c *Ctx) error { return c.NoContent() })
		}, mark("admin"))
		api.Put("/p", func(c *Ctx) error { return c.Text(200, "put") })
		api.Patch("/p", func(c *Ctx) error { return c.Text(200, "patch") })
		api.Any("/any", func(c *Ctx) error { return c.Text(200, c.R.Method) })
	})
	r.Get("/files/{path...}", func(c *Ctx) error { return c.Text(200, c.Param("path")) }).Name("files")
	r.Get("/panic", func(c *Ctx) error { panic("boom") })
	r.Get("/fail", func(c *Ctx) error { return errors.New("db down") })
	r.Get("/teapot", func(c *Ctx) error { return Error(418, "short and stout") })
	r.Get("/go", func(c *Ctx) error { return c.Redirect(0, "/") })
	r.HandleHTTP("GET", "/plain", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "plain") }))
	sub := http.NewServeMux()
	sub.HandleFunc("/x", func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "mounted "+req.URL.Path) })
	r.Mount("/legacy", sub)

	if w := do(r, "GET", "/", nil); w.Code != 200 || w.Body.String() != "home" || w.Header().Get(RequestIDHeader) == "" {
		t.Fatalf("home: %d %q %v", w.Code, w.Body, w.Header())
	}
	order = nil
	w := do(r, "GET", "/api/users/7?q=x", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":7`) || !strings.Contains(w.Body.String(), `"q":"x"`) {
		t.Fatalf("user: %d %s", w.Code, w.Body)
	}
	if strings.Join(order, ",") != "global,api,route" {
		t.Fatalf("middleware order %v", order)
	}
	order = nil
	if w := do(r, "DELETE", "/api/admin/users/1", nil); w.Code != 204 || strings.Join(order, ",") != "global,api,admin" {
		t.Fatalf("admin: %d %v", w.Code, order)
	}
	if w := do(r, "GET", "/api", nil); w.Body.String() != "api root" {
		t.Fatalf("group root %q", w.Body)
	}
	if w := do(r, "GET", "/api/users/abc", nil); w.Code != 404 {
		t.Fatalf("non-numeric id %d", w.Code)
	}
	for m, want := range map[string]string{"PUT": "put", "PATCH": "patch"} {
		if w := do(r, m, "/api/p", nil); w.Body.String() != want {
			t.Errorf("%s %q", m, w.Body)
		}
	}
	if w := do(r, "OPTIONS", "/api/any", nil); w.Body.String() != "OPTIONS" {
		t.Errorf("any %q", w.Body)
	}
	if w := do(r, "GET", "/files/a/b.txt", nil); w.Body.String() != "a/b.txt" {
		t.Errorf("wildcard %q", w.Body)
	}
	if w := do(r, "GET", "/plain", nil); w.Body.String() != "plain" {
		t.Errorf("plain %q", w.Body)
	}
	if w := do(r, "GET", "/legacy/x", nil); w.Body.String() != "mounted /x" {
		t.Errorf("mount %q", w.Body)
	}
	if w := do(r, "GET", "/go", nil); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Errorf("redirect %d", w.Code)
	}

	// JSON bodies.
	if w := do(r, "POST", "/echo", strings.NewReader(`{"id":1,"name":"Ann"}`)); w.Code != 201 || !strings.Contains(w.Body.String(), `"Ann"`) || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("echo %d %s", w.Code, w.Body)
	}
	for body, want := range map[string]string{
		``:                      "empty request body",
		`{"id":1,"extra":true}`: "unknown field",
		`{"id":1} {}`:           "trailing data",
		`{"id":`:                "invalid JSON",
	} {
		w := do(r, "POST", "/echo", strings.NewReader(body), "Content-Type", "application/json")
		if w.Code != 400 || !strings.Contains(w.Body.String(), want) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%q: %d %s", body, w.Code, w.Body)
		}
	}
	old := MaxBodyBytes
	MaxBodyBytes = 8
	if w := do(r, "POST", "/echo", strings.NewReader(`{"name":"a long name"}`)); w.Code != 413 {
		t.Errorf("too large %d", w.Code)
	}
	MaxBodyBytes = old

	// Errors.
	logs.Reset()
	if w := do(r, "GET", "/fail", nil); w.Code != 500 || strings.Contains(w.Body.String(), "db down") || !strings.Contains(logs.String(), "db down") {
		t.Fatalf("fail: %d %q\n%s", w.Code, w.Body, logs)
	}
	logs.Reset()
	if w := do(r, "GET", "/panic", nil, "Accept", "application/json"); w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"Internal Server Error"`) || !strings.Contains(logs.String(), "boom") {
		t.Fatalf("panic: %d %q\n%s", w.Code, w.Body, logs)
	}
	if w := do(r, "GET", "/teapot", nil); w.Code != 418 || w.Body.String() != "short and stout" {
		t.Fatalf("teapot %d %q", w.Code, w.Body)
	}
	if w := do(r, "GET", "/nope", nil, "Accept", "application/json"); w.Code != 404 || !strings.Contains(w.Body.String(), `"error":"Not Found"`) {
		t.Fatalf("404 %d %q", w.Code, w.Body)
	}
	if w := do(r, "POST", "/", nil); w.Code != 405 || w.Header().Get("Allow") == "" || w.Body.String() != "Method Not Allowed" {
		t.Fatalf("405 %d %q %v", w.Code, w.Body, w.Header())
	}
	if !strings.Contains(logs.String(), "status=404") {
		t.Errorf("access log:\n%s", logs)
	}

	// Named routes.
	for _, c := range []struct {
		name   string
		params []any
		want   string
	}{
		{"home", nil, "/"},
		{"users.show", []any{"id", 7}, "/api/users/7"},
		{"users.show", []any{"id", "a b", "tab", "x"}, "/api/users/a%20b?tab=x"},
		{"files", []any{"path", "a b/c"}, "/files/a%20b/c"},
	} {
		if got, err := r.URL(c.name, c.params...); err != nil || got != c.want {
			t.Errorf("URL(%s) = %q %v", c.name, got, err)
		}
	}
	for _, bad := range [][]any{{"nope"}, {"users.show"}, {"users.show", "id"}} {
		if _, err := r.URL(bad[0].(string), bad[1:]...); err == nil {
			t.Errorf("URL%v accepted", bad)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("duplicate route name accepted")
		}
	}()
	r.Get("/other", func(*Ctx) error { return nil }).Name("home")
}

func TestBindingAndCustomErrors(t *testing.T) {
	r := NewRouter()
	r.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	users := map[string]*user{"1": {ID: 1, Name: "Ann"}}
	r.Bind("user", func(req *http.Request, v string) (any, error) {
		if u, ok := users[v]; ok {
			return u, nil
		}
		return nil, ErrNotFound
	})
	r.Get("/users/{user}", func(c *Ctx) error {
		u, err := Model[*user](c, "user")
		if err != nil {
			return err
		}
		if _, ok := c.Bound("user"); !ok {
			return errors.New("not bound")
		}
		if _, err := Model[string](c, "user"); err == nil {
			return errors.New("wrong type accepted")
		}
		if _, err := Model[*user](c, "other"); err == nil {
			return errors.New("unbound name accepted")
		}
		return c.Text(200, u.Name)
	})
	r.Get("/raw/{id}", func(c *Ctx) error { // no binder for id
		_, ok := c.Bound("id")
		return c.Text(200, c.Param("id")+" "+map[bool]string{true: "bound", false: "raw"}[ok])
	})
	if w := do(r, "GET", "/users/1", nil); w.Body.String() != "Ann" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if w := do(r, "GET", "/users/2", nil); w.Code != 404 {
		t.Fatalf("missing model %d", w.Code)
	}
	if w := do(r, "GET", "/raw/5", nil); w.Body.String() != "5 raw" {
		t.Fatalf("%q", w.Body)
	}

	r.OnError(func(c *Ctx, err error) {
		_ = c.JSON(StatusOf(err), map[string]any{"custom": err.Error()})
	})
	if w := do(r, "GET", "/users/9", nil); w.Code != 404 || !strings.Contains(w.Body.String(), "custom") {
		t.Fatalf("custom handler %d %q", w.Code, w.Body)
	}
	if w := do(r, "GET", "/none", nil); !strings.Contains(w.Body.String(), "custom") {
		t.Fatalf("custom 404 %q", w.Body)
	}
}

func TestErrors(t *testing.T) {
	e := &HTTPError{Status: 422, Message: "invalid", Err: errors.New("cause"), Fields: map[string]any{"errors": map[string]string{"email": "required"}}}
	if e.Error() != "422 invalid: cause" || !errors.Is(e, e.Err) || StatusOf(e) != 422 {
		t.Fatal(e.Error())
	}
	if ErrNotFound.Error() != "404 Not Found" || Wrap(503, errors.New("x")).Status != 503 || StatusOf(errors.New("x")) != 500 {
		t.Fatal()
	}
	r := NewRouter()
	r.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Get("/v", func(*Ctx) error { return e })
	r.Get("/late", func(c *Ctx) error {
		c.Text(200, "partial")
		return errors.New("after write")
	})
	if w := do(r, "GET", "/v", nil, "Accept", "application/json"); w.Code != 422 || !strings.Contains(w.Body.String(), `"email":"required"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := do(r, "GET", "/late", nil); w.Code != 200 || w.Body.String() != "partial" {
		t.Fatalf("an error after the response started must not write again: %d %q", w.Code, w.Body)
	}
}

type fakeRenderer struct{ fail bool }

func (f fakeRenderer) Render(w io.Writer, name string, data any) error {
	if f.fail {
		return errors.New("template broke")
	}
	_, err := io.WriteString(w, "<p>"+name+"</p>")
	return err
}

func TestHTML(t *testing.T) {
	r := NewRouter()
	r.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Get("/page", func(c *Ctx) error { return c.HTML(200, "home", nil) })
	if w := do(r, "GET", "/page", nil); w.Code != 500 {
		t.Fatalf("no renderer: %d", w.Code)
	}
	r.SetRenderer(fakeRenderer{})
	if w := do(r, "GET", "/page", nil); w.Body.String() != "<p>home</p>" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%q", w.Body)
	}
	r.SetRenderer(fakeRenderer{fail: true})
	if w := do(r, "GET", "/page", nil); w.Code != 500 || strings.Contains(w.Body.String(), "<p>") {
		t.Fatalf("broken template: %d %q", w.Code, w.Body)
	}
}

func TestMiddleware(t *testing.T) {
	// RequestID keeps a sane incoming id and replaces a bad one.
	h := RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, RequestIDOf(r)) }))
	if w := do(h, "GET", "/", nil, RequestIDHeader, "abc-123"); w.Body.String() != "abc-123" {
		t.Fatalf("%q", w.Body)
	}
	if w := do(h, "GET", "/", nil, RequestIDHeader, "bad id!"); w.Body.String() == "bad id!" || len(w.Body.String()) != 16 {
		t.Fatalf("%q", w.Body)
	}
	if saneID(strings.Repeat("a", 65)) {
		t.Fatal("long id")
	}
	// SecureHeaders.
	sh := SecureHeaders(true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	if w := do(sh, "GET", "/", nil); w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal(w.Header())
	}
	// Recover outside a Router, and ErrAbortHandler passes through.
	rec := Recover()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") }))
	if w := do(rec, "GET", "/", nil); w.Code != 500 {
		t.Fatal(w.Code)
	}
	abort := Recover()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("ErrAbortHandler swallowed")
			}
		}()
		do(abort, "GET", "/", nil)
	}()
	// AccessLog outside a Router, with the default logger.
	al := AccessLog(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	if w := do(al, "GET", "/", nil); w.Code != 503 {
		t.Fatal(w.Code)
	}
	// Routes lists what was registered, with names.
	lr := NewRouter()
	lr.Get("/a", func(*Ctx) error { return nil }).Name("a")
	lr.Post("/a", func(*Ctx) error { return nil })
	lr.HandleHTTP("", "/h", http.NotFoundHandler())
	lr.Mount("/m", http.NotFoundHandler())
	if rs := lr.Routes(); len(rs) != 4 || rs[0] != (RouteInfo{Method: "GET", Path: "/a", Name: "a"}) || rs[1].Name != "" || rs[3].Path != "/m/" {
		t.Fatalf("%+v", rs)
	}
	// Fail: through the router's ErrorHandler, or http.Error outside one.
	if w := do(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Fail(w, r, ErrForbidden) }), "GET", "/", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	fr := NewRouter()
	fr.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { Fail(w, r, Error(401, "log in")) })
	})
	if w := do(fr, "GET", "/x", nil, "Accept", "application/json"); w.Code != 401 || !strings.Contains(w.Body.String(), `"error":"log in"`) {
		t.Fatal(w.Code, w.Body)
	}
	// FromRequest outside a Router.
	if _, ok := FromRequest(httptest.NewRequest("GET", "/", nil)); ok {
		t.Fatal("ctx outside a router")
	}
}

func TestStatusWriterAndCtx(t *testing.T) {
	r := NewRouter()
	r.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.Get("/w", func(c *Ctx) error {
		if c.Written() {
			return errors.New("written early")
		}
		if u, err := c.URL("w"); err != nil || u != "/w" {
			return errors.New("url")
		}
		if err := http.NewResponseController(c.W).Flush(); err != nil {
			return err // Unwrap reaches the recorder's Flush
		}
		io.WriteString(c.W, "body")
		if !c.Written() {
			return errors.New("not written")
		}
		return nil
	}).Name("w")
	if w := do(r, "GET", "/w", nil); w.Code != 200 || w.Body.String() != "body" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
}

type fakeClient struct{}

func (fakeClient) Render(w io.Writer, name string, data any) error {
	_, err := io.WriteString(w, "<p>"+name+"</p>")
	return err
}
func (fakeClient) Version() string { return "v1" }
func (fakeClient) ClientData(name string, data any) ([]byte, bool) {
	return []byte(`{"view":"` + name + `"}`), name != "server-only"
}
func (fakeClient) ServePack(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "pack") }

func TestClientRenderer(t *testing.T) {
	r := NewRouter()
	r.SetRenderer(fakeClient{})
	r.SetRenderer(fakeClient{}) // the pack route once
	r.Get("/p/{name}", func(c *Ctx) error { return c.HTML(201, c.Param("name"), nil) })
	if w := do(r, "GET", PackPath, nil); w.Body.String() != "pack" {
		t.Fatal(w.Body.String())
	}
	w := do(r, "GET", "/p/home", nil)
	if w.Body.String() != "<p>home</p>" || w.Header().Get(PackHeader) != "v1" || w.Header().Get("Vary") != PackHeader {
		t.Fatal(w.Body.String(), w.Header())
	}
	w = do(r, "GET", "/p/home", nil, PackHeader, "v1")
	if w.Code != 201 || w.Body.String() != `{"view":"home"}` || w.Header().Get("Content-Type") != PageJSON {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	// another version, a page the browser skips, one only the server renders
	for _, hdr := range [][]string{{PackHeader, "v0"}, {PackHeader, "v1", PackHeader + "-Skip", "a, home"}} {
		if w := do(r, "GET", "/p/home", nil, hdr...); w.Body.String() != "<p>home</p>" {
			t.Fatal(hdr, w.Body.String())
		}
	}
	if w := do(r, "GET", "/p/server-only", nil, PackHeader, "v1"); w.Body.String() != "<p>server-only</p>" {
		t.Fatal(w.Body.String())
	}
}
