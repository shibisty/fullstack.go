package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// HTTPError is an error with an HTTP status. Handlers return it (or wrap
// it) to answer with that status; Message is shown to the client, Err is
// logged and never shown.
type HTTPError struct {
	Status  int
	Message string
	Err     error
	// Fields are extra JSON fields of the error response (validation errors).
	Fields map[string]any
}

func (e *HTTPError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Err != nil {
		return fmt.Sprintf("%d %s: %v", e.Status, msg, e.Err)
	}
	return fmt.Sprintf("%d %s", e.Status, msg)
}

func (e *HTTPError) Unwrap() error { return e.Err }

// Error returns an HTTPError; msg defaults to the status text.
func Error(status int, msg string) *HTTPError { return &HTTPError{Status: status, Message: msg} }

// Wrap returns an HTTPError with a cause that is logged but not shown.
func Wrap(status int, err error) *HTTPError { return &HTTPError{Status: status, Err: err} }

// Common errors.
var (
	ErrBadRequest   = Error(http.StatusBadRequest, "")
	ErrUnauthorized = Error(http.StatusUnauthorized, "")
	ErrForbidden    = Error(http.StatusForbidden, "")
	ErrNotFound     = Error(http.StatusNotFound, "")
	ErrConflict     = Error(http.StatusConflict, "")
	ErrTooLarge     = Error(http.StatusRequestEntityTooLarge, "")
)

// StatusOf returns the HTTP status of err: an HTTPError's status, or 500.
func StatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return http.StatusInternalServerError
}

// ErrorHandler writes the response for an error a handler returned.
type ErrorHandler func(c *Ctx, err error)

// DefaultErrorHandler answers with the status of err (500 for errors that
// are not HTTPErrors) as JSON when the client asks for JSON, otherwise as
// text. 5xx errors are logged with the request; their details are never
// sent to the client.
func DefaultErrorHandler(c *Ctx, err error) {
	status := StatusOf(err)
	msg := http.StatusText(status)
	var fields map[string]any
	var he *HTTPError
	if errors.As(err, &he) {
		if he.Message != "" {
			msg = he.Message
		}
		fields = he.Fields
	}
	if status >= 500 {
		c.Logger().Error("request failed", slog.Int("status", status), slog.String("error", err.Error()))
	}
	if c.Written() {
		return // the handler already started the response
	}
	if c.WantsJSON() {
		body := map[string]any{"error": msg}
		for k, v := range fields {
			body[k] = v
		}
		_ = c.JSON(status, body)
		return
	}
	_ = c.Text(status, msg)
}

// Fail answers a request with an error through the router's ErrorHandler,
// from net/http middleware that cannot return errors (an auth check, a rate
// limit). Outside a Router it falls back to http.Error.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	c, ok := FromRequest(r)
	if !ok {
		http.Error(w, http.StatusText(StatusOf(err)), StatusOf(err))
		return
	}
	c.W, c.R = w, r
	c.router.handleError(c, err)
}

// WantsJSON reports whether the client asks for JSON: an Accept header with
// json, or a JSON request body.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "json") ||
		strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}
