// Command example is a small fullstack application: pages with a layout,
// a JSON API with model binding, and configuration from .env.
package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"fullstack"
	"fullstack/bundler"
	"fullstack/compress"
	auth "fullstack/passport"
	"fullstack/validation"
	"fullstack/pages"
	"fullstack/view"
	"fullstack/web"
	"passport"
	"passport/local"
	"passport/passwordhash"
)

//go:embed views
var views embed.FS

// Note is the example's model, kept in memory.
type Note struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// Notes is an in-memory store, registered in the container.
type Notes struct {
	mu   sync.Mutex
	next int64
	all  []*Note
}

func (n *Notes) Add(text string) *Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.next++
	note := &Note{ID: n.next, Text: text}
	n.all = append(n.all, note)
	return note
}

func (n *Notes) Find(id int64) (*Note, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, x := range n.all {
		if x.ID == id {
			return x, true
		}
	}
	return nil, false
}

func (n *Notes) List() []*Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*Note(nil), n.all...)
}

//go:embed all:public/build
var build embed.FS

// prerendered holds the pages written by `go run . pages:prerender` (the
// public ones: offline.html), served as they are.
//
//go:embed all:public/prerendered
var prerendered embed.FS

var (
	sitePages []pages.Page        // filled when the routes boot, for pages:prerender
	pageStore = pages.NewStore(0) // rendered pages kept for guests
)

// assetsProvider builds resources/js and resources/css with esbuild: on
// every change with APP_ENV=local, with `go run . assets:build` for
// production (embedded into the binary).
func assetsProvider() *bundler.Provider {
	return &bundler.Provider{
		Options: bundler.Options{
			Entries:  []string{"resources/js/app.ts", "resources/css/app.css", "resources/icon.svg"},
			Navigate: true, // links and forms swap the page in place, without a reload
			// Installable, and the pages seen open offline (not while developing).
			PWA: &bundler.PWA{
				ThemeColor: "#3b82f6",
				Icons:      []bundler.Icon{{Src: "resources/icon.svg", Sizes: "any", Type: "image/svg+xml"}},
				Offline:    "/offline",
			},
		},
		Build: bundler.Sub(build, "public/build"),
	}
}

// demoUser is the example's only account: ann@example.com / secret.
var demoHash, _ = passwordhash.Hash("secret")

func verifyUser(_ context.Context, email, password string) (*passport.Identity, error) {
	if ok, _ := passwordhash.Verify(password, demoHash); email != "ann@example.com" || !ok {
		return nil, local.ErrInvalidCredentials
	}
	return &passport.Identity{Provider: "local", ProviderUserID: "1", Email: email, Name: "Ann"}, nil
}

// Routes is the service provider of the HTTP routes.
type Routes struct{}

func (Routes) Register(app *fullstack.App) error {
	app.AddCommand(pages.PrerenderCommand(&sitePages))
	fullstack.Provide(app, func(*fullstack.App) (*Notes, error) {
		n := &Notes{}
		n.Add("Hello from fullstack")
		return n, nil
	})
	return nil
}

