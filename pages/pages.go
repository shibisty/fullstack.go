// Package pages routes the pages of a template folder by their file names,
// like Next.js or Nuxt: a page is a file, its URL is its path, and the Go
// code that a page needs is a loader keyed by the page's name.
//
//	views/pages/index.html            GET /
//	views/pages/about.html            GET /about
//	views/pages/notes/index.html      GET /notes
//	views/pages/notes/[note].html     GET /notes/{note}
//	views/pages/docs/[...path].html   GET /docs/{path...}
//	views/pages/(auth)/login.html     GET /login          a folder in () only groups files
//	views/pages/_form.html            not a page: files and folders starting with _
//
// The pages are rendered by the view engine (fullstack/view), so they use
// its layouts and partials, and the router (fullstack/web), so {note} goes
// through the binder of "note" and the routes show in routes:list:
//
//	err := pages.Register(site, views, pages.Options{
//	    Dir: "views/pages",
//	    Shared: func(c *web.Ctx) map[string]any { return map[string]any{"User": ...} },
//	    Loaders: pages.Loaders{
//	        "index": func(c *web.Ctx) (any, error) {
//	            return map[string]any{"Notes": notes.List()}, nil
//	        },
//	    },
//	    Actions: pages.Actions{"notes/index": createNote}, // POST /notes
//	})
//
// The folder is read once, when routes are registered: routes come from
// the files that exist then (gtr dev restarts the app when one is added).
// Loaders and actions for pages that do not exist are errors, so a renamed
// file cannot silently lose its code.
package pages

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"fullstack/web"
)

// Loader returns a page's data. A map[string]any is merged into the data
// the template gets; any other value is its .Data. A loader may answer
// itself (c.Redirect) and return nil; an error goes to the router's error
// handler (web.ErrNotFound is a 404).
type Loader func(c *web.Ctx) (any, error)

// Loaders map page names ("notes/[note]") to loaders.
type Loaders map[string]Loader

// Actions map page names to the handlers of POST requests to the page's URL
// (its forms).
type Actions map[string]web.Handler

// Options configure Register.
type Options struct {
	// Dir is the pages folder in the FS (default "views/pages").
	Dir string
	// View is the prefix of the pages' names in the view engine (default
	// the last element of Dir and a slash: "pages/"), so that the page
	// "notes/index" is rendered as "pages/notes/index".
	View *string
	// Ext is the template extension (default ".html").
	Ext string
	// Shared returns the data of every page: the user, the CSRF field.
	Shared  func(c *web.Ctx) map[string]any
	Loaders Loaders
	Actions Actions
	// Name names a page's route (default: the page name, "notes/[note]");
	// "" leaves the route unnamed.
	Name func(page string) string
	// Middleware run for every page and action.
	Middleware []web.Middleware
	// PageMiddleware run for one page and its action, after Middleware:
	// {"login": {auth.Guest()}}.
	PageMiddleware map[string][]web.Middleware

	// Cache keeps rendered pages (see Cache), by page name.
	Cache map[string]Cache
	// Guest reports whether a request is a visitor's, who may get a kept
	// page that is not Public (e.g. no auth.User). Without it only Public
	// pages are kept.
	Guest func(c *web.Ctx) bool
	// Store holds the kept pages (default NewStore(0)); keep it to Purge
	// pages when their data changes.
	Store *Store
	// Prerendered holds the files of pages:prerender (public pages without
	// parameters), served as they are: embed them for pages rendered at
	// build time.
	Prerendered fs.FS
}

// Page is a routed page.
type Page struct {
	Name    string // "notes/[note]"
	File    string // path in the FS: "views/pages/notes/[note].html"
	Pattern string // "/notes/{note}"
	View    string // "pages/notes/[note]"
	Cache   *Cache // its policy, if it is kept
}

var segment = regexp.MustCompile(`^\[(\.\.\.)?([A-Za-z_][A-Za-z0-9_]*)\]$`)

