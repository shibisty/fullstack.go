// Package web is the HTTP layer of fullstack: a router on net/http's
// ServeMux (Go 1.22 patterns) with groups, middleware, model binding and
// named routes; handlers that return errors; JSON, text, HTML and redirect
// helpers.
//
//	r := web.NewRouter()
//	r.Use(web.Recover(), web.RequestID(), web.AccessLog(nil))
//	r.Get("/", home)
//	r.Group("/api", func(api *web.Router) {
//	    api.Use(auth)
//	    api.Get("/users/{user}", showUser).Name("users.show")
//	})
//	http.ListenAndServe(":8080", r)
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Handler handles a request; a returned error is answered by the router's
// ErrorHandler (an HTTPError with its status, anything else as a 500).
type Handler func(c *Ctx) error

// Middleware is a standard net/http middleware, so that any of the
// ecosystem's (passport's guards, CSRF, rate limits) plugs in as is.
type Middleware func(http.Handler) http.Handler

// Binder resolves a path parameter to a value (a model loaded by its id).
// Return ErrNotFound (or an HTTPError) for a value that does not exist.
type Binder func(r *http.Request, value string) (any, error)

// shared is the state of a router and all its groups.
type shared struct {
	mux     *http.ServeMux
	mu      sync.RWMutex
	global  []Middleware
	chain   http.Handler // mux wrapped in global; rebuilt after Use on the root
	binders map[string]Binder
	names   map[string]string // route name → path pattern
	routes  []RouteInfo
	render  Renderer
	log     *slog.Logger
	onError ErrorHandler
}

// Router registers routes. The root router's middleware (Use) wraps every
// request, unmatched ones included; a group's middleware wraps the routes
// registered in it after Use.
type Router struct {
	s      *shared
	prefix string
	mw     []Middleware
	root   bool
}

// NewRouter returns an empty router.
func NewRouter() *Router {
	return &Router{s: &shared{mux: http.NewServeMux(), binders: map[string]Binder{}, names: map[string]string{}}, root: true}
}

// Use adds middleware: on the root router, for every request; on a group,
// for the routes registered after it in that group and its subgroups.
func (r *Router) Use(mw ...Middleware) {
	if r.root {
		r.s.mu.Lock()
		r.s.global = append(r.s.global, mw...)
		r.s.chain = nil
		r.s.mu.Unlock()
		return
	}
	r.mw = append(r.mw, mw...)
}

// Group calls fn with a router for the routes under prefix, with extra
// middleware, and returns it.
func (r *Router) Group(prefix string, fn func(g *Router), mw ...Middleware) *Router {
	g := &Router{s: r.s, prefix: joinPath(r.prefix, prefix)}
	if !r.root {
		g.mw = append(g.mw, r.mw...)
	}
	g.mw = append(g.mw, mw...)
	if fn != nil {
		fn(g)
	}
	return g
}

// Bind registers a Binder for the path parameter name, in every route:
// {user} in "/users/{user}" is resolved before the handler, which reads it
// with Model or Ctx.Bound.
func (r *Router) Bind(name string, b Binder) {
	r.s.mu.Lock()
	r.s.binders[name] = b
	r.s.mu.Unlock()
}

// SetRenderer sets the template renderer of Ctx.HTML.
// A ClientRenderer's template pack is served at PackPath.
func (r *Router) SetRenderer(rd Renderer) {
	r.s.render = rd
	if _, ok := rd.(ClientRenderer); ok {
		r.s.mu.RLock()
		_, served := r.s.names["fullstack.templates"]
		r.s.mu.RUnlock()
		if !served {
			r.HandleHTTP(http.MethodGet, PackPath, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if cr, ok := r.s.render.(ClientRenderer); ok {
					cr.ServePack(w, req)
					return
				}
				http.NotFound(w, req)
			})).Name("fullstack.templates")
		}
	}
}

// SetLogger sets the logger of Ctx.Logger and the error handler.
func (r *Router) SetLogger(l *slog.Logger) { r.s.log = l }

// OnError replaces the handler of returned errors (DefaultErrorHandler).
func (r *Router) OnError(h ErrorHandler) { r.s.onError = h }

func (r *Router) logger() *slog.Logger {
	if r.s.log != nil {
		return r.s.log
	}
	return slog.Default()
}

func (r *Router) renderer() Renderer { return r.s.render }

