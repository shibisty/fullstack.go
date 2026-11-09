package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"strconv"
)

// Ctx is the request context of a Handler: the request, the response and
// helpers to read and answer. It is valid only during the handler.
type Ctx struct {
	W      http.ResponseWriter
	R      *http.Request
	router *Router
	bound  map[string]any
	sw     *statusWriter // the response as the router started it
}

type ctxKey struct{}

// FromRequest returns the Ctx of a request served by a Router (for
// net/http middleware and helpers that only get the *http.Request).
func FromRequest(r *http.Request) (*Ctx, bool) {
	c, ok := r.Context().Value(ctxKey{}).(*Ctx)
	return c, ok
}

// Context returns the request's context.
func (c *Ctx) Context() context.Context { return c.R.Context() }

// Logger returns the router's logger with the request's id, method and path.
func (c *Ctx) Logger() *slog.Logger {
	l := c.router.logger()
	return l.With(slog.String("method", c.R.Method), slog.String("path", c.R.URL.Path), slog.String("request_id", RequestIDOf(c.R)))
}

// Param returns a path parameter: "{id}" in "/users/{id}".
func (c *Ctx) Param(name string) string { return c.R.PathValue(name) }

// ParamInt returns a path parameter as an int64; a value that is not a
// number is a 404 (the route does not exist for it).
func (c *Ctx) ParamInt(name string) (int64, error) {
	n, err := strconv.ParseInt(c.R.PathValue(name), 10, 64)
	if err != nil {
		return 0, ErrNotFound
	}
	return n, nil
}

// Query returns a query string parameter.
func (c *Ctx) Query(name string) string { return c.R.URL.Query().Get(name) }

// Bound returns the value a Binder resolved for a path parameter.
func (c *Ctx) Bound(name string) (any, bool) {
	v, ok := c.bound[name]
	return v, ok
}

// Model returns the value a Binder resolved for the path parameter name,
// as T:
//
//	r.Bind("user", ...)
//	r.Get("/users/{user}", func(c *web.Ctx) error {
//	    user, err := web.Model[*User](c, "user")
//	    ...
//	})
func Model[T any](c *Ctx, name string) (T, error) {
	var zero T
	v, ok := c.bound[name]
	if !ok {
		return zero, fmt.Errorf("web: no binder resolved {%s}", name)
	}
	t, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("web: {%s} is %T, not %T", name, v, zero)
	}
	return t, nil
}

// MaxBodyBytes limits the bodies DecodeJSON reads (1 MiB).
var MaxBodyBytes int64 = 1 << 20

// DecodeJSON reads a JSON body into v. Malformed JSON, unknown fields and
// trailing data are a 400, a body over MaxBodyBytes a 413.
func (c *Ctx) DecodeJSON(v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(c.W, c.R.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return ErrTooLarge
		}
		if errors.Is(err, io.EOF) {
			return Error(http.StatusBadRequest, "empty request body")
		}
		return &HTTPError{Status: http.StatusBadRequest, Message: "invalid JSON: " + err.Error(), Err: err}
	}
	if dec.More() {
		return Error(http.StatusBadRequest, "invalid JSON: trailing data")
	}
	return nil
}

// statusWriter records whether and with which status the response started.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach Flush, Hijack and deadlines.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Written reports whether the response has started.
func (c *Ctx) Written() bool {
	if c.sw != nil {
		return c.sw.status != 0
	}
	sw, ok := c.W.(*statusWriter)
	return ok && sw.status != 0
}

// WantsJSON reports whether the client asks for JSON (Accept or Content-Type).
func (c *Ctx) WantsJSON() bool { return wantsJSON(c.R) }

// JSON writes v as JSON with the status.
func (c *Ctx) JSON(status int, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	_, err = c.W.Write(append(data, '\n'))
	return err
}

// Text writes plain text with the status.
func (c *Ctx) Text(status int, s string) error {
	c.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.W.Header().Set("X-Content-Type-Options", "nosniff")
	c.W.WriteHeader(status)
	_, err := io.WriteString(c.W, s)
	return err
}

// NoContent answers 204.
func (c *Ctx) NoContent() error {
	c.W.WriteHeader(http.StatusNoContent)
	return nil
}

// Redirect answers with a redirect (303 See Other when status is 0, the
// right one after a form POST).
func (c *Ctx) Redirect(status int, url string) error {
	if status == 0 {
		status = http.StatusSeeOther
	}
	http.Redirect(c.W, c.R, url, status)
	return nil
}

// Renderer renders named templates (view.Engine).
type Renderer interface {
	Render(w io.Writer, name string, data any) error
}

// ClientRenderer is a Renderer whose pages the browser can render itself
// (fullstack/view's Engine): HTML answers a request that carries the
// current template pack version in PackHeader with the page's data
// (PageJSON) instead of its HTML, and the router serves the pack at
// PackPath.
type ClientRenderer interface {
	Renderer
	Version() string
	ClientData(name string, data any) ([]byte, bool)
	ServePack(w http.ResponseWriter, r *http.Request)
}

const (
	// PackHeader carries the template pack version (requests and responses).
	PackHeader = "X-Fullstack-Templates"
	// PageJSON is the content type of a page's data.
	PageJSON = "application/vnd.fullstack.page+json"
	// PackPath is where the template pack is served.
	PackPath = "/__fullstack/templates"
)

// HTML renders the template name with data through the router's Renderer.
// The page is rendered into a buffer first, so a template error is a clean
// 500 instead of half a page.
func (c *Ctx) HTML(status int, name string, data any) error {
	rd := c.router.renderer()
	if rd == nil {
		return errors.New("web: HTML needs a Renderer (Router.SetRenderer)")
	}
	if cr, ok := rd.(ClientRenderer); ok {
		v := cr.Version()
		c.W.Header().Set(PackHeader, v)
		c.W.Header().Add("Vary", PackHeader)
		if c.R != nil && c.R.Header.Get(PackHeader) == v && !skipped(c.R.Header.Get(PackHeader+"-Skip"), name) {
			if b, ok := cr.ClientData(name, data); ok {
				c.W.Header().Set("Content-Type", PageJSON)
				c.W.WriteHeader(status)
				_, err := c.W.Write(b)
				return err
			}
		}
	}
	var buf bytesBuffer
	if err := rd.Render(&buf, name, data); err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.WriteHeader(status)
	_, err := c.W.Write(buf.b)
	return err
}

// skipped reports whether the browser asked for name as HTML (it could
// not render it from data).
func skipped(list, name string) bool {
	for _, n := range strings.Split(list, ",") {
		if strings.TrimSpace(n) == name {
			return true
		}
	}
	return false
}

type bytesBuffer struct{ b []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) { b.b = append(b.b, p...); return len(p), nil }

// URL returns the path of a named route (Router.URL).
func (c *Ctx) URL(name string, params ...any) (string, error) { return c.router.URL(name, params...) }
