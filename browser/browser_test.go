package browser

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func site() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Home</title><h1>Home</h1><a href="/form?x=1">form</a>
<ul><li>a</li><li> b </li></ul><p id="late"></p>
<script>setTimeout(() => { document.getElementById("late").textContent = "arrived"; }, 100)</script>`)
	})
	mux.HandleFunc("GET /form", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Form</title><form method="post" action="/done"><input name="name" value="old"><button>Send</button></form>`)
	})
	mux.HandleFunc("POST /done", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><title>Done</title><p id="msg">Hello, %s</p>`, r.FormValue("name"))
	})
	return mux
}

func TestPage(t *testing.T) {
	b := New(t, site(), Timeout(5*time.Second))
	p := b.Page()
	p.Visit("/").Mark()
	if p.Title() != "Home" || p.Text("h1") != "Home" || p.Path() != "/" {
		t.Fatal(p.Title(), p.Path())
	}
	if got := p.Texts("li"); len(got) != 2 || got[1] != "b" {
		t.Fatal(got)
	}
	p.WaitText("#late", "arrived")
	if p.Reloaded() {
		t.Fatal("reloaded without a navigation")
	}
	p.Click(`a[href="/form?x=1"]`).WaitPath("/form?x=1")
	if !p.Reloaded() {
		t.Fatal("a full load is a reload")
	}
	p.Fill(`input[name="name"]`, "Ann").Click("form button").WaitFor("#msg").WaitText("#msg", "Hello, Ann")
	var n int
	p.Eval(`Promise.resolve(40 + 2)`, &n)
	if n != 42 {
		t.Fatal(n)
	}
	// a second tab shares the browser
	q := b.Page().Visit(b.URL() + "/form")
	if q.Title() != "Form" {
		t.Fatal(q.Title())
	}
}

func TestFailure(t *testing.T) {
	b := New(t, site(), Timeout(300*time.Millisecond))
	ft := &fakeT{TB: t}
	p := &Page{t: ft, b: b, ctx: b.Page().ctx}
	func() {
		defer func() { recover() }()
		p.Visit("/").WaitFor("#never")
	}()
	if ft.msg == "" {
		t.Fatal("no failure")
	}
	t.Log(ft.msg)
}

type fakeT struct {
	testing.TB
	msg string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	panic("fatal")
}

func TestNoChrome(t *testing.T) {
	t.Setenv("FULLSTACK_BROWSER", "off")
	ran := false
	t.Run("skipped", func(t *testing.T) {
		New(t, site())
		ran = true
	})
	if ran {
		t.Fatal("not skipped")
	}
}