func (r *Router) handleError(c *Ctx, err error) {
	if h := r.s.onError; h != nil {
		h(c, err)
		return
	}
	DefaultErrorHandler(c, err)
}

// Route is a registered route; Name names it for URL.
type Route struct {
	r    *Router
	path string
	idx  int
}

// Name names the route for Router.URL ("users.show"). Names are unique.
func (rt *Route) Name(name string) *Route {
	rt.r.s.mu.Lock()
	defer rt.r.s.mu.Unlock()
	if old, dup := rt.r.s.names[name]; dup {
		panic(fmt.Sprintf("web: route name %q is already used by %s", name, old))
	}
	rt.r.s.names[name] = rt.path
	rt.r.s.routes[rt.idx].Name = name
	return rt
}

// Get registers a GET (and HEAD) route.
func (r *Router) Get(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle(http.MethodGet, path, h, mw...)
}

// Post registers a POST route.
func (r *Router) Post(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle(http.MethodPost, path, h, mw...)
}

// Put registers a PUT route.
func (r *Router) Put(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle(http.MethodPut, path, h, mw...)
}

// Patch registers a PATCH route.
func (r *Router) Patch(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle(http.MethodPatch, path, h, mw...)
}

// Delete registers a DELETE route.
func (r *Router) Delete(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle(http.MethodDelete, path, h, mw...)
}

// Any registers a route for every method.
func (r *Router) Any(path string, h Handler, mw ...Middleware) *Route {
	return r.Handle("", path, h, mw...)
}

// Handle registers a route for a method ("" for any). The path is a
// ServeMux pattern relative to the group: "/users/{id}", "/files/{path...}".
// A path is matched exactly: "/" is only the home page (ServeMux would match
// everything under it), and in a group "/" is the group's own path.
func (r *Router) Handle(method, path string, h Handler, mw ...Middleware) *Route {
	full := exact(joinPath(r.prefix, path))
	params := wildcards(full)
	final := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c, ok := FromRequest(req)
		if !ok { // served by a handler outside a Router (tests)
			c = &Ctx{router: r}
		}
		c.W, c.R = w, req
		if err := r.bind(c, params); err != nil {
			r.handleError(c, err)
			return
		}
		if err := h(c); err != nil {
			r.handleError(c, err)
		}
	})
	idx := r.register(method, full, chain(final, append(append([]Middleware(nil), r.mw...), mw...)))
	return &Route{r: r, path: full, idx: idx}
}

// HandleHTTP registers a plain http.Handler (a method may be "").
func (r *Router) HandleHTTP(method, path string, h http.Handler, mw ...Middleware) *Route {
	full := exact(joinPath(r.prefix, path))
	idx := r.register(method, full, chain(h, append(append([]Middleware(nil), r.mw...), mw...)))
	return &Route{r: r, path: full, idx: idx}
}

// Mount serves everything under prefix with h, which sees paths with the
// prefix stripped (another router, a file server, a third-party handler).
func (r *Router) Mount(prefix string, h http.Handler, mw ...Middleware) {
	full := strings.TrimSuffix(joinPath(r.prefix, prefix), "/")
	r.register("", full+"/", chain(http.StripPrefix(full, h), append(append([]Middleware(nil), r.mw...), mw...)))
}

// RouteInfo describes a registered route (routes:list).
type RouteInfo struct {
	Method string // "" for any method
	Path   string
	Name   string
}

// Routes lists the registered routes in registration order, with their names.
func (r *Router) Routes() []RouteInfo {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	return append([]RouteInfo(nil), r.s.routes...)
}

func (r *Router) register(method, path string, h http.Handler) int {
	r.s.mu.Lock()
	r.s.routes = append(r.s.routes, RouteInfo{Method: method, Path: path})
	idx := len(r.s.routes) - 1
	r.s.mu.Unlock()
	pattern := path
	if method != "" {
		pattern = method + " " + path
	}
	r.s.mux.Handle(pattern, h)
	return idx
}

func (r *Router) bind(c *Ctx, params []string) error {
	if len(params) == 0 {
		return nil
	}
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	for _, name := range params {
		b, ok := r.s.binders[name]
		if !ok {
			continue
		}
		v, err := b(c.R, c.R.PathValue(name))
		if err != nil {
			return err
		}
		if c.bound == nil {
			c.bound = map[string]any{}
		}
		c.bound[name] = v
	}
	return nil
}

