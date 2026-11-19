package fullstack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

// MakeDirs are where the make:* commands write, relative to the working
// directory (the project root under `go run .`).
var MakeDirs = map[string]string{
	"model":      "app/models",
	"controller": "app/http/controllers",
	"middleware": "app/http/middleware",
	"provider":   "app/providers",
	"seeder":     "database/seeders",
	"test":       "tests",
}

// MakeTemplates are the text/template sources of the make:* commands;
// replace one to change what a project generates. The data has Name
// (Post), Snake (blog_post), Table (blog_posts), Var (post), Package and
// Module (the module path from go.mod, for imports).
var MakeTemplates = map[string]string{
	"model": `package {{.Package}}

import "orm"

// {{.Name}} is a row of the {{.Table}} table.
type {{.Name}} struct {
	orm.BaseModel
	orm.Timestamps
}

// TableName implements orm.Model.
func ({{.Name}}) TableName() string { return "{{.Table}}" }
`,
	"controller": `package {{.Package}}

import (
	"fullstack/web"
)

// {{.Name}}Controller handles the {{.Var}} resource.
type {{.Name}}Controller struct{}

// Routes registers the resource routes:
//
//	GET    /{{.Table}}            Index
//	POST   /{{.Table}}            Store
//	GET    /{{.Table}}/{id}       Show
//	PUT    /{{.Table}}/{id}       Update
//	DELETE /{{.Table}}/{id}       Destroy
func (h {{.Name}}Controller) Routes(r *web.Router) {
	r.Group("/{{.Table}}", func(g *web.Router) {
		g.Get("/", h.Index).Name("{{.Table}}.index")
		g.Post("/", h.Store).Name("{{.Table}}.store")
		g.Get("/{id}", h.Show).Name("{{.Table}}.show")
		g.Put("/{id}", h.Update).Name("{{.Table}}.update")
		g.Delete("/{id}", h.Destroy).Name("{{.Table}}.destroy")
	})
}

func (h {{.Name}}Controller) Index(c *web.Ctx) error   { return c.JSON(200, []any{}) }
func (h {{.Name}}Controller) Store(c *web.Ctx) error   { return c.JSON(201, map[string]any{}) }
func (h {{.Name}}Controller) Show(c *web.Ctx) error    { return web.ErrNotFound }
func (h {{.Name}}Controller) Update(c *web.Ctx) error  { return web.ErrNotFound }
func (h {{.Name}}Controller) Destroy(c *web.Ctx) error { return c.NoContent() }
`,
	"middleware": `package {{.Package}}

import "net/http"

// {{.Name}} is a middleware: r.Use({{.Package}}.{{.Name}}).
func {{.Name}}(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
`,
	"provider": `package {{.Package}}

import "fullstack"

// {{.Name}} is a service provider: app.Register(&{{.Package}}.{{.Name}}{}).
type {{.Name}} struct{}

// Register adds services to the container (fullstack.Provide).
func (p *{{.Name}}) Register(app *fullstack.App) error {
	return nil
}

// Boot runs when every provider is registered, before serving.
func (p *{{.Name}}) Boot(app *fullstack.App) error {
	return nil
}
`,
	"seeder": `package {{.Package}}

import (
	"context"

	"fullstack"
)

// {{.Name}} seeds the database: database.Seeder{Name: "{{.Name}}", Run: {{.Package}}.{{.Name}}}.
func {{.Name}}(ctx context.Context, app *fullstack.App) error {
	return nil
}
`,
	"test": `package {{.Package}}

import (
	"testing"

	apptest "{{.Testing}}"
)

func Test{{.Name}}(t *testing.T) {
	c := apptest.New(t, newApp(t))
	c.Get("/").AssertOK()
}
`,
}

var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

