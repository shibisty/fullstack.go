// Package apptest tests fullstack applications through HTTP, in memory
// (gtr package @fullstack/testing):
//
//	func TestDashboard(t *testing.T) {
//	    app := bootstrap.NewApp()                         // your App with its providers
//	    c := apptest.New(t, app)
//	    c.Get("/dashboard").AssertRedirect("/login?redirect=%2Fdashboard")
//	    c.ActingAs(&passport.Identity{ProviderUserID: "1", Name: "Ann"}).
//	        Get("/dashboard").AssertOK().AssertSee("Ann")
//	    c.PostJSON("/api/notes", map[string]any{"text": ""}).
//	        AssertUnprocessable().AssertJSONValidationErrors("text")
//	}
//
// The client keeps cookies between requests like a browser and sends the
// CSRF token (from the csrf_token cookie) on POST, PUT, PATCH and DELETE,
// as a page's JavaScript would.
package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"fullstack"
	"passport"
	"passport/httpauth"
)

// Client sends requests to an application's handler.
type Client struct {
	t       testing.TB
	h       http.Handler
	app     *fullstack.App
	jar     map[string]*http.Cookie
	headers http.Header
	csrf    bool
}

// New boots the application (its providers' Boot) and returns a client; a
// failing boot fails the test.
func New(t testing.TB, app *fullstack.App) *Client {
	t.Helper()
	h, err := app.Handler()
	if err != nil {
		t.Fatalf("apptest: booting the application: %v", err)
	}
	return &Client{t: t, h: h, app: app, jar: map[string]*http.Cookie{}, headers: http.Header{}, csrf: true}
}

// NewHandler returns a client for any http.Handler (a router without an App).
func NewHandler(t testing.TB, h http.Handler) *Client {
	return &Client{t: t, h: h, jar: map[string]*http.Cookie{}, headers: http.Header{}, csrf: true}
}

func (c *Client) clone() *Client {
	n := *c
	n.headers = c.headers.Clone()
	return &n // the cookie jar is shared, as tabs of one browser
}

// WithHeader returns a client that adds a header to every request.
func (c *Client) WithHeader(key, value string) *Client {
	n := c.clone()
	n.headers.Set(key, value)
	return n
}

// WithoutCSRF returns a client that does not send the CSRF token (to test
// that a route refuses requests without it).
func (c *Client) WithoutCSRF() *Client {
	n := c.clone()
	n.csrf = false
	return n
}

// WithToken returns a client that sends Authorization: Bearer token.
func (c *Client) WithToken(token string) *Client {
	return c.WithHeader("Authorization", "Bearer "+token)
}

// Cookie returns a cookie the application set ("" when there is none).
func (c *Client) Cookie(name string) string {
	if ck, ok := c.jar[name]; ok {
		return ck.Value
	}
	return ""
}

// SetCookie sets a cookie for the following requests.
func (c *Client) SetCookie(name, value string) { c.jar[name] = &http.Cookie{Name: name, Value: value} }

// ActingAs logs identity in through the application's passport session
// store, as if they had logged in: the following requests carry the
// session cookie (the web guard). The App must have a
// *passport.Authenticator (auth.Provider).
func (c *Client) ActingAs(identity *passport.Identity) *Client {
	c.t.Helper()
	a := c.authenticator()
	id, err := a.StartSession(context.Background(), identity, time.Hour)
	if err != nil {
		c.t.Fatalf("apptest: ActingAs: %v", err)
	}
	c.jar[httpauth.CookieName] = &http.Cookie{Name: httpauth.CookieName, Value: id}
	return c
}

// ActingAsAPI returns a client with a Bearer token for identity (the api
// guard); the application needs a token issuer (AUTH_JWT_SECRET).
func (c *Client) ActingAsAPI(identity *passport.Identity) *Client {
	c.t.Helper()
	tok, err := c.authenticator().IssueToken(context.Background(), identity, time.Hour)
	if err != nil {
		c.t.Fatalf("apptest: ActingAsAPI: %v", err)
	}
	return c.WithToken(tok)
}

func (c *Client) authenticator() *passport.Authenticator {
	c.t.Helper()
	if c.app == nil {
		c.t.Fatal("apptest: ActingAs needs a client made with New(t, app)")
	}
	a, err := fullstack.Make[*passport.Authenticator](c.app)
	if err != nil {
		c.t.Fatalf("apptest: ActingAs needs passport in the application (auth.Provider): %v", err)
	}
	return a
}

// Logout forgets the session cookie.
func (c *Client) Logout() *Client {
	delete(c.jar, httpauth.CookieName)
	return c
}

// Get sends a GET request.
func (c *Client) Get(path string) *Response { return c.Do("GET", path, nil, "") }

