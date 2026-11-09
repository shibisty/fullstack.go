# Changelog

## Unreleased

### Added
- `fullstack.App`: `.env` loading, `Config` from the environment (`APP_*`, `LOG_*`, `HTTP_*`),
  a `log/slog` logger (text or JSON, also slog's default), the router with `Recover`,
  `RequestID` and `AccessLog`, and `Run` / `Serve` with graceful shutdown on SIGINT/SIGTERM.
- Service container: `Provide`, `Set`, `Make`, `MustMake`, `Has`; lazy singletons built
  once, dependency cycles reported as errors. Service providers (`Provider`, `Booter`,
  `Shutdowner`) and `OnShutdown` hooks; `App.Stopping()` for long-lived handlers to return on
  when the server starts to stop; `App.InConsole()` while a console command runs.
- `fullstack/env`: `Load`, `LoadIfExists`, `Overload`, `Parse` (quotes, escapes, `${VAR}` and
  `${VAR:-default}`), `String`, `Int`, `Bool`, `Duration`, `Required`, and `Decode` into a struct
  by `env` / `default` tags, with all errors reported together.
- `fullstack/web`: router on `net/http.ServeMux` patterns with groups, middleware
  (`func(http.Handler) http.Handler`), model binding (`Bind`, `Model[T]`), named routes and
  `URL`; handlers returning errors (`HTTPError`, `DefaultErrorHandler`: JSON for API clients,
  text otherwise, 5xx details only in the log); `Ctx` helpers `JSON`, `Text`, `HTML`, `Redirect`,
  `NoContent`, `DecodeJSON` (size limit, unknown fields rejected), `Param`, `ParamInt`, `Query`;
  middleware `Recover`, `RequestID`, `AccessLog`, `SecureHeaders`; `web.Fail` answers an error
  from plain net/http middleware through the router's ErrorHandler.
- `fullstack/view`: `html/template` pages with layouts and partials from an `fs.FS`
  (`embed.FS`), `Reload` for development, a `dict` function for partials.
- `fullstack/pages`: routes from the files of `views/pages/` (`index`, `[param]`, `[...rest]`,
  `(group)` folders, `_` for non-pages), rendered by the view engine; loaders and POST actions keyed
  by page name, shared data, `.Params`, per-page middleware, named routes; duplicate URLs and code
  for missing pages are errors at start.
- Rendering in the browser: `view.Engine` serves its template pack (escaped html/template trees as
  JSON) at `/__fullstack/templates` through `Router.SetRenderer`, and `Ctx.HTML` answers a request
  carrying the pack version with the page's data (`view.Encode`: Go field names, typed strings,
  `json:"-"` left out) instead of HTML; `view.Options.ClientFuncs` for functions with a browser
  side (constant calls precomputed); pages using other functions stay on the server. With
  `Reload`, templates are parsed again only when a file changed.
- `ServePack` takes a weak `If-None-Match` (a compressing middleware weakens the ETag).
- Kept pages: `pages.Options.Cache` (per page: `TTL`, `Public` or for `Guest`s, `Vary`), `Store`
  with `Purge`; ETags and 304; responses with cookies, other statuses or a CSRF field are not kept.
  `pages:prerender` (`PrerenderCommand`) writes the public pages as HTML files, served from
  `Options.Prerendered`.
- Console: `App.Console(os.Args[1:])` runs commands of the application binary (`go run . <command>`);
  `App.AddCommand`, `Command`, `ExitError`; built-in `list`, `routes:list` and generators
  `make:model [-m]`, `make:controller`, `make:middleware`, `make:provider`, `make:seeder`,
  `make:test` (`Generate`, `MakeTemplates` and `MakeDirs` to customize them). `Router.Routes`.
