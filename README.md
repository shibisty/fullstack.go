# fullstack

The application core of the GTR fullstack framework: a Laravel-style structure on the Go
standard library. `net/http` routing (Go 1.22 patterns), `html/template` views, `log/slog`
logging, configuration from the environment, a service container with providers.
No dependencies beyond the standard library.

Other parts are separate packages: `fullstack-validation`, `fullstack-orm` (database from
the config, migrations, factories), `fullstack-passport` (guards, `auth` middleware),
`fullstack-testing`.

## Installation

```json
{
    "dependencies": {
        "fullstack": "github:shibisty/fullstack.go"
    }
}
```

```bash
gtr install
```

## Quick start

```go
package main

import (
    "context"
    "embed"
    "log"

    "fullstack"
    "fullstack/view"
    "fullstack/web"
)

//go:embed views
var views embed.FS

func main() {
    app, err := fullstack.New() // .env, Config, logger, router
    if err != nil {
        log.Fatal(err)
    }
    v, err := view.New(views, view.Options{Dir: "views", Reload: app.IsLocal()})
    if err != nil {
        log.Fatal(err)
    }
    app.Router.SetRenderer(v)

    app.Router.Get("/", func(c *web.Ctx) error {
        return c.HTML(200, "pages/home", map[string]any{"Name": app.Config.Name})
    }).Name("home")
    app.Router.Group("/api", func(api *web.Router) {
        api.Get("/health", func(c *web.Ctx) error {
            return c.JSON(200, map[string]string{"status": "ok"})
        })
    })

    if err := app.Run(context.Background()); err != nil { // HTTP_ADDR, graceful shutdown
        log.Fatal(err)
    }
}
```

## Configuration

`fullstack.New` loads `.env` (variables already set in the environment win), then reads:

| Variable | Default | |
|---|---|---|
| `APP_NAME` | `fullstack` | in every log line |
| `APP_ENV` | `production` | `local`, `testing`, … (`app.IsLocal()`, `app.IsTesting()`) |
| `APP_DEBUG` | `false` | |
| `APP_URL` | `http://localhost:8080` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `text` | `text` or `json` |
| `HTTP_ADDR` | `:8080` | |
| `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT` | `15s`, `30s`, `60s` | |
| `HTTP_SHUTDOWN_TIMEOUT` | `15s` | how long requests in flight may finish |

Your own configuration is a struct read with `env.Decode`, usually in `config/*.go`:

```go
type Database struct {
    DSN      string `env:"DATABASE_DSN,required"`
    MaxConns int    `env:"DATABASE_MAX_CONNS" default:"10"`
}

var db config.Database
err := env.Decode(&db) // every missing or invalid variable in one error
```

`.env` syntax: `KEY=value`, `# comments`, `export KEY=...`, `"double quotes"` with `\n` escapes and
`${OTHER}` / `${OTHER:-default}`, `'single quotes'` taken literally.

## Services and providers

The container holds services by type. A provider registers how to build one; it is built once,
on first use:

```go
type Database struct{}

func (Database) Register(app *fullstack.App) error {
    fullstack.Provide(app, func(app *fullstack.App) (*sql.DB, error) {
        var cfg config.Database
        if err := env.Decode(&cfg); err != nil {
            return nil, err
        }
        db, err := sql.Open("pgx", cfg.DSN)
        if err == nil {
            app.OnShutdown(func(context.Context) error { return db.Close() })
        }
        return db, err
    })
    return nil
}

// Boot runs after every provider has registered, before serving.
func (Database) Boot(app *fullstack.App) error {
    db, err := fullstack.Make[*sql.DB](app)
    if err != nil {
        return err
    }
    return db.Ping()
}
```

```go
app.Register(providers.Database{}, providers.Routes{})
users := fullstack.MustMake[*repo.Users](app)
```

`Make` returns an error for a missing service and for a dependency cycle; `Set` registers a value
that is already built. The App, its `Config`, `*slog.Logger` and `*web.Router` are in the container
too. Providers may implement `Shutdowner`; it and `OnShutdown` hooks run in reverse order when the
server stops. `app.Stopping()` is closed as soon as stopping begins: a long-lived handler (an event
stream, a websocket) returns on it, so that shutdown does not wait `HTTP_SHUTDOWN_TIMEOUT` for it.

