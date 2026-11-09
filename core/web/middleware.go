package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// Recover turns a panic in a handler into a 500 through the ErrorHandler,
// with the stack in the log. http.ErrAbortHandler is re-panicked, as
// net/http expects.
func Recover() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v)
				}
				c, ok := FromRequest(r)
				if !ok {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				c.Logger().Error("panic", slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
				c.W, c.R = w, r
				c.router.handleError(c, Wrap(http.StatusInternalServerError, fmt.Errorf("panic: %v", v)))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type requestIDKey struct{}

// RequestIDHeader is the header RequestID reads and writes.
const RequestIDHeader = "X-Request-ID"

// RequestID gives every request an id: the incoming X-Request-ID when it
// is a sane value (from a trusted proxy), otherwise a random one. It is
// sent back in the response header and is in Ctx.Logger.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if !saneID(id) {
				var b [8]byte
				_, _ = rand.Read(b[:])
				id = hex.EncodeToString(b[:])
			}
			w.Header().Set(RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

func saneID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// RequestIDOf returns the request's id set by RequestID ("" without it).
func RequestIDOf(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// AccessLog logs every request with its status, size and duration at Info
// (Warn for 4xx, Error for 5xx). A nil logger is slog.Default().
func AccessLog(l *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw, ok := w.(*statusWriter)
			if !ok {
				sw = &statusWriter{ResponseWriter: w}
			}
			next.ServeHTTP(sw, r)
			status := sw.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}
			log := l
			if log == nil {
				log = slog.Default()
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method), slog.String("path", r.URL.Path),
				slog.Int("status", status), slog.Int64("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)), slog.String("request_id", RequestIDOf(r)))
		})
	}
}

// SecureHeaders sets conservative security headers: nosniff, frame
// denial, a same-origin referrer policy and, when hsts is set, HSTS for a
// year (only behind HTTPS).
func SecureHeaders(hsts bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