func (Routes) Boot(app *fullstack.App) error {
	notes, err := fullstack.Make[*Notes](app)
	if err != nil {
		return err
	}
	assets, err := fullstack.Make[*bundler.Assets](app)
	if err != nil {
		return err
	}
	v, err := view.New(views, view.Options{Dir: "views", Reload: app.IsLocal(),
		Funcs:       assets.Funcs(),
		ClientFuncs: assets.ClientFuncs(), // so that pages can be rendered in the browser too
	})
	if err != nil {
		return err
	}
	au, err := fullstack.Make[*auth.Auth](app)
	if err != nil {
		return err
	}
	r := app.Router
	// zstd, brotli or gzip: fast for pages and the API, maximal (and kept)
	// for the build's files and the template pack
	r.Use(compress.New(compress.Options{}))
	r.SetRenderer(v)
	r.Use(web.SecureHeaders(false))
	r.Bind("note", func(_ *http.Request, id string) (any, error) {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, web.ErrNotFound
		}
		note, ok := notes.Find(n)
		if !ok {
			return nil, web.ErrNotFound
		}
		return note, nil
	})

	shared := func(c *web.Ctx) map[string]any {
		data := map[string]any{"App": app.Config.Name, "CSRFField": auth.CSRFField(c)}
		if u, ok := auth.User(c); ok {
			data["User"] = u
		}
		return data
	}
	page := func(c *web.Ctx, extra map[string]any) map[string]any {
		data := shared(c)
		for k, v := range extra {
			data[k] = v
		}
		return data
	}

	// The site: session cookie and CSRF on every page.
	var pagesErr error
	r.Group("/", func(site *web.Router) {
		site.Use(au.Web(), au.CSRF())
		// A page for every file in views/pages: index.html is /, login.html
		// is /login; their Go code is a loader keyed by the file's name.
		sitePages, pagesErr = pages.Register(site, views, pages.Options{
			Shared: shared,
			// Guests get the home page rendered once every 30 seconds (or after
			// a new note); the offline page is the same for everyone.
			Cache: map[string]pages.Cache{
				"index":   {TTL: 30 * time.Second},
				"offline": {Public: true},
			},
			Guest:       func(c *web.Ctx) bool { _, ok := auth.User(c); return !ok },
			Store:       pageStore,
			Prerendered: bundler.Sub(prerendered, "public/prerendered"),
			Name: func(p string) string {
				if p == "index" {
					return "home"
				}
				return p
			},
			Loaders: pages.Loaders{
				"index": func(c *web.Ctx) (any, error) { return map[string]any{"Notes": notes.List()}, nil },
				"login": func(c *web.Ctx) (any, error) { return map[string]any{"Redirect": c.Query("redirect")}, nil },
			},
			PageMiddleware: map[string][]web.Middleware{"login": {au.Guest()}},
		})
		site.Post("/login", func(c *web.Ctx) error {
			_, err := au.Attempt(c, "local", map[string]string{"email": c.R.FormValue("email"), "password": c.R.FormValue("password")})
			if errors.Is(err, auth.ErrInvalidCredentials) {
				return c.HTML(422, "pages/login", page(c, map[string]any{"Error": "These credentials do not match our records.", "Email": c.R.FormValue("email")}))
			}
			if err != nil {
				return err
			}
			return c.Redirect(0, au.Intended(c, "/"))
		}, au.Throttle(5, time.Minute))
		site.Post("/logout", func(c *web.Ctx) error {
			if err := au.Logout(c); err != nil {
				return err
			}
			// forget the pages kept for offline use (PWA) and the browser cache
			c.W.Header().Set("Clear-Site-Data", `"cache", "storage"`)
			return c.Redirect(0, "/")
		})
		site.Post("/notes", func(c *web.Ctx) error {
			if text := c.R.FormValue("text"); text != "" {
				notes.Add(text)
				pageStore.Purge("index")
			}
			home, _ := c.URL("home")
			return c.Redirect(0, home)
		}, au.Require())
	})
	if pagesErr != nil {
		return pagesErr
	}
	r.Group("/api", func(api *web.Router) {
		api.Get("/notes", func(c *web.Ctx) error { return c.JSON(200, notes.List()) })
		api.Post("/notes", func(c *web.Ctx) error {
			var in struct {
				Text string `json:"text" validate:"required,max=200"`
			}
			if err := validation.Bind(c, &in); err != nil {
				return err // 422 with {"errors": {"text": [...]}}
			}
			note := notes.Add(in.Text)
			pageStore.Purge("index")
			return c.JSON(201, note)
		})
		api.Get("/notes/{note}", func(c *web.Ctx) error {
			note, err := web.Model[*Note](c, "note")
			if err != nil {
				return err
			}
			return c.JSON(200, note)
		}).Name("api.notes.show")
	})
	return nil
}

func main() {
	app, err := fullstack.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Register(assetsProvider(), &auth.Provider{Strategies: []passport.Strategy{local.New(verifyUser)}}, Routes{}); err != nil {
		log.Fatal(err)
	}
	if code, ok := app.Console(os.Args[1:]); ok { // go run . routes:list, make:model…
		os.Exit(code)
	}
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
