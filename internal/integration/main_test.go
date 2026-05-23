package integration

// TestMain bootstraps the template-engine registry the same way
// cmd/gmk/main.go does, so integration tests that exercise examples
// using ${render:...} see the same engine surface a real `gmk` would.
//
// Two registrations come from package init: the "go" engine
// (always available) registers from internal/template/go_engine.go,
// and the "jinja" engine registers from internal/template/jinja
// — but only when built without `-tags nogonja`. Under that tag, the
// blank import below resolves to a no-op stub package, and any
// example that requires jinja is detected and skipped by
// TestExamplesRun rather than failing.

import (
	"os"
	"testing"

	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/template"

	// Self-registers jinja engine via init() when built without
	// -tags nogonja. Under that tag, this import resolves to a
	// stub package with no init body — no-op.
	_ "github.com/suhrut/gmk/internal/template/jinja"
)

func TestMain(m *testing.M) {
	// Default engine. Match main.go's policy: jinja preferred.
	// If jinja isn't registered (-tags nogonja), default lookup
	// will fail; examples that use the default engine are skipped
	// by exampleNeedsMissingEngine() below.
	template.SetDefault("jinja")

	os.Exit(m.Run())
}

// exampleNeedsMissingEngine inspects a loaded project's template
// declarations and returns the missing engine name (or "" if all
// engines that the project uses are available).
//
// "Needs" = the template's Engine field is non-empty and that
// engine isn't registered, OR Engine is empty (meaning "use default")
// and the default isn't resolvable.
func exampleNeedsMissingEngine(templates map[string]*ir.Template) string {
	for _, t := range templates {
		if t.Engine == "" {
			if _, err := template.DefaultEngine(); err != nil {
				return "(default)"
			}
			continue
		}
		if _, ok := template.Get(t.Engine); !ok {
			return t.Engine
		}
	}
	return ""
}
