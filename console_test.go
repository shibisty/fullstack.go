package fullstack_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fullstack"
	"fullstack/web"
)

func consoleApp(t *testing.T) (*fullstack.App, *bytes.Buffer, *bytes.Buffer) {
	a, _ := newApp(t, "APP_NAME=demo")
	var out, errb bytes.Buffer
	a.Stdout, a.Stderr = &out, &errb
	return a, &out, &errb
}

func TestConsole(t *testing.T) {
	a, out, errb := consoleApp(t)
	if _, ok := a.Console(nil); ok {
		t.Fatal("no args handled")
	}
	if _, ok := a.Console([]string{"-v"}); ok {
		t.Fatal("flags handled")
	}
	if _, ok := a.Console([]string{"serve"}); ok {
		t.Fatal("serve handled")
	}
	a.AddCommand(fullstack.Command{Name: "hello", Usage: "<who>", Description: "Say hello", Run: func(_ context.Context, a *fullstack.App, args []string) error {
		if len(args) == 0 {
			return errors.New("hello whom?")
		}
		if args[0] == "quiet" {
			return fullstack.ExitError{Code: 3}
		}
		_, err := a.Stdout.Write([]byte("hello " + args[0] + "\n"))
		return err
	}})
	if code, ok := a.Console([]string{"hello", "Ann"}); !ok || code != 0 || out.String() != "hello Ann\n" {
		t.Fatal(code, out)
	}
	if code, _ := a.Console([]string{"hello"}); code != 1 || !strings.Contains(errb.String(), "hello whom?") {
		t.Fatal(code, errb)
	}
	errb.Reset()
	if code, _ := a.Console([]string{"hello", "quiet"}); code != 3 || errb.Len() != 0 {
		t.Fatal(code, errb)
	}
	if (fullstack.ExitError{Code: 2}).Error() != "exit status 2" {
		t.Fatal()
	}
	out.Reset()
	if code, _ := a.Console([]string{"help"}); code != 0 || !strings.Contains(out.String(), "hello <who>") || !strings.Contains(out.String(), "make:model <Name>") || !strings.Contains(out.String(), "demo commands") {
		t.Fatal(out)
	}
	errb.Reset()
	if code, _ := a.Console([]string{"nope"}); code != 1 || !strings.Contains(errb.String(), `unknown command "nope"`) || !strings.Contains(errb.String(), "routes:list") {
		t.Fatal(errb)
	}
}

type routesProvider struct{}

func (routesProvider) Register(*fullstack.App) error { return nil }
func (routesProvider) Boot(a *fullstack.App) error {
	a.Router.Get("/", func(*web.Ctx) error { return nil }).Name("home")
	a.Router.Post("/login", func(*web.Ctx) error { return nil })
	a.Router.Get("/login", func(*web.Ctx) error { return nil }).Name("login")
	a.Router.Any("/hook", func(*web.Ctx) error { return nil })
	return nil
}