## Routing

```go
r := app.Router
r.Get("/", home).Name("home")
r.Post("/login", login)
r.Group("/admin", func(admin *web.Router) {
    admin.Get("/users/{user}", showUser).Name("admin.users.show")
    admin.Delete("/users/{user}", deleteUser)
}, requireAdmin)
r.Mount("/static", http.FileServerFS(static))
```

- Patterns are `net/http` ones: `{id}`, `{path...}`; methods `Get` (also HEAD), `Post`, `Put`,
  `Patch`, `Delete`, `Any`. Paths match exactly: `/` is only the home page.
- Middleware is the standard `func(http.Handler) http.Handler`, so passport's guards, CSRF and
  rate limits plug in as they are. Root `Use` wraps every request (404s too); a group's middleware
  wraps its routes.
- Handlers return errors. `web.Error(422, "...")`, `web.ErrNotFound` and `web.Wrap(503, err)` set
  the status; any other error is a 500 whose details go only to the log. API clients
  (`Accept: application/json`) get `{"error": "..."}`, others plain text; `r.OnError` replaces this.
- Named routes: `r.URL("admin.users.show", "user", 7)` → `/admin/users/7`.

### Model binding

```go
r.Bind("user", func(req *http.Request, id string) (any, error) {
    u, err := users.Find(req.Context(), id)
    if errors.Is(err, orm.ErrNotFound) {
        return nil, web.ErrNotFound
    }
    return u, err
})

r.Get("/users/{user}", func(c *web.Ctx) error {
    user, err := web.Model[*User](c, "user")
    if err != nil {
        return err
    }
    return c.JSON(200, user)
})
```

### Ctx

`c.Param`, `c.ParamInt` (a non-number is a 404), `c.Query`, `c.DecodeJSON(&v)` (1 MiB limit,
unknown fields and trailing data are a 400), `c.JSON`, `c.Text`, `c.HTML`, `c.Redirect(0, url)`
(303), `c.NoContent`, `c.Logger()` (with the request id), `c.URL`, and `c.W` / `c.R`.

## Views

```
views/
  layouts/app.html    {{define "layout"}}<html>…{{block "content" .}}{{end}}…</html>{{end}}
  partials/nav.html   {{define "nav"}}…{{end}}
  pages/home.html     {{define "content"}}<h1>Hello {{.Name}}</h1>{{end}}
```

A page that defines `content` is rendered through the layout; a page without it is rendered as it
is. Partials take several values with `dict`: `{{template "card" dict "Title" .Title}}`. With
`Reload: true` templates are parsed on every render (development); pages are rendered into a buffer,
so a template error is a clean 500.

### Rendering in the browser

The engine can send its templates to the browser, so that a page swapped in by fullstack-bundler's
navigation (`Navigate: true`) comes as data and is rendered there:

- the router serves the template pack at `/__fullstack/templates`: every page's templates after
  html/template has added its escapers for each context, as JSON (about 1.4 KB gzipped for the
  example), with an `ETag`;
- `c.HTML(status, page, data)` answers a request that carries the pack's version
  (`X-Fullstack-Templates`) with `{"view": page, "data": data}` instead of the HTML; the browser
  renders it with the same escaping (text, attributes, URLs, JS, CSS) as the server would;
- functions need a browser side: `dict` has one, fullstack-bundler's `assets`, `asset` and
  `island` too (`Funcs: assets.Funcs(), ClientFuncs: assets.ClientFuncs()`); a page calling any
  other function, or a page the browser fails to render (a method call such as `.When.Format`),
  is rendered by the server as before.

```go
v, err := view.New(views, view.Options{Dir: "views", Funcs: assets.Funcs(), ClientFuncs: assets.ClientFuncs()})
```

**The data goes to the browser whole**: every exported field of what a page gets, whether the
template prints it or not. Keep secrets out of page data, or tag such fields `json:"-"` (they are
not sent). Structs travel with their field names, typed strings (`template.HTML`, `template.URL`…)
keep their type.

## Pages: routes from files

`fullstack/pages` routes the files of `views/pages/` by their paths, like Next.js or Nuxt, and
renders them with the view engine (layouts and partials as above):

