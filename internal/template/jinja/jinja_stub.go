// This file is the no-op stand-in for the jinja engine, used when
// building with -tags nogonja. Compiled in place of jinja.go (which
// imports gonja). Keeps the package importable so cmd/gmk's blank
// import doesn't break in minimal-deps builds; the cost is that any
// project using `engine: jinja` will fail at lookup time with a
// clear "default engine 'jinja' is not registered" error pointing at
// the missing build tag.

//go:build nogonja

package jinja

// No init(), no Engine. Importing this package becomes a no-op.
// The package must exist (even empty) so the blank import in
// cmd/gmk/main.go resolves cleanly in both build configurations.
