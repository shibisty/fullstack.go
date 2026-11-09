package fullstack

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// Command is a console command of the application binary, like an artisan
// command: `go run . routes:list`, `go run . make:model Post`.
type Command struct {
	Name        string // "make:model"
	Usage       string // arguments, for help: "<Name>"
	Description string
	Run         func(ctx context.Context, app *App, args []string) error
}

// ExitError ends a command with an exit code without printing anything
// more (the command has already reported).
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// AddCommand adds console commands; a name already taken is replaced.
func (a *App) AddCommand(cmds ...Command) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.commands == nil {
		a.commands = map[string]Command{}
	}
	for _, c := range cmds {
		a.commands[c.Name] = c
	}
}

func (a *App) builtinCommands() {
	a.AddCommand(
		Command{Name: "list", Description: "List the commands", Run: func(_ context.Context, a *App, _ []string) error {
			a.printCommands(a.Stdout)
			return nil
		}},
		Command{Name: "routes:list", Description: "List the routes (after booting the providers)", Run: routesList},
		makeCommand("model", "<Name>", "Create a model in app/models (with -m, its migration too)"),
		makeCommand("controller", "<Name>", "Create a resource controller in app/http/controllers"),
		makeCommand("middleware", "<Name>", "Create a middleware in app/http/middleware"),
		makeCommand("provider", "<Name>", "Create a service provider in app/providers"),
		makeCommand("seeder", "<Name>", "Create a seeder in database/seeders"),
		makeCommand("test", "<Name>", "Create an HTTP test in tests"),
	)
}

// Console runs the command named by args[0] and reports whether there was
// one: with no arguments (or only flags) it returns false and the caller
// serves HTTP. Output goes to App.Stdout and App.Stderr.
//
//	func main() {
//	    app := bootstrap.NewApp()
//	    if code, ok := app.Console(os.Args[1:]); ok {
//	        os.Exit(code)
//	    }
//	    app.Run(context.Background())
//	}
func (a *App) Console(args []string) (code int, handled bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return 0, false
	}
	name := args[0]
	if name == "help" || name == "--help" {
		name = "list"
	}
	if name == "serve" {
		return 0, false
	}
	a.mu.Lock()
	cmd, ok := a.commands[name]
	a.console = true
	a.mu.Unlock()
	if !ok {
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", name)
		a.printCommands(a.Stderr)
		return 1, true
	}
	if err := cmd.Run(context.Background(), a, args[1:]); err != nil {
		var ee ExitError
		if errors.As(err, &ee) {
			return ee.Code, true
		}
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		fmt.Fprintln(a.Stderr, err)
		return 1, true
	}
	return 0, true
}

func (a *App) printCommands(w io.Writer) {
	a.mu.Lock()
	cmds := make([]Command, 0, len(a.commands))
	for _, c := range a.commands {
		cmds = append(cmds, c)
	}
	a.mu.Unlock()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	fmt.Fprintf(w, "%s commands (go run . <command>; no command serves HTTP):\n", a.Config.Name)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range cmds {
		fmt.Fprintf(tw, "  %s %s\t%s\n", c.Name, c.Usage, c.Description)
	}
	tw.Flush()
}

func routesList(_ context.Context, a *App, _ []string) error {
	if err := a.Boot(); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tNAME")
	for _, r := range a.Router.Routes() {
		m := r.Method
		if m == "" {
			m = "ANY"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m, strings.TrimSuffix(r.Path, "{$}"), r.Name)
	}
	return tw.Flush()
}
