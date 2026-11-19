// Package fullstack is the application core of the GTR fullstack framework:
// configuration from the environment and .env, a service container with
// providers in the style of Laravel, structured logging (log/slog), the web
// router (package fullstack/web) and the HTTP server with graceful shutdown.
//
//	func main() {
//	    app, err := fullstack.New()
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    if err := app.Register(&providers.Database{}, &providers.Routes{}); err != nil {
//	        log.Fatal(err)
//	    }
//	    if err := app.Run(context.Background()); err != nil {
//	        app.Log.Error("server", "error", err)
//	        os.Exit(1)
//	    }
//	}
package fullstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"fullstack/env"
	"fullstack/web"
)

// Config is the application's own configuration, read from the environment
// by New (after .env).
type Config struct {
	Name  string `env:"APP_NAME" default:"fullstack"`
	Env   string `env:"APP_ENV" default:"production"` // local, testing, production…
	Debug bool   `env:"APP_DEBUG" default:"false"`
	URL   string `env:"APP_URL" default:"http://localhost:8080"`

	LogLevel  string `env:"LOG_LEVEL" default:"info"`  // debug, info, warn, error
	LogFormat string `env:"LOG_FORMAT" default:"text"` // text or json

	Addr            string        `env:"HTTP_ADDR" default:":8080"`
	ReadTimeout     time.Duration `env:"HTTP_READ_TIMEOUT" default:"15s"`
	WriteTimeout    time.Duration `env:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout     time.Duration `env:"HTTP_IDLE_TIMEOUT" default:"60s"`
	ShutdownTimeout time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" default:"15s"`

	// TrustedProxies are the reverse proxies whose ProxyHeader names the
	// client (web.ParseTrustedProxies: CIDRs, addresses, "loopback",
	// "private"). Empty: RemoteAddr is the client.
	TrustedProxies string `env:"HTTP_TRUSTED_PROXIES"`
	ProxyHeader    string `env:"HTTP_PROXY_HEADER" default:"X-Real-IP"` // or X-Forwarded-For
}

// Provider registers services and routes of one part of the application
// (database, auth, routes), like a Laravel service provider. Register runs
// in App.Register; a provider may also implement Booter (runs once all
// providers are registered, before serving) and Shutdowner.
type Provider interface {
	Register(a *App) error
}

// Booter is a Provider with a Boot step, run by App.Boot in registration
// order, when every provider's services are registered.
type Booter interface {
	Boot(a *App) error
}

// Shutdowner is a Provider (or a service) that releases resources when the
// application stops; run in reverse registration order.
type Shutdowner interface {
	Shutdown(ctx context.Context) error
}

// App is the application: configuration, logger, router and services.
type App struct {
	*core
	stack []reflect.Type // services being built by this App (Make)
}

type core struct {
	Config Config
	Log    *slog.Logger
	Router *web.Router

	// Stdout and Stderr are the console's output (os.Stdout and os.Stderr).
	Stdout, Stderr io.Writer

	c         container
	mu        sync.Mutex
	commands  map[string]Command
	providers []Provider
	booted    bool
	onStop    []func(context.Context) error
	stopping  chan struct{}
	stopOnce  sync.Once
	console   bool
}

// Options adjust New.
type Options struct {
	// EnvFiles are loaded before the configuration is read (default
	// ".env", skipped when missing; variables already set win).
	EnvFiles []string
	// LogOutput is where the default logger writes (default os.Stderr).
	LogOutput io.Writer
}

// New creates the application: loads .env, reads Config, sets up the
// logger (also as slog's default) and the router with Recover, RequestID
// and AccessLog (TrustProxy first when HTTP_TRUSTED_PROXIES is set). The
// App itself and its Config, logger and router are in
// the container (Make[*App], Make[*slog.Logger], Make[*web.Router]).
func New(opts ...Options) (*App, error) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.EnvFiles == nil {
		o.EnvFiles = []string{".env"}
	}
	if err := env.LoadIfExists(o.EnvFiles...); err != nil {
		return nil, err
	}
	var cfg Config
	if err := env.Decode(&cfg); err != nil {
		return nil, err
	}
	log, err := newLogger(cfg, o.LogOutput)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(log)
	proxies, err := web.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("fullstack: HTTP_TRUSTED_PROXIES: %w", err)
	}
	switch http.CanonicalHeaderKey(cfg.ProxyHeader) {
	case http.CanonicalHeaderKey(web.HeaderRealIP), http.CanonicalHeaderKey(web.HeaderForwardedFor):
	default:
		return nil, fmt.Errorf("fullstack: HTTP_PROXY_HEADER=%q: want X-Real-IP or X-Forwarded-For", cfg.ProxyHeader)
	}
	r := web.NewRouter()
	r.SetLogger(log)
	if len(proxies) > 0 {
		r.Use(web.TrustProxy(proxies, web.ProxyOptions{Header: cfg.ProxyHeader}))
	}
	r.Use(web.Recover(), web.RequestID(), web.AccessLog(log))
	a := &App{core: &core{Config: cfg, Log: log, Router: r, Stdout: os.Stdout, Stderr: os.Stderr,
		c: container{entries: map[reflect.Type]*entry{}}, stopping: make(chan struct{})}}
	a.builtinCommands()
	Set(a, a)
	Set(a, cfg)
	Set(a, log)
	Set(a, r)
	return a, nil
}

