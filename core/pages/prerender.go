package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"fullstack"
)

// PrerenderCommand is the console command pages:prerender [dir]: it
// renders the Public pages without parameters (Options.Cache) through the
// application, as a visitor's GET, and writes them to dir (default
// "public/prerendered") as <path>/index.html. Serve them with
// Options.Prerendered (an embed of dir), or upload them to a static host.
// list is filled by Register, which runs when the app boots:
//
//	var site []pages.Page
//	app.AddCommand(pages.PrerenderCommand(&site))
//	// in Boot: site, err = pages.Register(...)
func PrerenderCommand(list *[]Page) fullstack.Command {
	return fullstack.Command{Name: "pages:prerender", Usage: "[dir]",
		Description: "Render the public pages without parameters into HTML files",
		Run: func(_ context.Context, app *fullstack.App, args []string) error {
			dir := "public/prerendered"
			if len(args) > 0 {
				dir = args[0]
			}
			h, err := app.Handler()
			if err != nil {
				return err
			}
			n := 0
			for _, p := range *list {
				if p.Cache == nil || !p.Cache.Public || strings.Contains(p.Pattern, "{") {
					continue
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p.Pattern, nil))
				if w.Code != http.StatusOK {
					return fmt.Errorf("pages:prerender: %s answered %d", p.Pattern, w.Code)
				}
				file := filepath.Join(dir, filepath.FromSlash(prerenderPath(p.Pattern)))
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(file, w.Body.Bytes(), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(app.Stdout, "%s → %s\n", p.Pattern, file)
				n++
			}
			fmt.Fprintf(app.Stdout, "Prerendered %d pages\n", n)
			return nil
		}}
}
