# fullstack-browser

End-to-end tests of a [fullstack](https://github.com/shibisty/fullstack.go) app (or any `http.Handler`)
in a headless Chrome, written as ordinary Go tests — no Node.js, no Playwright. The app is served by
`httptest`, Chrome is driven through the DevTools protocol ([chromedp](https://github.com/chromedp/chromedp)).

## Installation

```bash
gtr add -D github:shibisty/fullstack.go-browser
```

Needs Go 1.24+ (chromedp) and Chrome, Chromium or Edge on the machine; GitHub's runners have Chrome.

## Use

```go
func TestLogin(t *testing.T) {
    h, err := newApp(t).Handler()
    if err != nil {
        t.Fatal(err)
    }
    p := browser.New(t, h).Page()
    p.Visit("/login").Mark()
    p.Fill(`input[name="email"]`, "ann@example.com")
    p.Fill(`input[name="password"]`, "secret")
    p.Click("form button")
    p.WaitPath("/").WaitText("nav", "Log out")
    if p.Reloaded() {
        t.Fatal("the page was loaded anew, not swapped in place")
    }
}
```

| | |
|---|---|
| `New(t, handler, opts...)` | serves the handler, starts Chrome; both stop with the test |
| `Browser.Page()` | a tab (tabs share cookies); `Browser.URL()` the app's address |
| `Visit(path)` | loads a path (or a URL) and waits for the page |
| `Click(sel)`, `Fill(sel, text)` | CSS selectors; `Fill` replaces the value |
| `WaitFor(sel)`, `WaitText(sel, text)`, `WaitPath(path)` | wait through page loads and swaps |
| `Text(sel)`, `Texts(sel)`, `Title()`, `Path()` | read the page |
| `Eval(js, &out)` | runs JavaScript; promises are awaited |
| `Mark()`, `Reloaded()` | whether the document was replaced by a full load since `Mark` |

Every step fails the test with what it was doing and the page's URL; one step may take 10 seconds
(`browser.Timeout(d)`). `browser.Flag(name, value)` passes a flag to Chrome.

Without Chrome the test is skipped. `FULLSTACK_CHROME` names the executable,
`FULLSTACK_BROWSER=off` skips browser tests, `FULLSTACK_BROWSER=show` opens a visible window.

## License

MIT
