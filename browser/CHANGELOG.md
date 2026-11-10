# Changelog

## Unreleased

### Added
- `browser.New`: an `http.Handler` on an httptest server and a headless Chrome (chromedp), skipped
  when there is no Chrome (`FULLSTACK_CHROME`, `FULLSTACK_BROWSER=off|show`); `Page` with `Visit`,
  `Click`, `Fill`, `WaitFor`, `WaitText`, `WaitPath` (through page loads and in-place swaps),
  `Text`, `Texts`, `Title`, `Path`, `Eval`, `Mark`/`Reloaded`; `Timeout`, `Flag`.
