package cli

// `gmk schema` — emit a JSON Schema describing the build.yml file format.
//
// Editors with JSON-Schema support (VS Code, neovim with coc-yaml, IntelliJ)
// can ingest this schema and provide autocomplete, validation, and hover
// docs for every top-level key, every target field, every function field.
//
// The schema is hand-maintained in this file (rather than generated from
// the Go structs by reflection) because the YAML schema is broader than
// the Go types — it accepts shorthand forms (e.g. `deps: foo` instead of
// `deps: [foo]`) that don't have a 1:1 Go representation. Hand-writing
// makes the trade-offs explicit.
//
// Naming convention: schema IDs use a stable URL pattern so multiple
// versions can coexist if a project pins a specific gmk version.

import (
	"github.com/spf13/cobra"
)

func newSchemaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Emit a JSON Schema for build.yml (for editor LSPs)",
		Long: `Print a JSON Schema (draft 2020-12) describing the build.yml file format.

Pipe to a file and point your editor's YAML-LSP config at it:

  gmk schema > .gmk.schema.json

Then in .vscode/settings.json:
  "yaml.schemas": { ".gmk.schema.json": ["build.yml", "*.gmk.yml"] }

The schema describes the v0.4 (Stage 3b) shape: top-level keys
includes/vars/vars_N/targets/functions/languages, every per-callable
field, every parameter shape, and the typed-result form.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return jsonEncode(cmd.OutOrStdout(), buildSchemaDoc())
		},
	}
	return cmd
}

// buildSchemaDoc returns the JSON-Schema document as a nested map.
// Using map[string]any keeps the source readable and lets us emit
// stable JSON via the existing jsonEncode helper. The $defs section
// holds the shared Callable/Function/Param/Result/Prelude/Language
// shapes referenced by $ref from the top-level properties.
func buildSchemaDoc() map[string]any {
	return map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "https://github.com/suhrut/gmk/schema/v0.4/build.yml.json",
		"title":       "gmk build.yml",
		"description": "Schema for gmk Stage 3b build files.",
		"type":        "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"includes":  schemaIncludes(),
			"vars":      schemaVarsBlock(),
			"targets":   schemaTargets(),
			"functions": schemaFunctions(),
			"languages": schemaLanguages(),
		},
		"patternProperties": map[string]any{
			"^vars_[0-9]+$": schemaVarsBlock(),
		},
		"$defs": map[string]any{
			"Callable": schemaCallable(),
			"Function": schemaFunction(),
			"Language": schemaLanguage(),
			"Param":    schemaParam(),
			"Result":   schemaResult(),
			"Prelude":  schemaPrelude(),
		},
	}
}

func schemaIncludes() map[string]any {
	return map[string]any{
		"description": "List of files or library imports to merge before this one. " +
			"Library imports use the lib:<name> form; relative paths resolve from this file.",
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
}

func schemaVarsBlock() map[string]any {
	return map[string]any{
		"description": "Map of variable name to value. Values may be strings, " +
			"numbers, or booleans; strings may contain ${...} substitutions.",
		"type": "object",
		"additionalProperties": map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "number"},
				map[string]any{"type": "boolean"},
				map[string]any{"type": "null"},
			},
		},
	}
}

func schemaTargets() map[string]any {
	return map[string]any{
		"description": "Map of target name to target definition. Targets are " +
			"invoked by `gmk run`.",
		"type": "object",
		"additionalProperties": map[string]any{
			"$ref": "#/$defs/Callable",
		},
	}
}

func schemaFunctions() map[string]any {
	return map[string]any{
		"description": "Map of function name to function definition. " +
			"Functions are invoked by `gmk call` or expression ${call:name(args)}.",
		"type": "object",
		"additionalProperties": map[string]any{
			"$ref": "#/$defs/Function",
		},
	}
}

func schemaLanguages() map[string]any {
	return map[string]any{
		"description": "User-defined language interpreters. Built-ins (bash, " +
			"sh, python, ruby, node, perl) are always available; entries here " +
			"override or extend the built-in set.",
		"type": "object",
		"additionalProperties": map[string]any{
			"$ref": "#/$defs/Language",
		},
	}
}

// schemaCallable is the shape shared between a Target and the body-level
// portion of a Function. Target-only fields (deps, phony) live here too,
// flagged as optional in their descriptions.
func schemaCallable() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"run":     map[string]any{"type": "string", "description": "Body script."},
			"script":  map[string]any{"type": "string", "description": "Alias for run."},
			"lang":    map[string]any{"type": "string", "description": "Interpreter language (bash|sh|python|ruby|node|perl, or user-defined)."},
			"deps":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Target dependencies (target-only)."},
			"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"cwd":     map[string]any{"type": "string", "description": "Working directory; relative to project root."},
			"phony":   map[string]any{"type": "boolean", "description": "Target-only: always run, never skip."},
			"prelude": map[string]any{"$ref": "#/$defs/Prelude"},
			"doc":     map[string]any{"type": "string", "description": "Human-readable description."},
		},
	}
}

func schemaFunction() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"doc":     map[string]any{"type": "string"},
			"params":  map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/Param"}},
			"result":  map[string]any{"$ref": "#/$defs/Result"},
			"prelude": map[string]any{"$ref": "#/$defs/Prelude"},
			"run":     map[string]any{"type": "string"},
			"script":  map[string]any{"type": "string"},
			"lang":    map[string]any{"type": "string"},
			"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"cwd":     map[string]any{"type": "string"},
		},
	}
}

func schemaParam() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"name", "type"},
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"type":    map[string]any{"type": "string", "enum": []any{"string", "int", "float", "bool", "list", "map"}},
			"default": map[string]any{"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "number"},
				map[string]any{"type": "boolean"},
				map[string]any{"type": "null"},
			}},
			"doc": map[string]any{"type": "string"},
		},
	}
}

func schemaResult() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"type": map[string]any{"type": "string", "enum": []any{"string", "int", "float", "bool", "list", "map", "none"}},
			"doc":  map[string]any{"type": "string"},
		},
	}
}

func schemaPrelude() map[string]any {
	return map[string]any{
		"description": "Ordered map of name to gmk-time expression. " +
			"Later bindings may reference earlier ones.",
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "string"},
	}
}

func schemaLanguage() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"interpreter"},
		"properties": map[string]any{
			"interpreter": map[string]any{"type": "string", "description": "Binary name or absolute path."},
			"args":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"ext":         map[string]any{"type": "string", "description": "File extension (with leading dot)."},
		},
	}
}