func newLogger(cfg Config, out io.Writer) (*slog.Logger, error) {
	if out == nil {
		out = os.Stderr
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, fmt.Errorf("fullstack: LOG_LEVEL=%q: %w", cfg.LogLevel, err)
	}
	ho := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(cfg.LogFormat) {
	case "json":
		h = slog.NewJSONHandler(out, ho)
	case "text", "":
		h = slog.NewTextHandler(out, ho)
	default:
		return nil, fmt.Errorf("fullstack: LOG_FORMAT=%q: want text or json", cfg.LogFormat)
	}
	return slog.New(h).With(slog.String("app", cfg.Name)), nil
}

// IsLocal reports APP_ENV=local (development).
func (a *App) IsLocal() bool { return a.Config.Env == "local" }

// IsTesting reports APP_ENV=testing.
func (a *App) IsTesting() bool { return a.Config.Env == "testing" }

// Register adds providers and runs their Register in order. Registering
// after Boot is an error.
func (a *App) Register(ps ...Provider) error {
	for _, p := range ps {
		a.mu.Lock()
		if a.booted {
			a.mu.Unlock()
			return fmt.Errorf("fullstack: Register(%T) after Boot", p)
		}
		a.providers = append(a.providers, p)
		a.mu.Unlock()
		if err := p.Register(a); err != nil {
			return fmt.Errorf("fullstack: %T.Register: %w", p, err)
		}
	}
	return nil
}

// Boot runs the Boot step of every provider once, in registration order.
func (a *App) Boot() error {
	a.mu.Lock()
	if a.booted {
		a.mu.Unlock()
		return nil
	}
	a.booted = true
	ps := append([]Provider(nil), a.providers...)
	a.mu.Unlock()
	for _, p := range ps {
		if b, ok := p.(Booter); ok {
			if err := b.Boot(a); err != nil {
				return fmt.Errorf("fullstack: %T.Boot: %w", p, err)
			}
		}
	}
	return nil
}

// InConsole reports whether the app runs a console command (go run .
// routes:list) rather than serving HTTP: providers skip what only a server
// needs, such as watching the frontend.
func (a *App) InConsole() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.console
}

// Stopping is closed when the application starts to stop: before Serve
// waits for the requests in flight, and in Shutdown. Long-lived handlers
// (event streams, websockets) return on it, so that shutdown does not wait
// for them until ShutdownTimeout.
func (a *App) Stopping() <-chan struct{} { return a.stopping }

func (a *App) stop() { a.stopOnce.Do(func() { close(a.stopping) }) }

// OnShutdown adds a function run when the application stops (after the
// server has drained), in reverse order of registration.
func (a *App) OnShutdown(fn func(context.Context) error) {
	a.mu.Lock()
	a.onStop = append(a.onStop, fn)
	a.mu.Unlock()
}

// Shutdown runs the OnShutdown functions and the providers' Shutdown, last
// registered first, and returns their errors joined.
func (a *App) Shutdown(ctx context.Context) error {
	a.stop()
	a.mu.Lock()
	fns := append([]func(context.Context) error(nil), a.onStop...)
	for _, p := range a.providers {
		if s, ok := p.(Shutdowner); ok {
			fns = append(fns, s.Shutdown)
		}
	}
	a.onStop = nil
	a.mu.Unlock()
	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Handler boots the application and returns its HTTP handler (for tests and
// for serving with your own http.Server).
func (a *App) Handler() (http.Handler, error) {
	if err := a.Boot(); err != nil {
		return nil, err
	}
	return a.Router, nil
}

// Run boots the application and serves HTTP on Config.Addr until ctx is
// cancelled or the process gets SIGINT/SIGTERM; then it stops accepting
// connections, waits up to ShutdownTimeout for requests in flight and runs
// Shutdown.
func (a *App) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.Config.Addr)
	if err != nil {
		return err
	}
	return a.Serve(ctx, ln)
}

// Serve is Run on an existing listener.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	h, err := a.Handler()
	if err != nil {
		ln.Close()
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Handler:           h,
		ReadTimeout:       a.Config.ReadTimeout,
		ReadHeaderTimeout: a.Config.ReadTimeout,
		WriteTimeout:      a.Config.WriteTimeout,
		IdleTimeout:       a.Config.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	a.Log.Info("listening", slog.String("addr", ln.Addr().String()), slog.String("env", a.Config.Env))
	select {
	case err := <-errc:
		_ = a.Shutdown(context.Background())
		return err
	case <-ctx.Done():
	}
	a.Log.Info("shutting down")
	a.stop()
	sctx, cancel := context.WithTimeout(context.Background(), a.Config.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(sctx)
	if serr := a.Shutdown(sctx); serr != nil {
		err = errors.Join(err, serr)
	}
	if e := <-errc; e != nil && !errors.Is(e, http.ErrServerClosed) {
		err = errors.Join(err, e)
	}
	return err
}
