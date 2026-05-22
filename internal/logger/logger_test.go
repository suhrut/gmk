package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/logger"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want logger.Level
		err  bool
	}{
		{"trace", logger.LevelTrace, false},
		{"TRACE", logger.LevelTrace, false},
		{"debug", logger.LevelDebug, false},
		{"info", logger.LevelInfo, false},
		{"warn", logger.LevelWarn, false},
		{"warning", logger.LevelWarn, false},
		{"error", logger.LevelError, false},
		{"err", logger.LevelError, false},
		{"fatal", logger.LevelFatal, false},
		{"verbose", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := logger.ParseLevel(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("ParseLevel(%q): err=%v want err=%v", tc.in, err, tc.err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseLevel(%q): got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestLevelString(t *testing.T) {
	if got := logger.LevelTrace.String(); got != "trace" {
		t.Errorf("LevelTrace.String() = %q, want trace", got)
	}
	if got := logger.LevelDebug.String(); got != "debug" {
		t.Errorf("LevelDebug.String() = %q, want debug", got)
	}
	if got := logger.LevelInfo.String(); got != "info" {
		t.Errorf("LevelInfo.String() = %q, want info", got)
	}
	if got := logger.LevelWarn.String(); got != "warn" {
		t.Errorf("LevelWarn.String() = %q, want warn", got)
	}
	if got := logger.LevelError.String(); got != "error" {
		t.Errorf("LevelError.String() = %q, want error", got)
	}
	if got := logger.LevelFatal.String(); got != "fatal" {
		t.Errorf("LevelFatal.String() = %q, want fatal", got)
	}
}

func TestPolicyExactMatch(t *testing.T) {
	p := logger.NewPolicy(logger.LevelWarn)
	p.Set("gmk.target.release", logger.LevelDebug)
	p.Set("gmk.expr.parser", logger.LevelTrace)

	if got := p.LevelFor("gmk.target.release"); got != logger.LevelDebug {
		t.Errorf("got %v, want debug", got)
	}
	if got := p.LevelFor("gmk.expr.parser"); got != logger.LevelTrace {
		t.Errorf("got %v, want trace", got)
	}
	if got := p.LevelFor("gmk.unknown"); got != logger.LevelWarn {
		t.Errorf("got %v, want warn (default)", got)
	}
}

func TestPolicyWildcardMatch(t *testing.T) {
	p := logger.NewPolicy(logger.LevelInfo)
	p.Set("gmk.target.*", logger.LevelDebug)
	p.Set("gmk.expr.*", logger.LevelTrace)

	if got := p.LevelFor("gmk.target.release"); got != logger.LevelDebug {
		t.Errorf("gmk.target.release: got %v, want debug", got)
	}
	if got := p.LevelFor("gmk.target.flaky-deploy"); got != logger.LevelDebug {
		t.Errorf("gmk.target.flaky-deploy: got %v, want debug", got)
	}
	if got := p.LevelFor("gmk.expr.eval"); got != logger.LevelTrace {
		t.Errorf("gmk.expr.eval: got %v, want trace", got)
	}
	if got := p.LevelFor("gmk.cache"); got != logger.LevelInfo {
		t.Errorf("gmk.cache: got %v, want info (default)", got)
	}
}

func TestPolicyExactBeatsWildcard(t *testing.T) {
	// Exact-name rules must take precedence over wildcards.
	p := logger.NewPolicy(logger.LevelInfo)
	p.Set("gmk.target.*", logger.LevelDebug)
	p.Set("gmk.target.release", logger.LevelTrace)

	if got := p.LevelFor("gmk.target.release"); got != logger.LevelTrace {
		t.Errorf("exact should win over wildcard: got %v, want trace", got)
	}
	if got := p.LevelFor("gmk.target.deploy"); got != logger.LevelDebug {
		t.Errorf("wildcard catches non-exact: got %v, want debug", got)
	}
}

func TestPolicyLongestPrefixWins(t *testing.T) {
	p := logger.NewPolicy(logger.LevelInfo)
	p.Set("gmk.*", logger.LevelWarn)
	p.Set("gmk.target.*", logger.LevelDebug)
	p.Set("gmk.target.deep.*", logger.LevelTrace)

	if got := p.LevelFor("gmk.target.deep.thing"); got != logger.LevelTrace {
		t.Errorf("got %v, want trace (longest prefix)", got)
	}
	if got := p.LevelFor("gmk.target.shallow"); got != logger.LevelDebug {
		t.Errorf("got %v, want debug (medium prefix)", got)
	}
	if got := p.LevelFor("gmk.cache"); got != logger.LevelWarn {
		t.Errorf("got %v, want warn (gmk.* prefix)", got)
	}
}

func TestPolicyEnabled(t *testing.T) {
	p := logger.NewPolicy(logger.LevelInfo)
	p.Set("gmk.target.release", logger.LevelDebug)

	if !p.Enabled("gmk.target.release", logger.LevelInfo) {
		t.Error("info should be enabled when policy is debug")
	}
	if !p.Enabled("gmk.target.release", logger.LevelDebug) {
		t.Error("debug should be enabled when policy is debug")
	}
	if p.Enabled("gmk.target.release", logger.LevelTrace) {
		t.Error("trace should be disabled when policy is debug")
	}
	if p.Enabled("gmk.target.release", logger.LevelError) {
		// error > debug, so error is enabled
	}
	if !p.Enabled("gmk.target.release", logger.LevelError) {
		t.Error("error should be enabled when policy is debug")
	}
}

func TestNameMatcher(t *testing.T) {
	m := logger.NewNameMatcher("gmk.target.*", "gmk.fn.specific")
	if !m.Match("gmk.target.anything") {
		t.Error("wildcard should match")
	}
	if !m.Match("gmk.fn.specific") {
		t.Error("exact should match")
	}
	if m.Match("gmk.fn.other") {
		t.Error("non-matching exact should fail")
	}
	if m.Match("gmk.cache") {
		t.Error("unrelated name should fail")
	}

	// Match-all
	mAll := logger.NewNameMatcher("*")
	if !mAll.Match("anything.here") {
		t.Error("match-all should match")
	}
}

func TestPolicyClone(t *testing.T) {
	p := logger.NewPolicy(logger.LevelWarn)
	p.Set("a.b", logger.LevelDebug)
	p.Set("c.*", logger.LevelTrace)

	clone := p.Clone()
	clone.Set("a.b", logger.LevelError)

	// Original must not have been affected.
	if got := p.LevelFor("a.b"); got != logger.LevelDebug {
		t.Errorf("original mutated: got %v, want debug", got)
	}
	if got := clone.LevelFor("a.b"); got != logger.LevelError {
		t.Errorf("clone not updated: got %v, want error", got)
	}
}

func TestPolicyRules(t *testing.T) {
	p := logger.NewPolicy(logger.LevelInfo)
	p.Set("gmk.target.release", logger.LevelDebug)
	p.Set("gmk.target.*", logger.LevelTrace)

	rules := p.Rules()
	if rules["gmk.target.release"] != logger.LevelDebug {
		t.Errorf("exact rule missing")
	}
	if rules["gmk.target.*"] != logger.LevelTrace {
		t.Errorf("wildcard rule missing or wrong form")
	}
}

func TestParseCLIOverrides(t *testing.T) {
	overs, err := logger.ParseCLIOverrides([]string{
		"gmk.target.release=debug",
		"gmk.expr.*=trace",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(overs) != 2 {
		t.Fatalf("got %d overrides, want 2", len(overs))
	}
	if overs[0].Pattern != "gmk.target.release" || overs[0].Level != logger.LevelDebug {
		t.Errorf("first override wrong: %+v", overs[0])
	}
	if overs[1].Pattern != "gmk.expr.*" || overs[1].Level != logger.LevelTrace {
		t.Errorf("second override wrong: %+v", overs[1])
	}
}

func TestParseCLIOverridesErrors(t *testing.T) {
	cases := []string{
		"no-equals",
		"trailing=",
		"=leading",
		"x=verbose", // unknown level
	}
	for _, c := range cases {
		_, err := logger.ParseCLIOverrides([]string{c})
		if err == nil {
			t.Errorf("expected error for %q, got nil", c)
		}
	}
}

func TestParseEnv(t *testing.T) {
	overs, err := logger.ParseEnv("gmk.target.release:debug,gmk.expr.*:trace")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(overs) != 2 {
		t.Fatalf("got %d overrides, want 2", len(overs))
	}
	if overs[0].Pattern != "gmk.target.release" || overs[0].Level != logger.LevelDebug {
		t.Errorf("first: %+v", overs[0])
	}
}

func TestParseEnvEmpty(t *testing.T) {
	overs, err := logger.ParseEnv("")
	if err != nil {
		t.Errorf("empty env: %v", err)
	}
	if len(overs) != 0 {
		t.Errorf("empty env should give no overrides, got %d", len(overs))
	}
}

func TestWithName(t *testing.T) {
	ctx := context.Background()
	if logger.NameFromContext(ctx) != "" {
		t.Error("empty ctx should have no name")
	}
	ctx = logger.WithName(ctx, "gmk.target.x")
	if got := logger.NameFromContext(ctx); got != "gmk.target.x" {
		t.Errorf("got %q, want gmk.target.x", got)
	}
	// Override
	ctx = logger.WithName(ctx, "gmk.target.y")
	if got := logger.NameFromContext(ctx); got != "gmk.target.y" {
		t.Errorf("got %q, want gmk.target.y", got)
	}
}

func TestConfigureAndEmit(t *testing.T) {
	defer logger.Reset()

	var buf bytes.Buffer
	policy := logger.NewPolicy(logger.LevelInfo)
	policy.Set("gmk.target.release", logger.LevelDebug)
	policy.Set("gmk.expr.*", logger.LevelWarn)

	logger.Configure(logger.Options{
		Policy:         policy,
		TerminalWriter: &buf,
		TerminalFormat: "human",
	})

	// Below policy — should be suppressed
	logger.Get("gmk.expr.parser").Info("noise")
	if strings.Contains(buf.String(), "noise") {
		t.Errorf("info from gmk.expr.parser should be suppressed; got %q", buf.String())
	}

	// At or above policy — should be emitted
	logger.Get("gmk.target.release").Debug("starting build")
	if !strings.Contains(buf.String(), "starting build") {
		t.Errorf("debug from gmk.target.release should be emitted; got %q", buf.String())
	}

	// Default-level for an unknown name
	logger.Get("gmk.unknown.thing").Info("default-info")
	if !strings.Contains(buf.String(), "default-info") {
		t.Errorf("info at default level should be emitted; got %q", buf.String())
	}
}

func TestConfigureJSONFormat(t *testing.T) {
	defer logger.Reset()

	var buf bytes.Buffer
	logger.Configure(logger.Options{
		Policy:         logger.NewPolicy(logger.LevelInfo),
		TerminalWriter: &buf,
		TerminalFormat: "json",
	})

	logger.Get("gmk.target.x").Info("hello", "k", "v")

	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("JSON line should end in newline, got %q", out)
	}

	// Each line should parse as JSON.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("line %q is not JSON: %v", line, err)
		}
		if rec["msg"] != "hello" {
			t.Errorf("msg=%v", rec["msg"])
		}
		if rec["name"] != "gmk.target.x" {
			t.Errorf("name=%v", rec["name"])
		}
		if rec["k"] != "v" {
			t.Errorf("k=%v", rec["k"])
		}
	}
}

func TestConfigureFileRoute(t *testing.T) {
	defer logger.Reset()

	dir := t.TempDir()
	path := filepath.Join(dir, "out.jsonl")

	logger.Configure(logger.Options{
		Policy: logger.NewPolicy(logger.LevelWarn), // terminal would suppress info
		FileRoutes: []logger.FileRoute{{
			Path:    path,
			Matcher: logger.NewNameMatcher("gmk.target.*"),
		}},
	})

	// Terminal policy is warn, but file route ignores policy.
	// File captures because name matches.
	logger.Get("gmk.target.release").Info("captured")
	// File should NOT capture: name doesn't match.
	logger.Get("gmk.cache").Info("not-captured")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "captured") {
		t.Errorf("file should contain captured msg: %q", out)
	}
	if strings.Contains(out, "not-captured") {
		t.Errorf("file should not contain not-captured msg: %q", out)
	}
}

func TestLoadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.json")
	contents := `{
		"default": "warn",
		"format": "json",
		"loggers": {
			"gmk.target.*": "info",
			"gmk.target.release": "debug"
		},
		"outputs": {
			"gmk.plugin.*": "plugin.log"
		}
	}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := logger.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Default != "warn" {
		t.Errorf("default=%q", c.Default)
	}
	if c.Format != "json" {
		t.Errorf("format=%q", c.Format)
	}
	if len(c.Loggers) != 2 {
		t.Errorf("loggers len=%d", len(c.Loggers))
	}
	if len(c.Outputs) != 1 {
		t.Errorf("outputs len=%d", len(c.Outputs))
	}
}

func TestConfigToOptions(t *testing.T) {
	c := &logger.Config{
		Default: "warn",
		Format:  "human",
		Loggers: map[string]string{
			"gmk.target.*":       "info",
			"gmk.target.release": "debug",
		},
	}
	opts, err := c.ToOptions(nil, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}
	if opts.Policy.LevelFor("gmk.target.release") != logger.LevelDebug {
		t.Error("specific rule not applied")
	}
	if opts.Policy.LevelFor("gmk.target.deploy") != logger.LevelInfo {
		t.Error("wildcard rule not applied")
	}
	if opts.Policy.LevelFor("gmk.cache") != logger.LevelWarn {
		t.Error("default not applied")
	}
}

func TestConfigToOptionsCLIWins(t *testing.T) {
	c := &logger.Config{
		Default: "warn",
		Loggers: map[string]string{
			"gmk.target.release": "debug",
		},
	}
	cli, _ := logger.ParseCLIOverrides([]string{"gmk.target.release=trace"})
	opts, err := c.ToOptions(nil, cli)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}
	if got := opts.Policy.LevelFor("gmk.target.release"); got != logger.LevelTrace {
		t.Errorf("CLI override should win: got %v, want trace", got)
	}
}