// GetJSON sends a GET request that accepts JSON.
func (c *Client) GetJSON(path string) *Response {
	return c.WithHeader("Accept", "application/json").Do("GET", path, nil, "")
}

// Post sends a form (application/x-www-form-urlencoded).
func (c *Client) Post(path string, form url.Values) *Response {
	return c.Do("POST", path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// PostJSON sends v as JSON and accepts JSON.
func (c *Client) PostJSON(path string, v any) *Response { return c.sendJSON("POST", path, v) }

// PutJSON sends v as JSON with PUT.
func (c *Client) PutJSON(path string, v any) *Response { return c.sendJSON("PUT", path, v) }

// PatchJSON sends v as JSON with PATCH.
func (c *Client) PatchJSON(path string, v any) *Response { return c.sendJSON("PATCH", path, v) }

// Delete sends a DELETE request.
func (c *Client) Delete(path string) *Response { return c.Do("DELETE", path, nil, "") }

func (c *Client) sendJSON(method, path string, v any) *Response {
	c.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Fatalf("apptest: encoding the JSON body: %v", err)
	}
	return c.WithHeader("Accept", "application/json").Do(method, path, bytes.NewReader(data), "application/json")
}

// Do sends a request with a body and its content type.
func (c *Client) Do(method, path string, body io.Reader, contentType string) *Response {
	c.t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range c.headers {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, ck := range c.jar {
		req.AddCookie(ck)
	}
	switch method {
	case "GET", "HEAD", "OPTIONS", "TRACE":
	default:
		if tok := c.Cookie(httpauth.CSRFCookieName); c.csrf && tok != "" && req.Header.Get(httpauth.CSRFHeader) == "" {
			req.Header.Set(httpauth.CSRFHeader, tok)
		}
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.jar, ck.Name)
			continue
		}
		c.jar[ck.Name] = ck
	}
	return &Response{t: c.t, ResponseRecorder: w, method: method, path: path}
}

// Response is a response with assertions; they report with t.Errorf and
// return the Response for chaining.
type Response struct {
	*httptest.ResponseRecorder
	t            testing.TB
	method, path string
	json         any
	jsonErr      error
	decoded      bool
}

func (r *Response) fail(format string, args ...any) {
	r.t.Helper()
	body := r.ResponseRecorder.Body.String()
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	r.t.Errorf("%s %s: %s\nstatus %d, body:\n%s", r.method, r.path, fmt.Sprintf(format, args...), r.ResponseRecorder.Code, body)
}

// Status is the response status.
func (r *Response) Status() int { return r.ResponseRecorder.Code }

// Text is the response body.
func (r *Response) Text() string { return r.ResponseRecorder.Body.String() }

// AssertStatus checks the status.
func (r *Response) AssertStatus(code int) *Response {
	r.t.Helper()
	if r.ResponseRecorder.Code != code {
		r.fail("status %d, want %d", r.ResponseRecorder.Code, code)
	}
	return r
}

func (r *Response) AssertOK() *Response            { r.t.Helper(); return r.AssertStatus(200) }
func (r *Response) AssertCreated() *Response       { r.t.Helper(); return r.AssertStatus(201) }
func (r *Response) AssertNoContent() *Response     { r.t.Helper(); return r.AssertStatus(204) }
func (r *Response) AssertBadRequest() *Response    { r.t.Helper(); return r.AssertStatus(400) }
func (r *Response) AssertUnauthorized() *Response  { r.t.Helper(); return r.AssertStatus(401) }
func (r *Response) AssertForbidden() *Response     { r.t.Helper(); return r.AssertStatus(403) }
func (r *Response) AssertNotFound() *Response      { r.t.Helper(); return r.AssertStatus(404) }
func (r *Response) AssertUnprocessable() *Response { r.t.Helper(); return r.AssertStatus(422) }

// AssertSuccessful checks a 2xx status.
func (r *Response) AssertSuccessful() *Response {
	r.t.Helper()
	if r.ResponseRecorder.Code < 200 || r.ResponseRecorder.Code > 299 {
		r.fail("status %d, want 2xx", r.ResponseRecorder.Code)
	}
	return r
}

// AssertRedirect checks a 3xx status and, when to is given, the Location.
func (r *Response) AssertRedirect(to ...string) *Response {
	r.t.Helper()
	if r.ResponseRecorder.Code < 300 || r.ResponseRecorder.Code > 399 {
		r.fail("status %d, want a redirect", r.ResponseRecorder.Code)
		return r
	}
	if len(to) > 0 && r.ResponseRecorder.Header().Get("Location") != to[0] {
		r.fail("redirect to %q, want %q", r.ResponseRecorder.Header().Get("Location"), to[0])
	}
	return r
}

