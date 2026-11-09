package fullstack

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// The container holds the application's services by type: a provider
// registers how to build one (Provide) or the value itself (Set); Make
// builds it once, on first use, and returns the same value afterwards.
//
//	fullstack.Provide(app, func(a *fullstack.App) (*sql.DB, error) {
//	    return sql.Open("pgx", env.String("DATABASE_DSN", ""))
//	})
//	db, err := fullstack.Make[*sql.DB](app)

type entry struct {
	mu    sync.Mutex
	build func(*App) (any, error)
	done  bool
	val   any
}

type container struct {
	mu      sync.RWMutex
	entries map[reflect.Type]*entry
}

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// Provide registers how to build the service T. The function runs once, on
// the first Make[T]; it receives the App to Make its own dependencies. An
// error is returned by that Make and the next Make tries again. Providing a
// type again replaces it (before it was built).
func Provide[T any](a *App, build func(*App) (T, error)) {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	a.c.entries[typeOf[T]()] = &entry{build: func(a *App) (any, error) { return build(a) }}
}

// Set registers an already built service T.
func Set[T any](a *App, v T) {
	a.c.mu.Lock()
	defer a.c.mu.Unlock()
	a.c.entries[typeOf[T]()] = &entry{done: true, val: v}
}

// Has reports whether T is registered.
func Has[T any](a *App) bool {
	a.c.mu.RLock()
	defer a.c.mu.RUnlock()
	_, ok := a.c.entries[typeOf[T]()]
	return ok
}

// Make returns the service T, building it on first use. A missing service
// and a dependency cycle (A needs B needs A) are errors naming the types.
func Make[T any](a *App) (T, error) {
	var zero T
	t := typeOf[T]()
	for _, s := range a.stack {
		if s == t {
			names := make([]string, 0, len(a.stack)+1)
			for _, x := range a.stack {
				names = append(names, x.String())
			}
			return zero, fmt.Errorf("fullstack: dependency cycle: %s -> %s", strings.Join(names, " -> "), t)
		}
	}
	a.c.mu.RLock()
	e, ok := a.c.entries[t]
	a.c.mu.RUnlock()
	if !ok {
		return zero, fmt.Errorf("fullstack: no provider for %s (fullstack.Provide or a service provider registers it)", t)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.done {
		// The builder gets an App that remembers what is being built, so
		// that a cycle is an error instead of a deadlock.
		child := &App{core: a.core, stack: append(append([]reflect.Type(nil), a.stack...), t)}
		v, err := e.build(child)
		if err != nil {
			return zero, fmt.Errorf("fullstack: building %s: %w", t, err)
		}
		e.val, e.done = v, true
	}
	v, _ := e.val.(T) // a nil interface value stays the zero T
	return v, nil
}

// MustMake is Make that panics: for wiring at startup, where a missing
// service is a programming error.
func MustMake[T any](a *App) T {
	v, err := Make[T](a)
	if err != nil {
		panic(err)
	}
	return v
}
