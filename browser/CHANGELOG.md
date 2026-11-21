# Changelog

## Unreleased

## 0.2.1 — 2026-11-21

## 0.2.0 — 2026-11-19

### Changed
- Renamed from `fullstack-browser` to `@fullstack/browser` (gtr ADR-0012): `gtr add @fullstack/browser`, `import "fullstack/browser"`; published to the gtr registry. Dependencies on fullstack, passport, orm and migration packages are registry ranges instead of `github:` sources.

### Added
- `browser.New`: an `http.Handler` on an httptest server and a headless Chrome (chromedp), skipped
  when there is no Chrome (`FULLSTACK_CHROME`, `FULLSTACK_BROWSER=off|show`); `Page` with `Visit`,
  `Click`, `Fill`, `WaitFor`, `WaitText`, `WaitPath` (through page loads and in-place swaps),
  `Text`, `Texts`, `Title`, `Path`, `Eval`, `Mark`/`Reloaded`; `Timeout`, `Flag`.
