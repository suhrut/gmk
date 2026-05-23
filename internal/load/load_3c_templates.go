// Stage 3c template-block parser. Lives in its own file to keep the
// 3c additions browseable separately from the 3b function/language
// parsing in load_3b.go, and to make the eventual stage-notes audit
// easier (one file = one stage's surface area).

package load

import (
	"fmt"
	"path/filepath"

	"github.com/suhrut/gmk/internal/ir"
)

// loadTemplates parses the top-level `templates:` block. Mirrors the
// loadFunctions/loadLanguages shape: the block is a mapping from
// template-name to a small struct {body|file, engine, doc}.
//
// Exactly one of `body` and `file` must be set; both or neither is a
// load-time error. We enforce this here, before any rendering ever
// happens, so a typo in the YAML fails fast at `gmk run`/`gmk call`
// rather than at the first attempt to actually render the template.
func loadTemplates(p *ir.Project, entry yamlMapEntry, file string) error {
	m, err := entry.Value.AsMapping()
	if err != nil {
		return fmt.Errorf("load %s: templates: %w", file, err)
	}
	for _, kv := range m.Entries {
		t, err := loadTemplate(kv, file)
		if err != nil {
			return err
		}
		if _, dup := p.Templates[t.Name]; dup {
			return fmt.Errorf("load %s:%d:%d: template %q declared twice",
				file, kv.KeyLine, kv.KeyCol, t.Name)
		}
		p.Templates[t.Name] = t
		p.TemplateOrder = append(p.TemplateOrder, t.Name)
	}
	return nil
}

// loadTemplate parses one template entry. The body/file mutual-exclusion
// and the file-path resolution (relative to the declaring YAML) happen
// here. Engine name is captured verbatim; the registered-engine check
// happens at render time, not load time, because plugins (Stage 3f) can
// register engines after load.
func loadTemplate(kv yamlMapEntry, file string) (*ir.Template, error) {
	name := kv.Key
	body, err := kv.Value.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: template %q: %w", file, name, err)
	}
	t := &ir.Template{
		Name:   name,
		Source: ir.SourceLoc{File: file, Line: kv.Value.Line, Column: kv.Value.Col},
	}
	allowed := map[string]bool{"body": true, "file": true, "engine": true, "doc": true}
	for _, e := range body.Entries {
		if !allowed[e.Key] {
			return nil, fmt.Errorf("load %s:%d:%d: template %q has unknown field %q "+
				"(accepted: body, file, engine, doc)",
				file, e.KeyLine, e.KeyCol, name, e.Key)
		}
		switch e.Key {
		case "body":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: template %q.body: %w", file, name, err)
			}
			t.Body = s
		case "file":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: template %q.file: %w", file, name, err)
			}
			t.File = s
		case "engine":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: template %q.engine: %w", file, name, err)
			}
			t.Engine = s
		case "doc":
			s, err := e.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: template %q.doc: %w", file, name, err)
			}
			t.Doc = s
		}
	}

	// Body/file mutual exclusion.
	switch {
	case t.Body == "" && t.File == "":
		return nil, fmt.Errorf("validate %s: template %q has neither body nor file "+
			"(one is required)",
			file, name)
	case t.Body != "" && t.File != "":
		return nil, fmt.Errorf("validate %s: template %q has both body and file "+
			"(exactly one is required)",
			file, name)
	}

	// Resolve File relative to the YAML file that declared the template,
	// matching how includes are resolved. This means templates included
	// from a library YAML look in the library's directory, not the
	// consumer's project root — keeps libraries self-contained.
	if t.File != "" {
		if filepath.IsAbs(t.File) {
			t.ResolvedFile = t.File
		} else {
			declDir := filepath.Dir(file)
			t.ResolvedFile = filepath.Clean(filepath.Join(declDir, t.File))
		}
	}

	return t, nil
}
