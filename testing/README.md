# @fullstack/testing

Test helpers for [fullstack](https://github.com/shibisty/fullstack.go) applications, in the spirit of
Laravel's HTTP tests: an in-memory client that keeps cookies and sends the CSRF token like a browser,
`ActingAs`, and chained assertions.

## Installation

```json
{
    "devDependencies": {
        "@fullstack/testing": "^0.2"
    }
}
```

```go
import apptest "fullstack/testing"
```

## Usage

```go
func TestNotes(t *testing.T) {
    app := bootstrap.NewApp()          // the App with its providers
    c := apptest.New(t, app)           // boots it; a failing boot fails the test

    c.Get("/").AssertOK().AssertSee("Notes")
    c.Get("/dashboard").AssertRedirect("/login?redirect=%2Fdashboard")

    c.ActingAs(&passport.Identity{ProviderUserID: "1", Name: "Ann"}).
        Post("/notes", url.Values{"text": {"Hello"}}).AssertRedirect("/")

    c.PostJSON("/api/notes", map[string]any{"text": ""}).
        AssertUnprocessable().
        AssertJSONValidationErrors("text")

    c.GetJSON("/api/notes").AssertOK().AssertJSONCount("", 2).AssertJSONPath("0.text", "Hello")
}
```

- The client keeps cookies between requests and sends the `csrf_token` cookie's value in
  `X-CSRF-Token` on POST, PUT, PATCH and DELETE, as a page's JavaScript would; `c.WithoutCSRF()`
  tests that a route refuses requests without it. Get the cookie with a first GET.
- `c.ActingAs(identity)` starts a passport session (the `web` guard); `c.ActingAsAPI(identity)` returns
  a client with a Bearer token (the `api` guard, needs `AUTH_JWT_SECRET`). The App must have
  passport (`auth.Provider` of @fullstack/passport).
- `WithHeader`, `WithToken` and `WithoutCSRF` return a client that shares the cookies.
- Database tests: `database.UseTransaction(t, app)` of @fullstack/orm rolls everything back at the end.

## Assertions

| | |
|---|---|
| `AssertStatus(code)`, `AssertOK`, `AssertCreated`, `AssertNoContent`, `AssertBadRequest`, `AssertUnauthorized`, `AssertForbidden`, `AssertNotFound`, `AssertUnprocessable`, `AssertSuccessful` | the status |
| `AssertRedirect()`, `AssertRedirect("/to")` | a 3xx, and its `Location` |
| `AssertSee(texts...)`, `AssertDontSee(texts...)` | the body, as written or HTML-escaped |
| `AssertHeader(k, v)`, `AssertCookie(name)` | headers and cookies of the response |
| `AssertJSONPath("data.0.name", "Ann")` | a value at a dotted path; numbers by value, structs and maps by their JSON |
| `AssertJSONMissing(path)`, `AssertJSONCount(path, n)` | absence; the length of an array or object (`""` is the root) |
| `AssertJSONValidationErrors(fields...)` | the `errors` of a @fullstack/validation 422 |

Assertions report with `t.Errorf` (the test continues) and return the response, so they chain.
`JSONPath`, `DecodeJSON`, `Status` and `Text` read the response for your own checks.

## License

MIT