| File | Route |
|---|---|
| `index.html` | `GET /` |
| `about.html` | `GET /about` |
| `notes/index.html` | `GET /notes` |
| `notes/[note].html` | `GET /notes/{note}` (the router's binder of `note` applies) |
| `docs/[...path].html` | `GET /docs/{path...}` |
| `(auth)/login.html` | `GET /login`: a folder in parentheses only groups files |
| `_form.html`, `_drafts/` | not pages: names starting with `_` |

A page's Go code is a loader keyed by the page's name; actions answer POST to the same URL:

```go
//go:embed views
var views embed.FS

_, err := pages.Register(site, views, pages.Options{ // site: a group with session and CSRF middleware
    Shared: func(c *web.Ctx) map[string]any { return map[string]any{"User": ...} },
    Loaders: pages.Loaders{
        "notes/[note]": func(c *web.Ctx) (any, error) {
            note, err := web.Model[*Note](c, "note")
            return map[string]any{"Note": note}, err
        },
    },
    Actions:        pages.Actions{"notes/index": createNote},
    PageMiddleware: map[string][]web.Middleware{"login": {au.Guest()}},
})
```

A template gets the `Shared` data, `.Params` (the path parameters) and what the loader returned
(a `map[string]any` is merged, anything else is `.Data`). A loader may redirect and return nil;
`web.ErrNotFound` is a 404. Routes are named after their pages (`c.URL("notes/[note]", "note", 3)`;
`Name` renames them) and show in `routes:list`.

The folder is read when routes are registered (`gtr dev` restarts the app when a page is added);
two files with the same URL, and loaders, actions or middleware for a page that has no file, are
errors at start, so a renamed file cannot silently lose its code. Pages and hand-written routes mix:
a page's GET and a `site.Post` to the same URL work together.

### Kept pages and prerendering

A rendered page can be kept and served again without its loader and templates:

```go
store := pages.NewStore(0) // 32 MB, least recently used out first
pages.Register(site, views, pages.Options{
    Cache: map[string]pages.Cache{
        "index": {TTL: 30 * time.Second},             // kept for guests
        "about": {TTL: time.Hour, Public: true},      // the same for everyone
    },
    Guest: func(c *web.Ctx) bool { _, ok := auth.User(c); return !ok },
    Store: store,
})
store.Purge("index") // when its data changes (a new note)
```

A kept page has an ETag (a browser that has it gets a 304) and `Cache-Control: no-cache`, so
fullstack-compress compresses it once at its maximal level. The key is the URL, the representation
(HTML or page data) and `Cache.Vary`. Never kept: responses other than 200, ones that set a cookie,
and pages holding a CSRF field, which would hand one visitor's token to the others; a page rendered
for a user (not `Guest`, not `Public`) is never served from the store either.

`pages:prerender [dir]` renders the `Public` pages without parameters as a visitor would and writes
them to `public/prerendered/<path>/index.html`; with `Options.Prerendered` (an `embed.FS` of that
folder) they are served as they are, built with the binary. Register the command with
`app.AddCommand(pages.PrerenderCommand(&site))`, where `site` gets Register's result.

## Console

The application binary is also its console, like `php artisan`:

```go
func main() {
    app := bootstrap.NewApp()
    if code, ok := app.Console(os.Args[1:]); ok { // a command: run it and exit
        os.Exit(code)
    }
    app.Run(context.Background())                 // no command: serve HTTP
}
```

```bash
go run . list                       # every command
go run . routes:list
go run . make:model BlogPost -m     # app/models/blog_post.go (+ its migration with fullstack-orm)
go run . make:controller Post       # app/http/controllers/post_controller.go, resource routes
go run . make:middleware RequireAdmin
go run . make:provider Billing
go run . make:seeder UserSeeder     # database/seeders
go run . make:test Posts            # tests/posts_test.go
```

Add your own with `app.AddCommand(fullstack.Command{Name: "report:send", Run: ...})`; fullstack-orm
adds `migrate*`, `make:migration` and `db:seed`. The generated files come from
`fullstack.MakeTemplates` into `fullstack.MakeDirs`; replace an entry to change them.

## Development

```bash
gtr install
gtr run test
```

## License

MIT