// AssertSee checks that the body contains every text (HTML-escaped forms
// of the text count too).
func (r *Response) AssertSee(texts ...string) *Response {
	r.t.Helper()
	body := r.ResponseRecorder.Body.String()
	for _, s := range texts {
		if !strings.Contains(body, s) && !strings.Contains(body, htmlEscape(s)) {
			r.fail("the body does not contain %q", s)
		}
	}
	return r
}

// AssertDontSee checks that the body contains none of the texts.
func (r *Response) AssertDontSee(texts ...string) *Response {
	r.t.Helper()
	body := r.ResponseRecorder.Body.String()
	for _, s := range texts {
		if strings.Contains(body, s) || strings.Contains(body, htmlEscape(s)) {
			r.fail("the body contains %q", s)
		}
	}
	return r
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

// AssertHeader checks a response header.
func (r *Response) AssertHeader(key, value string) *Response {
	r.t.Helper()
	if got := r.ResponseRecorder.Header().Get(key); got != value {
		r.fail("header %s is %q, want %q", key, got, value)
	}
	return r
}

// AssertCookie checks that the response sets a cookie.
func (r *Response) AssertCookie(name string) *Response {
	r.t.Helper()
	for _, ck := range r.ResponseRecorder.Result().Cookies() {
		if ck.Name == name {
			return r
		}
	}
	r.fail("no cookie %q", name)
	return r
}

// DecodeJSON decodes the body into v (failing the test when it is not JSON).
func (r *Response) DecodeJSON(v any) *Response {
	r.t.Helper()
	if err := json.Unmarshal(r.ResponseRecorder.Body.Bytes(), v); err != nil {
		r.fail("the body is not JSON: %v", err)
	}
	return r
}

func (r *Response) value() (any, bool) {
	if !r.decoded {
		r.decoded = true
		r.jsonErr = json.Unmarshal(r.ResponseRecorder.Body.Bytes(), &r.json)
	}
	return r.json, r.jsonErr == nil
}

// JSONPath returns the value at a dotted path ("data.0.name", "" for the
// whole document) of the JSON body, and whether it exists.
func (r *Response) JSONPath(path string) (any, bool) {
	v, ok := r.value()
	if !ok {
		return nil, false
	}
	if path == "" {
		return v, true
	}
	for _, key := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			if v, ok = x[key]; !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// AssertJSONPath checks the value at a dotted path. Numbers compare by
// value (7 matches the JSON 7), other values by their JSON encoding.
func (r *Response) AssertJSONPath(path string, want any) *Response {
	r.t.Helper()
	got, ok := r.JSONPath(path)
	if !ok {
		r.fail("no JSON value at %q", path)
		return r
	}
	gj, _ := json.Marshal(got)
	wj, err := json.Marshal(want)
	if err != nil {
		r.fail("encoding %v: %v", want, err)
		return r
	}
	var wv any
	_ = json.Unmarshal(wj, &wv) // normalize (ints to float64, structs to maps)
	wj2, _ := json.Marshal(wv)
	if string(gj) != string(wj2) {
		r.fail("JSON at %q is %s, want %s", path, gj, wj2)
	}
	return r
}

// AssertJSONMissing checks that a path does not exist.
func (r *Response) AssertJSONMissing(path string) *Response {
	r.t.Helper()
	if _, ok := r.JSONPath(path); ok {
		r.fail("JSON has a value at %q", path)
	}
	return r
}

// AssertJSONCount checks the length of the array (or object) at a path.
func (r *Response) AssertJSONCount(path string, n int) *Response {
	r.t.Helper()
	v, ok := r.JSONPath(path)
	switch x := v.(type) {
	case []any:
		if len(x) != n {
			r.fail("%q has %d items, want %d", path, len(x), n)
		}
	case map[string]any:
		if len(x) != n {
			r.fail("%q has %d keys, want %d", path, len(x), n)
		}
	default:
		if !ok {
			r.fail("no JSON value at %q", path)
		} else {
			r.fail("%q is not an array or object", path)
		}
	}
	return r
}

// AssertJSONValidationErrors checks that the 422 body of
// fullstack/validation ({"errors": {...}}) has errors for every field.
func (r *Response) AssertJSONValidationErrors(fields ...string) *Response {
	r.t.Helper()
	errs, _ := r.JSONPath("errors")
	m, ok := errs.(map[string]any)
	if !ok {
		r.fail("no validation errors in the body")
		return r
	}
	for _, f := range fields {
		if _, ok := m[f]; !ok {
			r.fail("no validation error for %q", f)
		}
	}
	return r
}
