// Package browser runs end-to-end tests of a web app in a headless Chrome,
// from Go tests (gtr package @fullstack/browser). The app's handler is
// served by an httptest server; Chrome is driven through the DevTools
// protocol (chromedp), so the tests need no Node.js:
//
//	func TestLogin(t *testing.T) {
//	    h, _ := newApp(t).Handler()
//	    p := browser.New(t, h).Page()
//	    p.Visit("/login")
//	    p.Fill(`input[name="email"]`, "ann@example.com")
//	    p.Fill(`input[name="password"]`, "secret")
//	    p.Click(`form button`)
//	    p.WaitText("nav", "Log out")
//	}
//
// The test is skipped when no Chrome or Chromium is found; FULLSTACK_CHROME
// names the executable, FULLSTACK_BROWSER=off skips browser tests, and
// FULLSTACK_BROWSER=show opens a visible window.
package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	cdrt "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Browser is a Chrome and the server of the app under test.
type Browser struct {
	t       testing.TB
	srv     *httptest.Server
	ctx     context.Context // the browser
	timeout time.Duration
}

// Option adjusts New.
type Option func(*options)

type options struct {
	timeout time.Duration
	flags   []chromedp.ExecAllocatorOption
}

// Timeout is how long one step (Visit, Click, WaitText…) may take (default 10s).
func Timeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// Flag passes a command-line flag to Chrome.
func Flag(name string, value any) Option {
	return func(o *options) { o.flags = append(o.flags, chromedp.Flag(name, value)) }
}

// New serves h and starts Chrome; both stop when the test ends. The test is
// skipped when there is no Chrome (see the package documentation).
func New(t testing.TB, h http.Handler, opts ...Option) *Browser {
	t.Helper()
	if os.Getenv("FULLSTACK_BROWSER") == "off" {
		t.Skip("FULLSTACK_BROWSER=off")
	}
	o := options{timeout: 10 * time.Second}
	for _, fn := range opts {
		fn(&o)
	}
	exe := findChrome()
	if exe == "" {
		t.Skip("no Chrome or Chromium found (set FULLSTACK_CHROME)")
	}
	flags := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(exe), chromedp.NoSandbox,
		chromedp.Flag("disable-dev-shm-usage", true))
	if os.Getenv("FULLSTACK_BROWSER") == "show" {
		flags = append(flags, chromedp.Flag("headless", false))
	}
	flags = append(flags, o.flags...)
	actx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), flags...)
	ctx, cancel := chromedp.NewContext(actx)
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		cancelAlloc()
		t.Fatalf("browser: starting %s: %v", exe, err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		cancel()
		cancelAlloc()
		srv.Close()
	})
	return &Browser{t: t, srv: srv, ctx: ctx, timeout: o.timeout}
}

// URL is the address of the app (http://127.0.0.1:port).
func (b *Browser) URL() string { return b.srv.URL }

// Page opens a tab. The first page uses the browser's first tab; pages of
// one Browser share cookies.
func (b *Browser) Page() *Page {
	ctx, cancel := chromedp.NewContext(b.ctx)
	b.t.Cleanup(cancel)
	// The tab belongs to the context of the first Run: not a timeout's.
	if err := chromedp.Run(ctx); err != nil {
		b.t.Fatalf("browser: opening a tab: %v", err)
	}
	return &Page{t: b.t, b: b, ctx: ctx}
}

// findChrome looks for Chrome or Chromium: FULLSTACK_CHROME, the PATH and
// the usual install places.
func findChrome() string {
	if p := os.Getenv("FULLSTACK_CHROME"); p != "" {
		return p
	}
	names := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless-shell", "msedge"}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var places []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				places = append(places, dir+`\Google\Chrome\Application\chrome.exe`, dir+`\Chromium\Application\chrome.exe`,
					dir+`\Microsoft\Edge\Application\msedge.exe`)
			}
		}
	case "darwin":
		places = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"}
	default:
		places = []string{"/usr/bin/google-chrome", "/usr/bin/chromium", "/snap/bin/chromium"}
	}
	for _, p := range places {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Page is a browser tab. Its methods fail the test on errors.
type Page struct {
	t   testing.TB
	b   *Browser
	ctx context.Context
}

func (p *Page) run(what string, actions ...chromedp.Action) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, p.b.timeout)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("timed out")
		}
		p.t.Fatalf("browser: %s: %v (page %s)", what, err, p.url())
	}
}

