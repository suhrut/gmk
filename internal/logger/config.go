// JSON config file parsing for the logger.
//
// File format:
//
//	{
//	  "default": "warn",
//	  "format":  "human",
//	  "loggers": {
//	    "gmk.target.*":          "info",
//	    "gmk.target.release":    "debug",
//	    "gmk.plugin.gmk-plugin-git": "trace"
//	  },
//	  "outputs": {
//	    "gmk.target.release": ".gmk-cache/runs/last/release.log"
//	  }
//	}
//
// `default` is the default level (anything not matched).
// `loggers` maps name patterns to levels.
// `outputs` maps name patterns to file paths (one route per entry).
// `format` selects the terminal format ("human" or "json").
//
// Missing keys mean "use the default for that option". An empty file
// (i.e. just "{}") produces a Policy with default level info and no
// rules — the default gmk runtime policy.

package logger

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config is the on-disk JSON shape.
type Config struct {
	Default string            `json:"default"`
	Format  string            `json:"format"`
	Loggers map[string]string `json:"loggers"`
	Outputs map[string]string `json:"outputs"`
}

// LoadConfigFile reads and parses a JSON config file at the given path.
// Returns an error if the file is unreadable or contains invalid JSON.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("logger config: read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("logger config: parse %s: %w", path, err)
	}
	return &c, nil
}

// ToOptions builds Options from this Config plus runtime-supplied
// terminal writer (typically os.Stderr) and a list of CLI overrides
// (parsed by ParseCLIOverrides).
//
// Precedence for level rules:
//
//	1. CLI overrides (highest)
//	2. Config file loggers
//	3. Config file default
//	4. Built-in default (info)
func (c *Config) ToOptions(terminal interface{ Write(p []byte) (int, error) }, cliOverrides []Override) (Options, error) {
	defLevel := LevelInfo
	if c != nil && c.Default != "" {
		l, err := ParseLevel(c.Default)
		if err != nil {
			return Options{}, fmt.Errorf("logger config: default level: %w", err)
		}
		defLevel = l
	}

	policy := NewPolicy(defLevel)

	if c != nil {
		// Loggers from the config file.
		// Sort keys so the install order is deterministic; the policy
		// itself doesn't care, but tests are easier when the order is
		// stable.
		keys := make([]string, 0, len(c.Loggers))
		for k := range c.Loggers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, name := range keys {
			lev, err := ParseLevel(c.Loggers[name])
			if err != nil {
				return Options{}, fmt.Errorf("logger config: loggers[%s]: %w", name, err)
			}
			policy.Set(name, lev)
		}
	}

	// Apply CLI overrides (these win over config file).
	for _, ov := range cliOverrides {
		policy.Set(ov.Pattern, ov.Level)
	}

	format := "human"
	if c != nil && c.Format != "" {
		format = c.Format
	}

	opts := Options{
		Policy:         policy,
		TerminalFormat: format,
	}
	if terminal != nil {
		opts.TerminalWriter = terminal
	}

	if c != nil && len(c.Outputs) > 0 {
		for pattern, path := range c.Outputs {
			opts.FileRoutes = append(opts.FileRoutes, FileRoute{
				Path:    path,
				Matcher: NewNameMatcher(pattern),
			})
		}
	}

	return opts, nil
}

// Override represents one CLI-supplied policy rule:
//
//	--log gmk.target.release=debug
//	--log gmk.expr.*=trace
//
// Pattern and Level are filled by ParseCLIOverrides.
type Override struct {
	Pattern string
	Level   Level
}

// ParseCLIOverrides parses a list of "pattern=level" strings into a
// slice of Overrides. An empty slice is returned unchanged.
//
// Errors are returned for malformed entries (missing "=", unknown level).
func ParseCLIOverrides(raws []string) ([]Override, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]Override, 0, len(raws))
	for _, raw := range raws {
		eq := strings.Index(raw, "=")
		if eq <= 0 || eq == len(raw)-1 {
			return nil, errors.New("logger override must be of form pattern=level: " + raw)
		}
		pattern := raw[:eq]
		levStr := raw[eq+1:]
		lev, err := ParseLevel(levStr)
		if err != nil {
			return nil, fmt.Errorf("logger override %q: %w", raw, err)
		}
		out = append(out, Override{Pattern: pattern, Level: lev})
	}
	return out, nil
}

// ParseEnv parses the GMK_LOG env var into overrides.
// Format: "pattern1:level1,pattern2:level2"
// (Using colon as separator since "=" is reserved for shell var syntax.)
// Returns nil for empty input.
func ParseEnv(env string) ([]Override, error) {
	env = strings.TrimSpace(env)
	if env == "" {
		return nil, nil
	}
	parts := strings.Split(env, ",")
	var out []Override
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		colon := strings.LastIndex(p, ":")
		if colon <= 0 {
			return nil, errors.New("GMK_LOG entry must be pattern:level: " + p)
		}
		lev, err := ParseLevel(p[colon+1:])
		if err != nil {
			return nil, fmt.Errorf("GMK_LOG %q: %w", p, err)
		}
		out = append(out, Override{Pattern: p[:colon], Level: lev})
	}
	return out, nil
}
