// Package template provides the pluggable text-template machinery used
// by the ${render:name(args)} expression operator.
//
// Why pluggable: templates are how gmk produces structured output —
// Dockerfiles, k8s manifests, nginx configs, generated source files.
// No single engine is right for every project. Jinja2 syntax is the
// ops/devops lingua franca (Ansible, Salt, Helm-via-helm-template all
// speak it); Go's text/template is the right choice when you want zero
// new deps and you're already in a Go-shop mental model. We ship both
// out of the box and the interface lets Stage 3f plugins add more
// (Mustache, Liquid, custom DSLs, out-of-process renderers) without
// touching anything in this package.
//
// Selection: each template declaration in YAML carries an optional
// `engine:` field. When omitted, the registry's default name is used
// (set to "jinja" at startup; can be reconfigured for tests). When an
// engine name isn't registered, the render fails with a clear error —
// no silent fallback to a different engine.
//
// Engine registration happens via package init(), so simply blank-
// importing an engine subpackage is enough to make it available:
//
//	import _ "github.com/suhrut/gmk/internal/template/jinja"
//
// This keeps cmd/gmk free of compile-time knowledge of which engines
// exist; new engines drop in by adding one blank import line.

package template

import (
	"fmt"
	"sync"
)

// Engine renders a template source string with bound data and returns
// the rendered output as a string. Implementations are expected to be
// safe for concurrent use — gmk may render multiple templates in
// parallel during a single command (especially once iteration
// combinators (3c.2) land).
type Engine interface {
	// Name returns the identifier used in YAML's `engine:` field.
	// Conventionally lowercase, short ("jinja", "go"). Two engines
	// with the same Name() in the same Registry is a programming
	// error (Register panics on collision).
	Name() string

	// Render compiles and executes the template source against the
	// given data map. The data shape is map[string]any with values
	// that are either primitives (string, bool, float64, int64),
	// nested maps, or slices — the lowest-common-denominator shape
	// every Go template engine understands. Conversion from gmk's
	// own Value type happens at the call boundary in expr; engines
	// don't deal with gmk-internal types.
	//
	// The first return value is the rendered text. The error wraps
	// the engine's own error (preserved verbatim for debuggability)
	// with a "engine X:" prefix so users can tell which engine
	// failed when multiple are in play.
	Render(source string, data map[string]any) (string, error)
}

// Registry holds the set of registered engines and tracks which one
// is the default (used when a template declaration omits `engine:`).
//
// Concurrency: Register/SetDefault/Get/Default may be called from
// multiple goroutines (engine init() functions race with each other
// during startup, and Get is called from concurrent renders). The
// internal map is protected by a RWMutex with Get on the read path.
type Registry struct {
	mu          sync.RWMutex
	engines     map[string]Engine
	defaultName string
}

// NewRegistry returns an empty Registry with no default engine.
// In normal use you don't call this — the package-level Default
// registry is what every other gmk package talks to. NewRegistry
// exists for tests that want isolation.
func NewRegistry() *Registry {
	return &Registry{engines: make(map[string]Engine)}
}

// Register adds an engine. Panics if an engine with the same Name()
// is already registered, because engine collisions are a configuration
// bug that should surface at startup, not silently shadow.
func (r *Registry) Register(e Engine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := e.Name()
	if _, dup := r.engines[name]; dup {
		panic(fmt.Sprintf("template: engine %q already registered", name))
	}
	r.engines[name] = e
}

// SetDefault designates which registered engine is used when a
// template declaration omits the `engine:` field. The named engine
// does not need to be registered yet at the time SetDefault is
// called — engine registration order varies with init() ordering,
// and forcing SetDefault to come last would couple unrelated init
// functions. The resolution check happens at lookup time in
// Default(), not here.
func (r *Registry) SetDefault(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultName = name
}

// Get returns the engine registered under name. The second return
// value is false when no such engine exists, letting callers produce
// a context-rich error rather than panicking.
func (r *Registry) Get(name string) (Engine, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.engines[name]
	return e, ok
}

// Default returns the engine designated by SetDefault. Returns an
// error (not a panic) when no default is set or when the named
// default isn't registered, because both conditions surface as a
// user-facing failure ("you set engine: jinja but the jinja engine
// isn't compiled into this binary") and should be reportable.
func (r *Registry) Default() (Engine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.defaultName == "" {
		return nil, fmt.Errorf("template: no default engine set")
	}
	e, ok := r.engines[r.defaultName]
	if !ok {
		return nil, fmt.Errorf("template: default engine %q is not registered "+
			"(was it omitted at build time? — e.g. -tags nogonja excludes the jinja engine)",
			r.defaultName)
	}
	return e, nil
}

// Names returns the registered engine names in deterministic order.
// Used by `gmk list --engines` (future) and by error messages that
// want to suggest "did you mean one of: jinja, go".
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.engines))
	for n := range r.engines {
		out = append(out, n)
	}
	// Stable order without pulling in sort: small slice, insertion-sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Default is the package-wide registry. Engine subpackages register
// into this via their init() functions; the rest of gmk reads from
// it via the top-level Get/Default/Names helpers below.
//
// Tests that need isolation construct their own *Registry with
// NewRegistry() rather than mutating this.
var Default = NewRegistry()

// Register adds an engine to the package-wide Default registry.
// Convenience over Default.Register for engine init() functions.
func Register(e Engine) { Default.Register(e) }

// SetDefault sets the default engine name on the package-wide registry.
// Called from main during startup after all engine init()s have run.
func SetDefault(name string) { Default.SetDefault(name) }

// Get is the package-wide convenience over Default.Get.
func Get(name string) (Engine, bool) { return Default.Get(name) }

// DefaultEngine is the package-wide convenience over Default.Default.
// Named to avoid collision with the SetDefault setter.
func DefaultEngine() (Engine, error) { return Default.Default() }