// Find lists the pages of the folder, sorted by name, with their URL
// patterns. Two files with the same URL are an error.
func Find(fsys fs.FS, o Options) ([]Page, error) {
	o = o.withDefaults()
	var out []Page
	byPattern := map[string]string{}
	err := fs.WalkDir(fsys, o.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == o.Dir {
			return nil
		}
		if strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || path.Ext(p) != o.Ext {
			return nil
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, o.Dir+"/"), o.Ext)
		pattern, err := patternOf(name)
		if err != nil {
			return fmt.Errorf("pages: %s: %w", p, err)
		}
		key := normalize(pattern)
		if prev, dup := byPattern[key]; dup {
			return fmt.Errorf("pages: %s and %s are both %s", prev, p, pattern)
		}
		byPattern[key] = p
		out = append(out, Page{Name: name, File: p, Pattern: pattern, View: *o.View + name})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("pages: no folder %s", o.Dir)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// patternOf turns a page name into a ServeMux pattern.
func patternOf(name string) (string, error) {
	parts := strings.Split(name, "/")
	var segs []string
	params := map[string]bool{}
	for i, part := range parts {
		last := i == len(parts)-1
		switch m := segment.FindStringSubmatch(part); {
		case last && part == "index":
		case strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") && !last:
			// a group: no segment
		case m != nil:
			if params[m[2]] {
				return "", fmt.Errorf("parameter %q twice", m[2])
			}
			params[m[2]] = true
			if m[1] != "" {
				if !last {
					return "", errors.New("[...name] must be the last part")
				}
				segs = append(segs, "{"+m[2]+"...}")
			} else {
				segs = append(segs, "{"+m[2]+"}")
			}
		case strings.ContainsAny(part, "[]{}()") || part == "":
			return "", fmt.Errorf("%q is not a path segment, [param] or [...param]", part)
		default:
			segs = append(segs, part)
		}
	}
	return "/" + strings.Join(segs, "/"), nil
}

var paramName = regexp.MustCompile(`\{[^}.]+(\.\.\.)?\}`)

// normalize makes patterns that match the same URLs equal: /a/{x} and /a/{y}.
func normalize(pattern string) string {
	return paramName.ReplaceAllString(pattern, "{_$1}")
}

func (o Options) withDefaults() Options {
	if o.Dir == "" {
		o.Dir = "views/pages"
	}
	o.Dir = strings.Trim(path.Clean(o.Dir), "/")
	if o.View == nil {
		v := path.Base(o.Dir) + "/"
		o.View = &v
	}
	if o.Ext == "" {
		o.Ext = ".html"
	}
	if o.Name == nil {
		o.Name = func(page string) string { return page }
	}
	return o
}

// Register adds a GET route for every page of the folder (and a POST route
// for every action) to r, which may be a group with its own middleware.
func Register(r *web.Router, fsys fs.FS, o Options) ([]Page, error) {
	o = o.withDefaults()
	list, err := Find(fsys, o)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, p := range list {
		known[p.Name] = true
	}
	for i := range list {
		if pol, ok := o.Cache[list[i].Name]; ok {
			list[i].Cache = &pol
		}
	}
	if len(o.Cache) > 0 && o.Store == nil {
		o.Store = NewStore(0)
	}
	pre := prerendered(o.Prerendered, list)
	for _, m := range []map[string]bool{keys(o.Loaders), keys(o.Actions), keys(o.PageMiddleware), keys(o.Cache)} {
		for name := range m {
			if !known[name] {
				return nil, fmt.Errorf("pages: %q has a loader, an action, middleware or a cache policy but no file %s/%s%s (pages: %s)",
					name, o.Dir, name, o.Ext, strings.Join(names(list), ", "))
			}
		}
	}
	for _, p := range list {
		p := p
		load := o.Loaders[p.Name]
		mw := append(append([]web.Middleware(nil), o.Middleware...), o.PageMiddleware[p.Name]...)
		render := func(c *web.Ctx) error {
			data := map[string]any{}
			if o.Shared != nil {
				for k, v := range o.Shared(c) {
					data[k] = v
				}
			}
			params := map[string]string{}
			for _, name := range paramNames(p.Pattern) {
				params[name] = c.R.PathValue(name)
			}
			data["Params"] = params
			if load != nil {
				v, err := load(c)
				if err != nil {
					return err
				}
				if c.Written() || recorded(c.W) {
					return nil
				}
				if m, ok := v.(map[string]any); ok {
					for k, x := range m {
						data[k] = x
					}
				} else if v != nil {
					data["Data"] = v
				}
			}
			return c.HTML(200, p.View, data)
		}
		route := r.Get(p.Pattern, func(c *web.Ctx) error {
			if k := pre[p.Name]; k != nil && p.Cache != nil && p.Cache.Public {
				return serveKept(c, k)
			}
			if p.Cache != nil && (p.Cache.Public || o.Guest != nil && o.Guest(c)) {
				return cached(c, o.Store, p.Name, *p.Cache, func() error { return render(c) })
			}
			return render(c)
		}, mw...)
		if n := o.Name(p.Name); n != "" {
			route.Name(n)
		}
		if h, ok := o.Actions[p.Name]; ok {
			r.Post(p.Pattern, h, mw...)
		}
	}
	return list, nil
}

func paramNames(pattern string) []string {
	var out []string
	for _, m := range paramName.FindAllString(pattern, -1) {
		out = append(out, strings.TrimSuffix(strings.Trim(m, "{}"), "..."))
	}
	return out
}

func keys[V any](m map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func names(list []Page) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.Name
	}
	return out
}