// URL returns the path of a named route with its parameters, given as
// name/value pairs: URL("users.show", "user", 7) → "/users/7". Values are
// path-escaped; a {path...} value keeps its slashes.
func (r *Router) URL(name string, params ...any) (string, error) {
	r.s.mu.RLock()
	path, ok := r.s.names[name]
	r.s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("web: no route named %q", name)
	}
	if len(params)%2 != 0 {
		return "", fmt.Errorf("web: URL(%q): parameters must be name/value pairs", name)
	}
	vals := map[string]string{}
	for i := 0; i < len(params); i += 2 {
		vals[fmt.Sprint(params[i])] = fmt.Sprint(params[i+1])
	}
	var b strings.Builder
	rest := path
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(rest)
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		w := rest[i+1 : i+j]
		rest = rest[i+j+1:]
		if w == "$" {
			continue
		}
		key, multi := strings.CutSuffix(w, "...")
		v, ok := vals[key]
		if !ok {
			return "", fmt.Errorf("web: URL(%q): missing parameter %q", name, key)
		}
		delete(vals, key)
		if multi {
			segs := strings.Split(v, "/")
			for k := range segs {
				segs[k] = url.PathEscape(segs[k])
			}
			b.WriteString(strings.Join(segs, "/"))
		} else {
			b.WriteString(url.PathEscape(v))
		}
	}
	if len(vals) > 0 { // extra parameters become the query string
		q := url.Values{}
		for k, v := range vals {
			q.Set(k, v)
		}
		return b.String() + "?" + q.Encode(), nil
	}
	return b.String(), nil
}

// ServeHTTP serves a request: the root middleware, then the matching route,
// or the ErrorHandler with 404 / 405 when none matches.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.s.mu.RLock()
	h := r.s.chain
	r.s.mu.RUnlock()
	if h == nil {
		r.s.mu.Lock()
		if r.s.chain == nil {
			r.s.chain = chain(http.HandlerFunc(r.dispatch), r.s.global)
		}
		h = r.s.chain
		r.s.mu.Unlock()
	}
	sw := &statusWriter{ResponseWriter: w}
	c := &Ctx{router: r, sw: sw}
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, c))
	c.W, c.R = sw, req
	h.ServeHTTP(sw, req)
}

func (r *Router) dispatch(w http.ResponseWriter, req *http.Request) {
	if _, pattern := r.s.mux.Handler(req); pattern != "" {
		r.s.mux.ServeHTTP(w, req)
		return
	}
	// No route: ServeMux answers 404, 405 (with Allow) or a redirect. The
	// 404 and 405 go through the ErrorHandler, so they are JSON for APIs.
	iw := &interceptWriter{ResponseWriter: w}
	r.s.mux.ServeHTTP(iw, req)
	if iw.code != 0 {
		c, _ := FromRequest(req)
		c.W, c.R = w, req
		r.handleError(c, Error(iw.code, ""))
	}
}

// interceptWriter swallows ServeMux's own 404 and 405 responses.
type interceptWriter struct {
	http.ResponseWriter
	code int
}

func (w *interceptWriter) WriteHeader(code int) {
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		w.code = code
		w.Header().Del("Content-Type")
		w.Header().Del("X-Content-Type-Options")
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *interceptWriter) Write(b []byte) (int, error) {
	if w.code != 0 {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func chain(h http.Handler, mw []Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func joinPath(prefix, path string) string {
	prefix = strings.TrimSuffix(prefix, "/")
	switch {
	case path == "" || path == "/" && prefix != "":
		if prefix == "" {
			return "/"
		}
		return prefix
	case !strings.HasPrefix(path, "/"):
		path = "/" + path
	}
	return prefix + path
}

// exact makes a path that ends in "/" match only itself ("/" is the home
// page, not every path); a subtree is Mount or a {path...} wildcard.
func exact(path string) string {
	if strings.HasSuffix(path, "/") {
		return path + "{$}"
	}
	return path
}

// wildcards lists the {name} parameters of a pattern.
func wildcards(path string) []string {
	var out []string
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			return out
		}
		name := strings.TrimSuffix(path[i+1:i+j], "...")
		if name != "$" {
			out = append(out, name)
		}
		path = path[i+j+1:]
	}
}
