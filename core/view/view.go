// Package view renders html/template pages with layouts and partials from
// an fs.FS (embed.FS in production, os.DirFS with Reload in development).
//
// Layout of the template directory:
//
//	layouts/app.html     {{define "layout"}}<html>…{{block "content" .}}{{end}}…</html>{{end}}
//	partials/nav.html    {{define "nav"}}…{{end}}
//	pages/home.html      {{define "content"}}…{{end}}  (or a whole page without a layout)
//
// Every file outside layouts/ and partials/ is a page, named by its path
// without the extension ("pages/home"). A page is rendered through the
// layout template when it defines "content" and the layouts define the
// layout; otherwise the page file is rendered as it is.
//
//	//go:embed views
//	var views embed.FS
//
//	v, err := view.New(views, view.Options{Dir: "views", Reload: app.IsLocal()})
//	router.SetRenderer(v)
//	c.HTML(200, "pages/home", data)
package view

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"text/template/parse"
)

// Options configure an Engine.
type Options struct {
	Dir    string           // directory of the templates in the FS ("" or "." for the root)
	Ext    string           // template extension (default ".html")
	Layout string           // template the layouts define and pages render through (default "layout")
	Funcs  template.FuncMap // extra functions, available in every template
	Reload bool             // parse the templates again on Render when a file changed (development)
	// ClientFuncs are the browser versions of Funcs, for pages rendered in
	// the browser (see ClientData); a page calling a function without one
	// is rendered on the server.
	ClientFuncs map[string]ClientFunc
}

// Engine renders the pages of a template directory.
type Engine struct {
	fsys fs.FS
	o    Options

	mu     sync.RWMutex
	pages  map[string]page
	source string      // sourceHash of the files pages was parsed from
	packed *clientPack // built on demand for pages
}

type page struct {
	t      *template.Template
	layout bool // the page defines "content" itself and a layout exists
}

// New parses every template of the directory; a syntax error in any of
// them is returned here (or by Render with Reload).
func New(fsys fs.FS, o Options) (*Engine, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.Ext == "" {
		o.Ext = ".html"
	}
	if o.Layout == "" {
		o.Layout = "layout"
	}
	sub, err := fs.Sub(fsys, o.Dir)
	if err != nil {
		return nil, err
	}
	e := &Engine{fsys: sub, o: o}
	pages, source, err := e.parse("")
	if err != nil {
		return nil, err
	}
	e.pages, e.source = pages, source
	return e, nil
}

// funcs are the built-in template functions.
func (e *Engine) funcs() template.FuncMap {
	fm := template.FuncMap{
		// dict builds a map for passing several values to a partial:
		// {{template "card" dict "Title" .Title "User" .User}}
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
	}
	for k, v := range e.o.Funcs {
		fm[k] = v
	}
	return fm
}

// parse reads and parses the templates; with the hash of the sources
// already parsed (last) it returns nil pages when nothing changed.
func (e *Engine) parse(last string) (map[string]page, string, error) {
	var shared, pages []string
	files := map[string][]byte{}
	err := fs.WalkDir(e.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != e.o.Ext {
			return nil
		}
		data, err := fs.ReadFile(e.fsys, p)
		if err != nil {
			return err
		}
		files[p] = data
		if strings.HasPrefix(p, "layouts/") || strings.HasPrefix(p, "partials/") {
			shared = append(shared, p)
		} else {
			pages = append(pages, p)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	source := sourceHash(files)
	if source == last {
		return nil, source, nil
	}
	sort.Strings(shared)
	base := template.New("").Funcs(e.funcs())
	for _, p := range shared {
		if _, err := base.New(p).Parse(string(files[p])); err != nil {
			return nil, "", err
		}
	}
	// A layout's {{block "content"}} is a default; a page goes through the
	// layout only when it defines "content" itself.
	hasLayout := base.Lookup(e.o.Layout) != nil
	out := make(map[string]page, len(pages))
	for _, p := range pages {
		t, err := base.Clone()
		if err != nil {
			return nil, "", err
		}
		name := strings.TrimSuffix(p, e.o.Ext)
		pt := t.New(name)
		data := files[p]
		if _, err := pt.Parse(string(data)); err != nil {
			return nil, "", err
		}
		out[name] = page{t: pt, layout: hasLayout && definesContent(name, string(data))}
	}
	return out, source, nil
}

// definesContent reports whether a page's own text defines "content".
func definesContent(name, text string) bool {
	t := parse.New(name)
	t.Mode = parse.SkipFuncCheck
	trees := map[string]*parse.Tree{}
	if _, err := t.Parse(text, "", "", trees); err != nil {
		return false
	}
	_, ok := trees["content"]
	return ok
}

// reload parses the templates again, with Reload, when a file changed.
func (e *Engine) reload() error {
	if !e.o.Reload {
		return nil
	}
	e.mu.RLock()
	last := e.source
	e.mu.RUnlock()
	pages, source, err := e.parse(last)
	if err != nil || pages == nil {
		return err
	}
	e.mu.Lock()
	e.pages, e.source, e.packed = pages, source, nil
	e.mu.Unlock()
	return nil
}

// Render writes the page name ("pages/home") with data. With Reload, the
// templates are parsed again first when a file changed.
func (e *Engine) Render(w io.Writer, name string, data any) error {
	if err := e.reload(); err != nil {
		return err
	}
	e.mu.RLock()
	pg, ok := e.pages[name]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("view: no page %q", name)
	}
	if pg.layout {
		return pg.t.ExecuteTemplate(w, e.o.Layout, data)
	}
	return pg.t.Execute(w, data)
}

// RenderString is Render into a string (emails, fragments).
func (e *Engine) RenderString(name string, data any) (string, error) {
	var b bytes.Buffer
	if err := e.Render(&b, name, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Pages lists the page names, sorted.
func (e *Engine) Pages() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.pages))
	for n := range e.pages {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