func makeCommand(kind, usage, desc string) Command {
	return Command{Name: "make:" + kind, Usage: usage, Description: desc, Run: func(_ context.Context, a *App, args []string) error {
		fs := flag.NewFlagSet("make:"+kind, flag.ContinueOnError)
		fs.SetOutput(a.Stderr)
		withMigration := false
		if kind == "model" {
			fs.BoolVar(&withMigration, "m", false, "also create the migration (make:migration create_<table>_table)")
		}
		// Flags may follow the name, as in artisan: make:model Post -m.
		var flags, rest []string
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				flags = append(flags, a)
			} else {
				rest = append(rest, a)
			}
		}
		if err := fs.Parse(append(flags, rest...)); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("make:%s needs a name, e.g. make:%s Post", kind, kind)
		}
		path, err := Generate(kind, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "Created %s\n", path)
		if withMigration {
			a.mu.Lock()
			mig, ok := a.commands["make:migration"]
			a.mu.Unlock()
			if !ok {
				return errors.New("make:model -m needs make:migration (fullstack/orm's database.Provider)")
			}
			return mig.Run(context.Background(), a, []string{"create_" + tableName(fs.Arg(0)) + "_table"})
		}
		return nil
	}}
}

// Generate writes a file of kind (model, controller, middleware, provider,
// seeder, test) for name ("Post", "BlogPost") from MakeTemplates into
// MakeDirs, and returns its path. An existing file is not overwritten.
func Generate(kind, name string) (string, error) {
	src, ok := MakeTemplates[kind]
	if !ok {
		return "", fmt.Errorf("make: unknown kind %q", kind)
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "Controller")
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("make:%s: %q is not a name (letters and digits, e.g. BlogPost)", kind, name)
	}
	name = string(unicode.ToUpper(rune(name[0]))) + name[1:]
	dir := MakeDirs[kind]
	pkg := filepath.Base(dir)
	if kind == "test" {
		pkg = "tests"
	}
	snake := snakeCase(name)
	data := map[string]string{
		"Name": name, "Snake": snake, "Table": tableName(name), "Package": pkg, "Module": modulePath(), "Testing": testingImport(),
		"Var": string(unicode.ToLower(rune(name[0]))) + name[1:],
	}
	var buf bytes.Buffer
	if err := template.Must(template.New(kind).Parse(src)).Execute(&buf, data); err != nil {
		return "", err
	}
	out, err := format.Source(buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("make:%s: the template does not produce Go code: %w", kind, err)
	}
	file := snake + ".go"
	if kind == "controller" {
		file = snake + "_controller.go"
	}
	if kind == "test" {
		file = snake + "_test.go"
	}
	path := filepath.Join(dir, file)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, out, 0o644)
}

// snakeCase turns BlogPost into blog_post (and HTTPServer into http_server).
func snakeCase(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 && (unicode.IsLower(rs[i-1]) || i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1])) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// tableName is the plural snake case of a model name: Post → posts,
// Category → categories, Box → boxes.
func tableName(name string) string {
	s := snakeCase(name)
	switch {
	case strings.HasSuffix(s, "y") && len(s) > 1 && !strings.ContainsRune("aeiou", rune(s[len(s)-2])):
		return s[:len(s)-1] + "ies"
	case strings.HasSuffix(s, "s"), strings.HasSuffix(s, "x"), strings.HasSuffix(s, "z"),
		strings.HasSuffix(s, "ch"), strings.HasSuffix(s, "sh"):
		return s + "es"
	}
	return s + "s"
}

// modulePath reads the module path from ./go.mod ("" without one).
// testingImport is the import path of the testing package the project
// depends on: "fullstack/testing" (@fullstack/testing), or the old
// "fullstack/testing" when gtr.json still names that.
func testingImport() string {
	data, err := os.ReadFile("gtr.json")
	if err != nil {
		return "fullstack/testing"
	}
	var m struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &m) == nil {
		if _, old := m.DevDependencies["fullstack/testing"]; old {
			return "fullstack/testing"
		}
		if _, old := m.Dependencies["fullstack/testing"]; old {
			return "fullstack/testing"
		}
	}
	return "fullstack/testing"
}

func modulePath() string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}