func TestRoutesList(t *testing.T) {
	a, out, _ := consoleApp(t)
	a.Register(routesProvider{})
	if code, _ := a.Console([]string{"routes:list"}); code != 0 {
		t.Fatal(code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"METHOD PATH NAME", "GET /  home", "POST /login", "GET /login login", "ANY /hook"}
	for i, w := range want {
		if strings.Join(strings.Fields(lines[i]), " ") != strings.Join(strings.Fields(w), " ") {
			t.Fatalf("line %d: %q, want %q\n%s", i, lines[i], w, out)
		}
	}
	b, _, errb := consoleApp(t)
	b.Register(badBoot{})
	if code, _ := b.Console([]string{"routes:list"}); code != 1 || !strings.Contains(errb.String(), "boot failed") {
		t.Fatal(code, errb)
	}
}

type badBoot struct{}

func (badBoot) Register(*fullstack.App) error { return nil }
func (badBoot) Boot(*fullstack.App) error     { return errors.New("boot failed") }

func TestMake(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)
	os.WriteFile("go.mod", []byte("module blog\n\ngo 1.22\n"), 0o644)
	a, out, errb := consoleApp(t)
	for _, c := range []struct {
		args []string
		file string
		has  []string
	}{
		{[]string{"make:model", "BlogPost"}, "app/models/blog_post.go", []string{"package models", "type BlogPost struct", `return "blog_posts"`}},
		{[]string{"make:model", "category"}, "app/models/category.go", []string{"type Category struct", `"categories"`}},
		{[]string{"make:model", "Box"}, "app/models/box.go", []string{`"boxes"`}},
		{[]string{"make:controller", "PostController"}, "app/http/controllers/post_controller.go", []string{"package controllers", "type PostController struct", `g.Get("/{id}", h.Show).Name("posts.show")`}},
		{[]string{"make:middleware", "RequireAdmin"}, "app/http/middleware/require_admin.go", []string{"func RequireAdmin(next http.Handler)"}},
		{[]string{"make:provider", "AppServiceProvider"}, "app/providers/app_service_provider.go", []string{"func (p *AppServiceProvider) Register"}},
		{[]string{"make:seeder", "UserSeeder"}, "database/seeders/user_seeder.go", []string{"package seeders", "func UserSeeder(ctx context.Context"}},
		{[]string{"make:test", "HTTPServer"}, "tests/http_server_test.go", []string{"package tests", "func TestHTTPServer"}},
	} {
		out.Reset()
		if code, _ := a.Console(c.args); code != 0 {
			t.Fatalf("%v: %d %s", c.args, code, errb)
		}
		data, err := os.ReadFile(filepath.FromSlash(c.file))
		if err != nil {
			t.Fatalf("%v: %v (%s)", c.args, err, out)
		}
		for _, h := range c.has {
			if !strings.Contains(string(data), h) {
				t.Errorf("%s lacks %q:\n%s", c.file, h, data)
			}
		}
		if !strings.Contains(out.String(), "Created "+filepath.FromSlash(c.file)) {
			t.Errorf("output %q", out)
		}
	}
	errb.Reset()
	for _, bad := range [][]string{{"make:model"}, {"make:model", "1x"}, {"make:model", "BlogPost"}, {"make:model", "-z", "A"}} {
		if code, _ := a.Console(bad); code != 1 {
			t.Errorf("%v accepted", bad)
		}
	}
	if !strings.Contains(errb.String(), "already exists") {
		t.Fatal(errb)
	}
	// -m needs make:migration, which fullstack-orm adds.
	errb.Reset()
	if code, _ := a.Console([]string{"make:model", "-m", "Tag"}); code != 1 || !strings.Contains(errb.String(), "needs make:migration") {
		t.Fatal(errb)
	}
	var migArgs []string
	a.AddCommand(fullstack.Command{Name: "make:migration", Run: func(_ context.Context, _ *fullstack.App, args []string) error {
		migArgs = args
		return nil
	}})
	if code, _ := a.Console([]string{"make:model", "Reply", "-m"}); code != 0 || strings.Join(migArgs, " ") != "create_replies_table" {
		t.Fatal(code, migArgs, errb)
	}
	if _, err := fullstack.Generate("nope", "X"); err == nil {
		t.Fatal("unknown kind")
	}
	old := fullstack.MakeTemplates["model"]
	fullstack.MakeTemplates["model"] = "not go {{.Name}}"
	defer func() { fullstack.MakeTemplates["model"] = old }()
	if _, err := fullstack.Generate("model", "Broken"); err == nil || !strings.Contains(err.Error(), "does not produce Go code") {
		t.Fatal(err)
	}
	os.Remove("go.mod")
	fullstack.MakeTemplates["model"] = "package {{.Package}} // {{.Module}}\n"
	if p, err := fullstack.Generate("model", "NoMod"); err != nil {
		t.Fatal(err)
	} else if data, _ := os.ReadFile(p); !strings.Contains(string(data), "package models //") {
		t.Fatal(string(data))
	}
	os.WriteFile("blockfile", []byte("a file, not a directory"), 0o644)
	oldDir := fullstack.MakeDirs["seeder"]
	fullstack.MakeDirs["seeder"] = "blockfile/seeders"
	defer func() { fullstack.MakeDirs["seeder"] = oldDir }()
	if _, err := fullstack.Generate("seeder", "X"); err == nil {
		t.Fatal("unwritable directory")
	}
}
