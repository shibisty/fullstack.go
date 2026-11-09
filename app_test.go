package fullstack_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fullstack"
	"fullstack/web"
)

// clearEnv unsets the configuration variables for the test (Load sets
// them process-wide) and restores them afterwards.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"APP_NAME", "APP_ENV", "APP_DEBUG", "APP_URL", "LOG_LEVEL", "LOG_FORMAT", "HTTP_ADDR",
		"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func newApp(t *testing.T, envLines ...string) (*fullstack.App, *bytes.Buffer) {
	t.Helper()
	clearEnv(t)
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte(strings.Join(envLines, "\n")), 0o600)
	var logs bytes.Buffer
	a, err := fullstack.New(fullstack.Options{EnvFiles: []string{p}, LogOutput: &logs})
	if err != nil {
		t.Fatal(err)
	}
	return a, &logs
}

type DB struct{ name string }
type Repo struct{ db *DB }
type Svc interface{ Name() string }
type svc struct{}

func (svc) Name() string { return "svc" }

func TestContainer(t *testing.T) {
	a, _ := newApp(t)
	builds := 0
	fullstack.Provide(a, func(a *fullstack.App) (*DB, error) {
		builds++
		return &DB{name: a.Config.Name}, nil
	})
	fullstack.Provide(a, func(a *fullstack.App) (*Repo, error) {
		db, err := fullstack.Make[*DB](a)
		return &Repo{db: db}, err
	})
	fullstack.Set[Svc](a, svc{})

	var wg sync.WaitGroup
	repos := make([]*Repo, 8)
	for i := range repos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			repos[i] = fullstack.MustMake[*Repo](a)
		}(i)
	}
	wg.Wait()
	if builds != 1 || repos[0] != repos[7] || repos[0].db.name != "fullstack" {
		t.Fatalf("singletons: %d builds", builds)
	}
	if fullstack.MustMake[Svc](a).Name() != "svc" || !fullstack.Has[Svc](a) || fullstack.Has[*os.File](a) {
		t.Fatal("interface service")
	}
	if got := fullstack.MustMake[*fullstack.App](a); got.Config.Name != "fullstack" {
		t.Fatal("app in container")
	}
	if fullstack.MustMake[*web.Router](a) != a.Router || fullstack.MustMake[*slog.Logger](a) != a.Log || fullstack.MustMake[fullstack.Config](a).Addr != ":8080" {
		t.Fatal("built-in services")
	}

	if _, err := fullstack.Make[*os.File](a); err == nil || !strings.Contains(err.Error(), "no provider for *os.File") {
		t.Fatal(err)
	}
	// An error is returned and the next Make tries again.
	fails := true
	type flaky struct{}
	fullstack.Provide(a, func(*fullstack.App) (*flaky, error) {
		if fails {
			return nil, errors.New("not yet")
		}
		return &flaky{}, nil
	})
	if _, err := fullstack.Make[*flaky](a); err == nil || !strings.Contains(err.Error(), "building") {
		t.Fatal(err)
	}
	fails = false
	if v, err := fullstack.Make[*flaky](a); err != nil || v == nil {
		t.Fatal(err)
	}
	// Cycles are errors, not deadlocks.
	type A struct{}
	type B struct{}
	fullstack.Provide(a, func(a *fullstack.App) (*A, error) { _, err := fullstack.Make[*B](a); return &A{}, err })
	fullstack.Provide(a, func(a *fullstack.App) (*B, error) { _, err := fullstack.Make[*A](a); return &B{}, err })
	if _, err := fullstack.Make[*A](a); err == nil || !strings.Contains(err.Error(), "dependency cycle") || !strings.Contains(err.Error(), "*fullstack_test.A -> *fullstack_test.B -> *fullstack_test.A") {
		t.Fatal(err)
	}
	// A nil interface value.
	fullstack.Set[error](a, nil)
	if v, err := fullstack.Make[error](a); err != nil || v != nil {
		t.Fatal(v, err)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustMake did not panic")
		}
	}()
	fullstack.MustMake[*os.File](a)
}

type provider struct {
	name   string
	log    *[]string
	fail   string
	routes bool
}

func (p *provider) Register(a *fullstack.App) error {
	*p.log = append(*p.log, "register "+p.name)
	if p.fail == "register" {
		return errors.New("bad register")
	}
	if p.routes {
		a.Router.Get("/hello", func(c *web.Ctx) error { return c.Text(200, "hello from "+a.Config.Name) })
	}
	return nil
}

func (p *provider) Boot(a *fullstack.App) error {
	*p.log = append(*p.log, "boot "+p.name)
	if p.fail == "boot" {
		return errors.New("bad boot")
	}
	return nil
}

func (p *provider) Shutdown(context.Context) error {
	*p.log = append(*p.log, "shutdown "+p.name)
	if p.fail == "shutdown" {
		return errors.New("bad shutdown")
	}
	return nil
}

