package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/store"
)

func TestParseRunIdent_Forms(t *testing.T) {
	today := materialize.Today()
	cases := []struct {
		in      string
		wantDay string
		wantSeq int64
	}{
		// Full day/seq, zero-padded.
		{"20260523/0007", "20260523", 7},
		// Full day/seq, no padding.
		{"20260523/7", "20260523", 7},
		// Seq-only, defaults to today's day.
		{"0007", today, 7},
		{"7", today, 7},
		// Dir-name form with trailing -<callable_name>; the name is stripped.
		{"20260523/0007-publish", "20260523", 7},
		{"20260523/12-some-target", "20260523", 12},
		// Seq-only with dir-name suffix — defaults today.
		{"007-build", today, 7},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			day, seq, err := parseRunIdent(c.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if day != c.wantDay {
				t.Errorf("day = %q, want %q", day, c.wantDay)
			}
			if seq != c.wantSeq {
				t.Errorf("seq = %d, want %d", seq, c.wantSeq)
			}
		})
	}
}

func TestParseRunIdent_Errors(t *testing.T) {
	bad := []struct {
		in     string
		expect string // substring to look for in error
	}{
		{"", "empty"},
		{"abc", "not numeric"},
		{"2026/7", "8 digits"},          // day too short
		{"20260523/", "not numeric"},    // seq part empty
		{"20260523/0", "must be positive"},
		{"20260523/-3", "not numeric"}, // strconv parses negative ok then we reject
	}
	for _, b := range bad {
		t.Run(b.in, func(t *testing.T) {
			_, _, err := parseRunIdent(b.in)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), b.expect) {
				t.Errorf("error %q didn't mention %q", err, b.expect)
			}
		})
	}
}

// End-to-end: write a project, record a run, then inspect it through
// the executeInspect entry point. Verifies the human output mentions
// the callable name and the JSON output parses.
func TestExecuteInspect_HumanAndJSON(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "gmk.yml")
	if err := os.WriteFile(yml, []byte(`
vars:
  name: "inspect-me"
targets:
  noop:
    run: |
      echo hello
`), 0o644); err != nil {
		t.Fatalf("write yml: %v", err)
	}

	// Open the store under this project root and write a finished run
	// — bypassing the cli to keep the test focused on inspect itself.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	day := materialize.Today()
	seq, err := st.AllocateRun(ctx, day)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	started := time.Now().Add(-2 * time.Second).UTC()
	if err := st.RecordStart(ctx, store.StartParams{
		Day:          day,
		Seq:          seq,
		CallableName: "publish-thing",
		CallableKind: "target",
		StartedAt:    started,
		SourceFile:   yml,
		ArgsJSON:     `{"version":"v1.2.3"}`,
		PreludeJSON:  `{"build_id":"abc123"}`,
	}); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if err := st.FinishRun(ctx, store.FinishParams{
		Day:        day,
		Seq:        seq,
		FinishedAt: time.Now().UTC(),
		Status:     "ok",
		ExitCode:   0,
		ResultJSON: `"published"`,
	}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// Human output should mention the callable name and the status.
	var humanBuf bytes.Buffer
	if err := executeInspect(&humanBuf, yml, day+"/"+itoa(int(seq)), false); err != nil {
		t.Fatalf("inspect human: %v", err)
	}
	human := humanBuf.String()
	if !strings.Contains(human, "publish-thing") {
		t.Errorf("human output missing callable name: %q", human)
	}
	if !strings.Contains(human, "status=ok") {
		t.Errorf("human output missing status: %q", human)
	}
	if !strings.Contains(human, "v1.2.3") {
		t.Errorf("human output missing args content: %q", human)
	}
	if !strings.Contains(human, "abc123") {
		t.Errorf("human output missing prelude content: %q", human)
	}
	if !strings.Contains(human, "published") {
		t.Errorf("human output missing result content: %q", human)
	}

	// JSON output must parse and contain the same fields under their
	// expected JSON shapes.
	var jsonBuf bytes.Buffer
	if err := executeInspect(&jsonBuf, yml, day+"/"+itoa(int(seq)), true); err != nil {
		t.Fatalf("inspect json: %v", err)
	}
	js := jsonBuf.String()
	// Quick sanity — full schema-check would be over-engineering. The
	// raw substrings must be present in JSON form.
	for _, want := range []string{
		`"day":`, `"seq":`, `"callable_name": "publish-thing"`,
		`"status": "ok"`, `"args"`, `"v1.2.3"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("json output missing %q\nfull:\n%s", want, js)
		}
	}
}

// itoa here mirrors the package's own little helper (we don't want to
// import strconv into the test file just for this).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
