# Changelog

## Unreleased

## 0.2.1 — 2026-11-21

## 0.2.0 — 2026-11-19

### Changed
- Renamed from `fullstack-testing` to `@fullstack/testing` (gtr ADR-0012): `gtr add @fullstack/testing`, `import "fullstack/testing"`; published to the gtr registry. Dependencies on fullstack, passport, orm and migration packages are registry ranges instead of `github:` sources.

### Added
- `apptest.New(t, app)`: boots the application and sends requests to it in memory; a cookie jar
  between requests, the CSRF token sent on POST/PUT/PATCH/DELETE (`WithoutCSRF` to test refusals),
  `WithHeader`, `WithToken`, `Cookie`, `SetCookie`; `Get`, `GetJSON`, `Post` (forms), `PostJSON`,
  `PutJSON`, `PatchJSON`, `Delete`, `Do`. `NewHandler` for a bare `http.Handler`.
- `ActingAs(identity)` (a passport session, the web guard), `ActingAsAPI(identity)` (a Bearer
  token, the api guard), `Logout`.
- Assertions: `AssertStatus`, `AssertOK`, `AssertCreated`, `AssertNoContent`, `AssertBadRequest`,
  `AssertUnauthorized`, `AssertForbidden`, `AssertNotFound`, `AssertUnprocessable`,
  `AssertSuccessful`, `AssertRedirect`, `AssertSee` / `AssertDontSee` (HTML-escaped text matches),
  `AssertHeader`, `AssertCookie`, `AssertJSONPath`, `AssertJSONMissing`, `AssertJSONCount`,
  `AssertJSONValidationErrors`; `JSONPath`, `DecodeJSON`, `Status`, `Text`.