func TestProvidersAndServe(t *testing.T) {
	a, logs := newApp(t, "APP_NAME=demo", "APP_ENV=local", "LOG_FORMAT=json", "LOG_LEVEL=debug", "HTTP_SHUTDOWN_TIMEOUT=2s")
	if !a.IsLocal() || a.IsTesting() || a.Config.ShutdownTimeout != 2*time.Second {
		t.Fatalf("%+v", a.Config)
	}
	var events []string
	if err := a.Register(&provider{name: "db", log: &events}, &provider{name: "routes", log: &events, routes: true}); err != nil {
		t.Fatal(err)
	}
	a.OnShutdown(func(context.Context) error { events = append(events, "hook"); return nil })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	var body string
	for i := 0; i < 50; i++ {
		resp, err := http.Get("http://" + ln.Addr().String() + "/hello")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if body != "hello from demo" {
		t.Fatalf("body %q", body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := "register db,register routes,boot db,boot routes,shutdown routes,shutdown db,hook"
	if strings.Join(events, ",") != want {
		t.Fatalf("events\n%s\nwant\n%s", strings.Join(events, ","), want)
	}
	if !strings.Contains(logs.String(), `"msg":"listening"`) || !strings.Contains(logs.String(), `"app":"demo"`) || !strings.Contains(logs.String(), `"path":"/hello"`) {
		t.Fatalf("logs:\n%s", logs)
	}
	if err := a.Register(&provider{name: "late", log: &events}); err == nil {
		t.Fatal("register after boot")
	}
	if err := a.Boot(); err != nil { // once
		t.Fatal(err)
	}
}

func TestFailures(t *testing.T) {
	var events []string
	a, _ := newApp(t)
	if err := a.Register(&provider{name: "x", log: &events, fail: "register"}); err == nil || !strings.Contains(err.Error(), "bad register") {
		t.Fatal(err)
	}
	b, _ := newApp(t)
	b.Register(&provider{name: "y", log: &events, fail: "boot"})
	if _, err := b.Handler(); err == nil || !strings.Contains(err.Error(), "bad boot") {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	c, _ := newApp(t)
	c.Register(&provider{name: "z", log: &events, fail: "boot"})
	if err := c.Serve(context.Background(), ln); err == nil {
		t.Fatal("serve with a failing boot")
	}
	d, _ := newApp(t)
	d.Register(&provider{name: "s", log: &events, fail: "shutdown"})
	d.OnShutdown(func(context.Context) error { return errors.New("hook failed") })
	if err := d.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), "bad shutdown") || !strings.Contains(err.Error(), "hook failed") {
		t.Fatal(err)
	}
	// A listener that fails: Serve returns its error and shuts down.
	e, _ := newApp(t)
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	ln2.Close()
	if err := e.Serve(context.Background(), ln2); err == nil {
		t.Fatal("closed listener")
	}
	f, _ := newApp(t, "HTTP_ADDR=256.0.0.1:x")
	if err := f.Run(context.Background()); err == nil {
		t.Fatal("bad address")
	}
	for _, bad := range []string{"LOG_LEVEL=loud", "LOG_FORMAT=xml", "APP_DEBUG=maybe"} {
		clearEnv(t)
		p := filepath.Join(t.TempDir(), ".env")
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := fullstack.New(fullstack.Options{EnvFiles: []string{p}, LogOutput: io.Discard}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	clearEnv(t)
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("broken line"), 0o600)
	if _, err := fullstack.New(fullstack.Options{EnvFiles: []string{p}}); err == nil {
		t.Error("broken .env accepted")
	}
}

func TestDefaults(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	os.WriteFile(".env", []byte("APP_NAME=fromdotenv\nLOG_LEVEL=error\n"), 0o600)
	clearEnv(t)
	a, err := fullstack.New()
	if err != nil || a.Config.Name != "fromdotenv" {
		t.Fatal(err, a.Config.Name)
	}
}

// A stream that returns on Stopping does not hold shutdown until
// ShutdownTimeout.
func TestStoppingEndsStreams(t *testing.T) {
	a, _ := newApp(t, "HTTP_SHUTDOWN_TIMEOUT=10s")
	started := make(chan struct{})
	a.Router.HandleHTTP("GET", "/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: hi\n\n"))
		http.NewResponseController(w).Flush()
		close(started)
		<-a.Stopping()
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, ln) }()
	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = http.Get("http://" + ln.Addr().String() + "/stream"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	start := time.Now()
	cancel()
	if err := <-done; err != nil || time.Since(start) > 3*time.Second {
		t.Fatal(err, time.Since(start))
	}
	select {
	case <-a.Stopping():
	default:
		t.Fatal("Stopping not closed")
	}
	if err := a.Shutdown(context.Background()); err != nil { // again: no panic
		t.Fatal(err)
	}
}
