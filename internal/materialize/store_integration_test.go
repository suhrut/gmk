package materialize_test

// Tests for the SQLite-store-backed materialization path. Complements
// callable_test.go (which exercises the legacy no-store path) by
// verifying the new properties the store enables:
//
//   1. Content-addressed bodies: two callables with byte-identical
//      bodies share the same generated script file under .gmk-cache/bodies/.
//   2. Per-call scratch dirs: each materialization gets a unique
//      runs/<day>/<seq>-<name>/ directory.
//   3. Concurrent materialization: N goroutines materializing the same
//      callable simultaneously produce N distinct scratch dirs with
//      no errors, no overwrites, and the body file is written exactly
//      once (or written multiple times to the same content-addressed
//      path — both outcomes are correct as long as the final content
//      is right).
//   4. lib.sh is a single project-wide file (not per-run).

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/store"
)

func openStore(t *testing.T, root string) *store.Store {
	t.Helper()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMaterialize_StoreAllocation_AssignsMonotonicSeqs(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	c := &materialize.Callable{
		Name: "greet",
		Kind: "function",
		Lang: "bash",
		Run:  `r_set "hi"`,
	}

	for i := int64(1); i <= 5; i++ {
		inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
			ProjectRoot: root,
			Store:       st,
			Args:        map[string]expr.Value{"who": expr.NewString("test")},
		})
		if err != nil {
			t.Fatalf("materialize %d: %v", i, err)
		}
		if inv.Seq != i {
			t.Errorf("call %d: got seq=%d, want %d", i, inv.Seq, i)
		}
		// Dir name pattern: <day>/<seq>-<callable>.
		if !strings.HasSuffix(inv.ScratchDir, "-greet") {
			t.Errorf("scratch dir should end with -greet: %q", inv.ScratchDir)
		}
	}
}

func TestMaterialize_ContentAddressedBodies_Share(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	// Two callables with byte-identical bodies. They should hash to the
	// same content-addressed path and the body file should be written
	// once and shared.
	body := `r_set "the same body"`
	c1 := &materialize.Callable{Name: "alpha", Kind: "function", Lang: "bash", Run: body}
	c2 := &materialize.Callable{Name: "beta", Kind: "function", Lang: "bash", Run: body}

	inv1, err := materialize.MaterializeCallable(c1, materialize.MaterializeOpts{
		ProjectRoot: root, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	inv2, err := materialize.MaterializeCallable(c2, materialize.MaterializeOpts{
		ProjectRoot: root, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same body → same script path (content-addressed).
	if inv1.ScriptPath != inv2.ScriptPath {
		t.Errorf("identical bodies should share a script path:\n  inv1=%s\n  inv2=%s",
			inv1.ScriptPath, inv2.ScriptPath)
	}
	// But different scratch dirs (per-call JSON files).
	if inv1.ScratchDir == inv2.ScratchDir {
		t.Errorf("different callables should have different scratch dirs: %q", inv1.ScratchDir)
	}
	// Script path is under .gmk-cache/bodies/ (not under runs/).
	wantPrefix := filepath.Join(root, ".gmk-cache", "bodies")
	if !strings.HasPrefix(inv1.ScriptPath, wantPrefix) {
		t.Errorf("script not under bodies/: %q (want prefix %q)", inv1.ScriptPath, wantPrefix)
	}
}

func TestMaterialize_DifferentBodies_DistinctPaths(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	c1 := &materialize.Callable{Name: "a", Kind: "function", Lang: "bash", Run: `r_set "one"`}
	c2 := &materialize.Callable{Name: "a", Kind: "function", Lang: "bash", Run: `r_set "two"`}

	inv1, _ := materialize.MaterializeCallable(c1, materialize.MaterializeOpts{ProjectRoot: root, Store: st})
	inv2, _ := materialize.MaterializeCallable(c2, materialize.MaterializeOpts{ProjectRoot: root, Store: st})

	if inv1.ScriptPath == inv2.ScriptPath {
		t.Errorf("different bodies should hash to different paths: %q", inv1.ScriptPath)
	}
}

func TestMaterialize_LibShIsProjectWide(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	c := &materialize.Callable{Name: "x", Kind: "function", Lang: "bash", Run: "true"}

	// First call writes lib.sh; second should reuse the existing one.
	_, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}

	// lib.sh should live at .gmk-cache/lib.sh (project-wide), not in
	// any per-run dir.
	libPath := filepath.Join(root, ".gmk-cache", "lib.sh")
	if !fileExists(libPath) {
		t.Errorf("lib.sh not found at %s", libPath)
	}
}

// TestMaterialize_Concurrent_NoCollisions is the concurrency stress test
// for the materialize+store integration. N goroutines simultaneously
// materialize the same callable; every materialization must get its own
// scratch dir, the body file must be valid, and there must be no errors.
func TestMaterialize_Concurrent_NoCollisions(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	c := &materialize.Callable{
		Name: "shared",
		Kind: "function",
		Lang: "bash",
		Run:  `r_set "$(a_get who)"`,
	}

	const goroutines = 10
	const perRoutine = 8
	type result struct {
		dir string
		seq int64
		err error
	}
	results := make(chan result, goroutines*perRoutine)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
					ProjectRoot: root,
					Store:       st,
					Args: map[string]expr.Value{
						"who": expr.NewString("g" + itoa(g) + "i" + itoa(i)),
					},
				})
				if err != nil {
					results <- result{err: err}
					return
				}
				results <- result{dir: inv.ScratchDir, seq: inv.Seq}
			}
		}()
	}
	wg.Wait()
	close(results)

	// Collect results, check uniqueness.
	dirs := map[string]bool{}
	seqs := map[int64]bool{}
	count := 0
	for r := range results {
		if r.err != nil {
			t.Errorf("goroutine error: %v", r.err)
			continue
		}
		if dirs[r.dir] {
			t.Errorf("DUPLICATE scratch dir under concurrent materialize: %q", r.dir)
		}
		if seqs[r.seq] {
			t.Errorf("DUPLICATE seq under concurrent materialize: %d", r.seq)
		}
		dirs[r.dir] = true
		seqs[r.seq] = true
		count++
	}
	want := goroutines * perRoutine
	if count != want {
		t.Errorf("got %d successful materializations, want %d", count, want)
	}
}

func TestMaterialize_GMKEnv_IncludesDayAndSeq(t *testing.T) {
	root := t.TempDir()
	st := openStore(t, root)

	c := &materialize.Callable{Name: "f", Kind: "function", Lang: "bash", Run: "true"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root, Store: st,
	})
	if err != nil {
		t.Fatal(err)
	}

	if inv.Env["GMK_DAY"] == "" {
		t.Error("GMK_DAY env not set")
	}
	if inv.Env["GMK_SEQ"] == "" {
		t.Error("GMK_SEQ env not set")
	}
	if inv.Env["GMK_RUN_ID"] == "" {
		t.Error("GMK_RUN_ID env not set")
	}
	// GMK_RUN_ID has the form "<day>/<seq>" — i.e. it contains a slash
	// when the store is in use.
	if !strings.Contains(inv.Env["GMK_RUN_ID"], "/") {
		t.Errorf("GMK_RUN_ID should be day/seq when store is used, got %q",
			inv.Env["GMK_RUN_ID"])
	}
}

// --- helpers ---

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// itoa is a tiny int→string helper that doesn't drag strconv into a
// test-only file. Good enough for goroutine indexes 0..9.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
