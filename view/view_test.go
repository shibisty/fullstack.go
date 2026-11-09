package view

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

var files = fstest.MapFS{
	"views/layouts/app.html":   {Data: []byte(`{{define "layout"}}<html><title>{{block "title" .}}App{{end}}</title>{{template "nav" .}}<main>{{block "content" .}}default{{end}}</main></html>{{end}}`)},
	"views/partials/nav.html":  {Data: []byte(`{{define "nav"}}<nav>{{.User}}</nav>{{end}}`)},
	"views/pages/home.html":    {Data: []byte(`{{define "title"}}Home{{end}}{{define "content"}}<h1>Hi {{.User}}</h1>{{template "card" dict "T" .User}}{{end}}`)},
	"views/pages/plain.html":   {Data: []byte(`plain {{.User | shout}}`)},
	"views/partials/card.html": {Data: []byte(`{{define "card"}}<b>{{.T}}</b>{{end}}`)},
	"views/pages/about.html":   {Data: []byte(`{{define "content"}}about{{end}}`)},
	"views/readme.txt":         {Data: []byte(`not a template`)},
}

func funcs() template.FuncMap {
	return template.FuncMap{"shout": func(s string) string { return strings.ToUpper(s) }}
}

func TestRender(t *testing.T) {
	e, err := New(files, Options{Dir: "views", Funcs: funcs()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.RenderString("pages/home", map[string]any{"User": "<ann>"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<html><title>Home</title><nav>&lt;ann&gt;</nav><main><h1>Hi &lt;ann&gt;</h1><b>&lt;ann&gt;</b></main></html>`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got, _ := e.RenderString("pages/about", map[string]any{"User": "x"}); !strings.Contains(got, "<title>App</title>") || !strings.Contains(got, "<main>about</main>") {
		t.Fatalf("about: %s", got)
	}
	// A page without "content" is rendered as it is, not through the layout.
	if got, _ := e.RenderString("pages/plain", map[string]any{"User": "bob"}); got != "plain BOB" {
		t.Fatalf("plain: %q", got)
	}
	if _, err := e.RenderString("pages/missing", nil); err == nil {
		t.Fatal("missing page")
	}
	if strings.Join(e.Pages(), ",") != "pages/about,pages/home,pages/plain" {
		t.Fatal(e.Pages())
	}
	if _, err := e.RenderString("pages/home", nil); err != nil { // nil data: .User is empty
		t.Fatal(err)
	}
}

func TestDictErrors(t *testing.T) {
	fs := fstest.MapFS{
		"odd.html": {Data: []byte(`{{dict "a"}}`)},
		"key.html": {Data: []byte(`{{dict 1 2}}`)},
	}
	e, err := New(fs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"odd", "key"} {
		if _, err := e.RenderString(p, nil); err == nil {
			t.Errorf("%s: no error", p)
		}
	}
}

func TestParseErrorsAndReload(t *testing.T) {
	if _, err := New(fstest.MapFS{"layouts/a.html": {Data: []byte(`{{define "x"}}`)}}, Options{}); err == nil {
		t.Fatal("broken layout")
	}
	if _, err := New(fstest.MapFS{"p.html": {Data: []byte(`{{if}}`)}}, Options{}); err == nil {
		t.Fatal("broken page")
	}
	if _, err := New(fstest.MapFS{}, Options{Dir: "../x"}); err == nil {
		t.Fatal("bad dir")
	}

	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pages/a.tmpl", "v1")
	e, err := New(os.DirFS(dir), Options{Ext: ".tmpl", Reload: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.RenderString("pages/a", nil); got != "v1" {
		t.Fatal(got)
	}
	write("pages/a.tmpl", "v2")
	if got, _ := e.RenderString("pages/a", nil); got != "v2" {
		t.Fatalf("reload: %q", got)
	}
	write("pages/a.tmpl", "{{if}}")
	if _, err := e.RenderString("pages/a", nil); err == nil {
		t.Fatal("reload error")
	}
}