func (p *Page) url() string {
	var u string
	ctx, cancel := context.WithTimeout(p.ctx, time.Second)
	defer cancel()
	chromedp.Run(ctx, chromedp.Location(&u))
	return u
}

// Visit loads a path of the app (or a full URL) and waits for the page.
func (p *Page) Visit(path string) *Page {
	p.t.Helper()
	u := path
	if strings.HasPrefix(path, "/") {
		u = p.b.srv.URL + path
	}
	p.run("visiting "+path, chromedp.Navigate(u), chromedp.WaitReady("body", chromedp.ByQuery))
	return p
}

// Click clicks the first element matching the CSS selector.
func (p *Page) Click(sel string) *Page {
	p.t.Helper()
	p.run("clicking "+sel, chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible))
	return p
}

// Fill replaces the value of a field and types the text into it.
func (p *Page) Fill(sel, text string) *Page {
	p.t.Helper()
	p.run("filling "+sel, chromedp.Clear(sel, chromedp.ByQuery), chromedp.SendKeys(sel, text, chromedp.ByQuery))
	return p
}

// WaitFor waits until an element matching sel is visible.
func (p *Page) WaitFor(sel string) *Page {
	p.t.Helper()
	p.run("waiting for "+sel, chromedp.WaitVisible(sel, chromedp.ByQuery))
	return p
}

// WaitText waits until the text of the first element matching sel
// contains want.
func (p *Page) WaitText(sel, want string) *Page {
	p.t.Helper()
	p.poll("waiting for "+want+" in "+sel,
		`(() => { const e = document.querySelector(`+jsString(sel)+`); return !!e && e.textContent.includes(`+jsString(want)+`); })()`)
	return p
}

// WaitPath waits until the page's path (and query) is path.
func (p *Page) WaitPath(path string) *Page {
	p.t.Helper()
	p.poll("waiting for "+path, `location.pathname + location.search === `+jsString(path))
	return p
}

// poll evaluates js until it is true, through page loads (whose moments
// without a document are not errors), for the step's timeout.
func (p *Page) poll(what, js string) {
	p.t.Helper()
	deadline := time.Now().Add(p.b.timeout)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(p.ctx, time.Second)
		var ok bool
		last = chromedp.Run(ctx, chromedp.Evaluate(js, &ok))
		cancel()
		if last == nil && ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		last = errors.New("timed out")
	}
	p.t.Fatalf("browser: %s: %v (page %s)", what, last, p.url())
}

// Text returns the text of the first element matching sel.
func (p *Page) Text(sel string) string {
	p.t.Helper()
	var s string
	p.run("reading "+sel, chromedp.Text(sel, &s, chromedp.ByQuery, chromedp.NodeReady))
	return s
}

// Texts returns the texts of every element matching sel (none: empty).
func (p *Page) Texts(sel string) []string {
	p.t.Helper()
	var out []string
	p.Eval(`[...document.querySelectorAll(`+jsString(sel)+`)].map((e) => e.textContent.trim())`, &out)
	return out
}

// Title is the document's title.
func (p *Page) Title() string {
	p.t.Helper()
	var s string
	p.run("reading the title", chromedp.Title(&s))
	return s
}

// Path is the page's path and query.
func (p *Page) Path() string {
	p.t.Helper()
	var s string
	p.Eval(`location.pathname + location.search`, &s)
	return s
}

// Eval runs JavaScript in the page; the result goes into out (may be nil).
// A promise is awaited.
func (p *Page) Eval(js string, out any) *Page {
	p.t.Helper()
	p.run("evaluating "+short(js), chromedp.Evaluate(js, out, func(e *cdrt.EvaluateParams) *cdrt.EvaluateParams {
		return e.WithAwaitPromise(true)
	}))
	return p
}

// Mark marks the document: Reloaded reports whether it has been replaced
// since (a full page load), to check that navigation swapped pages in place.
func (p *Page) Mark() *Page {
	p.t.Helper()
	return p.Eval(`window.__fullstackBrowserMark = true`, nil)
}

// Reloaded reports whether the page was loaded anew since Mark.
func (p *Page) Reloaded() bool {
	p.t.Helper()
	var marked bool
	p.Eval(`window.__fullstackBrowserMark === true`, &marked)
	return !marked
}

func short(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "`", "\\`", "$", `\$`)
	return "`" + r.Replace(s) + "`"
}
