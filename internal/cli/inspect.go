package cli

// `gmk inspect <day>/<seq>` — show one run's metadata from the runs
// table. Pairs with `gmk run` and `gmk call` (which produce the rows)
// for the common debugging workflow:
//
//   $ gmk run release
//   ... error in target "publish" ...
//   $ gmk inspect 20260523/0007
//   target "publish": exit_code=1, started 12:03:14, finished 12:03:17
//   args:    {...}
//   prelude: {...}
//   result:  null
//   body:    /home/u/proj/.gmk-cache/bodies/<hash>/body.sh
//
// The user copy-pastes the body path to look at the materialized
// script. Future stages may add `gmk inspect --tail-stdout` to also
// dump the captured stdout/stderr.
//
// Input forms accepted:
//   - "20260523/0007"        — explicit day/seq
//   - "0007" or "7"          — seq only, defaults to today's day
//   - "20260523/0007-target" — the actual dir name from .gmk-cache/runs
//                              (the trailing -<name> is stripped)
//
// Output: human-readable by default; --json for scripting.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/store"
)

func newInspectCmd() *cobra.Command {
	var (
		file       string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <day>/<seq>",
		Short: "Show details of one recorded run",
		Long: `Show metadata for one row from the runs table.

Identifier forms:
  20260523/0007        explicit day and seq
  0007  or  7          seq only; day defaults to today
  20260523/0007-name   directory name from .gmk-cache/runs (trailing -<name> stripped)

Useful right after a failed run to inspect args, prelude, result, exit
code, and the materialized body script path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeInspect(cmd.OutOrStdout(), file, args[0], jsonOutput)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "explicit gmk.yml path; if unset the project is discovered")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit the run row as JSON for scripting")

	return cmd
}

// executeInspect is the shared core of gmk inspect, taking the
// dependencies as args so tests can drive it without cobra.
func executeInspect(out io.Writer, file, ident string, jsonOutput bool) error {
	project, err := loadProject(file)
	if err != nil {
		return err
	}

	day, seq, err := parseRunIdent(ident)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	st, err := store.Open(project.Root)
	if err != nil {
		return fmt.Errorf("inspect: open store: %w", err)
	}
	defer st.Close()

	run, err := st.GetRun(context.Background(), day, seq)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("inspect: no run found for %s/%04d (check `gmk inspect --list-day %s`)",
				day, seq, day)
		}
		return fmt.Errorf("inspect: query: %w", err)
	}

	if jsonOutput {
		return writeRunJSON(out, run, project.Root)
	}
	return writeRunHuman(out, run, project.Root)
}

// parseRunIdent accepts:
//
//	"20260523/0007"        → day="20260523", seq=7
//	"20260523/0007-name"   → day="20260523", seq=7   (trailing -<name> stripped)
//	"7"  / "0007"          → day=today,     seq=7
//
// The day-segment must be 8 digits if present; the seq-segment must
// start with digits and stops at the first '-' or end-of-string.
func parseRunIdent(s string) (string, int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, fmt.Errorf("identifier is empty")
	}

	var dayPart, seqPart string
	if slash := strings.Index(s, "/"); slash >= 0 {
		dayPart = s[:slash]
		seqPart = s[slash+1:]
	} else {
		dayPart = materialize.Today()
		seqPart = s
	}

	// Strip the trailing -<name> if present (handles the dir-name form).
	if dash := strings.Index(seqPart, "-"); dash >= 0 {
		seqPart = seqPart[:dash]
	}

	if len(dayPart) != 8 {
		return "", 0, fmt.Errorf("day part %q is not 8 digits (expected YYYYMMDD)", dayPart)
	}
	if _, err := strconv.Atoi(dayPart); err != nil {
		return "", 0, fmt.Errorf("day part %q is not numeric", dayPart)
	}

	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("seq part %q is not numeric: %w", seqPart, err)
	}
	if seq < 1 {
		return "", 0, fmt.Errorf("seq %d must be positive", seq)
	}
	return dayPart, seq, nil
}

// writeRunHuman emits the run as a labelled multi-line block.
// Times are rendered as the local-time HH:MM:SS for readability
// (the full ISO timestamp is in --json output).
func writeRunHuman(out io.Writer, r *store.Run, projectRoot string) error {
	fmt.Fprintf(out, "%s %s/%04d  status=%s",
		titleCase(r.CallableKind), r.Day, r.Seq, r.Status)
	if r.ExitCode != nil {
		fmt.Fprintf(out, " exit=%d", *r.ExitCode)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  name:        %s\n", r.CallableName)
	fmt.Fprintf(out, "  started:     %s\n", r.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if r.FinishedAt != nil {
		fmt.Fprintf(out, "  finished:    %s\n", r.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	}
	if r.DurationMS != nil {
		fmt.Fprintf(out, "  duration:    %s\n", time.Duration(*r.DurationMS)*time.Millisecond)
	}
	if r.SourceFile != "" {
		fmt.Fprintf(out, "  source:      %s\n", r.SourceFile)
	}
	if r.GMKPid != 0 {
		fmt.Fprintf(out, "  gmk pid:     %d\n", r.GMKPid)
	}

	writeJSONField(out, "args", r.ArgsJSON)
	writeJSONField(out, "prelude", r.PreludeJSON)
	writeJSONField(out, "result", r.ResultJSON)

	// Pointer to the materialized body script. We don't know the body
	// hash without parsing the cache layout, but the runs dir is
	// deterministic enough: .gmk-cache/runs/<day>/<seq>-<name>/ holds
	// args/prelude/result JSON files. Show that path.
	runsDir := filepath.Join(projectRoot, ".gmk-cache", "runs", r.Day,
		fmt.Sprintf("%04d-%s", r.Seq, r.CallableName))
	fmt.Fprintf(out, "  scratch:     %s\n", runsDir)

	return nil
}

// writeJSONField emits a "label: <one-line>" if the JSON is small,
// or "label:\n    <pretty-printed>" if it's a non-empty object or array.
// Empty / null payloads are omitted to keep the output compact.
func writeJSONField(out io.Writer, label, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "{}" || raw == "[]" {
		return
	}
	// Try to pretty-print. If it doesn't parse, fall back to verbatim.
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		pretty, _ := json.MarshalIndent(v, "    ", "  ")
		fmt.Fprintf(out, "  %-12s\n    %s\n", label+":", string(pretty))
		return
	}
	fmt.Fprintf(out, "  %-12s %s\n", label+":", raw)
}

// writeRunJSON emits the run as a single JSON object — convenient
// for piping into jq.
func writeRunJSON(out io.Writer, r *store.Run, projectRoot string) error {
	type runJSON struct {
		Day          string  `json:"day"`
		Seq          int64   `json:"seq"`
		CallableName string  `json:"callable_name"`
		CallableKind string  `json:"callable_kind"`
		StartedAt    string  `json:"started_at"`
		FinishedAt   *string `json:"finished_at,omitempty"`
		DurationMS   *int64  `json:"duration_ms,omitempty"`
		Status       string  `json:"status"`
		ExitCode     *int    `json:"exit_code,omitempty"`
		Args         any     `json:"args,omitempty"`
		Prelude      any     `json:"prelude,omitempty"`
		Result       any     `json:"result,omitempty"`
		SourceFile   string  `json:"source_file,omitempty"`
		GMKPid       int     `json:"gmk_pid,omitempty"`
		ScratchDir   string  `json:"scratch_dir"`
	}

	rj := runJSON{
		Day:          r.Day,
		Seq:          r.Seq,
		CallableName: r.CallableName,
		CallableKind: r.CallableKind,
		StartedAt:    r.StartedAt.UTC().Format(time.RFC3339Nano),
		Status:       r.Status,
		SourceFile:   r.SourceFile,
		GMKPid:       r.GMKPid,
		ScratchDir: filepath.Join(projectRoot, ".gmk-cache", "runs", r.Day,
			fmt.Sprintf("%04d-%s", r.Seq, r.CallableName)),
	}
	if r.FinishedAt != nil {
		s := r.FinishedAt.UTC().Format(time.RFC3339Nano)
		rj.FinishedAt = &s
	}
	if r.DurationMS != nil {
		rj.DurationMS = r.DurationMS
	}
	if r.ExitCode != nil {
		rj.ExitCode = r.ExitCode
	}
	parseInto(r.ArgsJSON, &rj.Args)
	parseInto(r.PreludeJSON, &rj.Prelude)
	parseInto(r.ResultJSON, &rj.Result)

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(rj)
}

// parseInto fills *dst with the JSON-decoded value of raw, leaving it
// nil if raw is empty or malformed. Used to nest the JSON columns
// inside the inspect output as native JSON values rather than escaped
// strings.
func parseInto(raw string, dst *any) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	_ = json.Unmarshal([]byte(raw), dst)
}

// titleCase upper-cases the first letter — "function" → "Function",
// "target" → "Target". The Go strings package's Title is deprecated;
// we do this trivially since we only care about ASCII letters.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}
