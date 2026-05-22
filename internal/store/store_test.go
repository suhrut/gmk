package store_test

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/suhrut/gmk/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_Open_CreatesDBAndSchema(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// DB file exists at the documented path.
	wantPath := filepath.Join(root, ".gmk-cache", "gmk.db")
	if s.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", s.Path(), wantPath)
	}

	// Schema is in place: AllocateRun must succeed.
	ctx := context.Background()
	seq, err := s.AllocateRun(ctx, "20260522")
	if err != nil {
		t.Fatalf("AllocateRun on fresh DB: %v", err)
	}
	if seq != 1 {
		t.Errorf("first allocation: got seq=%d, want 1", seq)
	}
}

func TestStore_Open_Idempotent(t *testing.T) {
	root := t.TempDir()
	s1, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Second open should succeed without re-running migrations.
	s2, err := store.Open(root)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	// State should persist.
	ctx := context.Background()
	_, _ = s2.AllocateRun(ctx, "20260522")
	seq2, _ := s2.AllocateRun(ctx, "20260522")
	if seq2 != 2 {
		t.Errorf("after reopen + allocations: got %d, want 2", seq2)
	}
}

func TestStore_AllocateRun_Monotonic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := "20260522"

	for i := int64(1); i <= 5; i++ {
		seq, err := s.AllocateRun(ctx, day)
		if err != nil {
			t.Fatal(err)
		}
		if seq != i {
			t.Errorf("allocation %d: got %d, want %d", i, seq, i)
		}
	}
}

func TestStore_AllocateRun_PerDayIndependent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// First day: counter starts at 1.
	seq, _ := s.AllocateRun(ctx, "20260522")
	if seq != 1 {
		t.Errorf("day 1 first allocation: %d", seq)
	}

	// Different day: counter independent, starts at 1 again.
	seq, _ = s.AllocateRun(ctx, "20260523")
	if seq != 1 {
		t.Errorf("day 2 first allocation: %d (should not inherit from day 1)", seq)
	}

	// Back to day 1: counter resumes.
	seq, _ = s.AllocateRun(ctx, "20260522")
	if seq != 2 {
		t.Errorf("day 1 second allocation: %d", seq)
	}
}

func TestStore_AllocateRun_InvalidDay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cases := []string{"", "2026-05-22", "20260", "2026052X", "abcdefgh"}
	for _, d := range cases {
		_, err := s.AllocateRun(ctx, d)
		if err == nil {
			t.Errorf("AllocateRun(%q): expected error", d)
		}
	}
}

// TestStore_AllocateRun_ConcurrentNoDup is the critical concurrency
// test. We spawn N goroutines, each allocating M seqs against the same
// day, and assert:
//   1. Every allocation succeeds.
//   2. Every returned seq is unique.
//   3. The set of returned seqs is exactly [1, N*M].
func TestStore_AllocateRun_ConcurrentNoDup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := "20260522"

	const (
		goroutines = 16
		perRoutine = 25
	)
	totalAllocs := goroutines * perRoutine

	allocs := make([]int64, totalAllocs)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				seq, err := s.AllocateRun(ctx, day)
				if err != nil {
					t.Errorf("goroutine %d alloc %d: %v", g, i, err)
					return
				}
				allocs[g*perRoutine+i] = seq
			}
		}()
	}
	wg.Wait()

	// Every alloc must be unique.
	seen := make(map[int64]bool, totalAllocs)
	for _, s := range allocs {
		if seen[s] {
			t.Errorf("duplicate seq: %d", s)
		}
		seen[s] = true
	}
	if len(seen) != totalAllocs {
		t.Errorf("got %d unique seqs, want %d", len(seen), totalAllocs)
	}

	// And the union should be exactly [1, totalAllocs].
	sort.Slice(allocs, func(i, j int) bool { return allocs[i] < allocs[j] })
	for i, s := range allocs {
		if int(s) != i+1 {
			t.Errorf("after sort: allocs[%d]=%d, want %d", i, s, i+1)
			break
		}
	}
}

func TestStore_RecordStartAndFinish(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := store.Today()

	seq, err := s.AllocateRun(ctx, day)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	if err := s.RecordStart(ctx, store.StartParams{
		Day:          day,
		Seq:          seq,
		CallableName: "greet",
		CallableKind: "function",
		StartedAt:    start,
		ArgsJSON:     `{"who":"Sam"}`,
		PreludeJSON:  `{"version":"1.0"}`,
		SourceFile:   "/proj/build.yml",
	}); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	// Mid-flight: row exists in running status.
	r, err := s.GetRun(ctx, day, seq)
	if err != nil {
		t.Fatalf("GetRun (running): %v", err)
	}
	if r.Status != "running" {
		t.Errorf("status=%q, want running", r.Status)
	}
	if r.CallableName != "greet" {
		t.Errorf("callable_name=%q", r.CallableName)
	}
	if r.ArgsJSON != `{"who":"Sam"}` {
		t.Errorf("args=%q", r.ArgsJSON)
	}
	if r.FinishedAt != nil {
		t.Errorf("finished_at should be nil while running")
	}

	// Finish.
	finish := start.Add(150 * time.Millisecond)
	if err := s.FinishRun(ctx, store.FinishParams{
		Day:        day,
		Seq:        seq,
		FinishedAt: finish,
		Status:     "ok",
		ExitCode:   0,
		ResultJSON: `"hello, Sam"`,
	}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	r, err = s.GetRun(ctx, day, seq)
	if err != nil {
		t.Fatalf("GetRun (finished): %v", err)
	}
	if r.Status != "ok" {
		t.Errorf("status=%q, want ok", r.Status)
	}
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("exit_code: %v", r.ExitCode)
	}
	if r.DurationMS == nil || *r.DurationMS != 150 {
		t.Errorf("duration_ms: %v (want ~150)", r.DurationMS)
	}
	if r.ResultJSON != `"hello, Sam"` {
		t.Errorf("result=%q", r.ResultJSON)
	}
}

func TestStore_FinishRun_InvalidStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := store.Today()
	seq, _ := s.AllocateRun(ctx, day)
	s.RecordStart(ctx, store.StartParams{
		Day: day, Seq: seq,
		CallableName: "f", CallableKind: "function",
		StartedAt: time.Now(),
	})

	err := s.FinishRun(ctx, store.FinishParams{
		Day: day, Seq: seq, Status: "weird", FinishedAt: time.Now(),
	})
	if err == nil {
		t.Error("expected invalid-status error")
	}
}

func TestStore_FinishRun_NoSuchRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.FinishRun(ctx, store.FinishParams{
		Day: "20260522", Seq: 999,
		Status: "ok", FinishedAt: time.Now(),
	})
	if err == nil {
		t.Error("expected error finishing nonexistent run")
	}
}

func TestStore_ListRunsForDay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := store.Today()

	// Insert three runs, finish two.
	for _, fn := range []string{"a", "b", "c"} {
		seq, _ := s.AllocateRun(ctx, day)
		s.RecordStart(ctx, store.StartParams{
			Day: day, Seq: seq,
			CallableName: fn, CallableKind: "function",
			StartedAt: time.Now(),
		})
		if fn != "c" {
			s.FinishRun(ctx, store.FinishParams{
				Day: day, Seq: seq,
				FinishedAt: time.Now().Add(10 * time.Millisecond),
				Status:     "ok",
			})
		}
	}

	runs, err := s.ListRunsForDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(runs))
	}
	// Ordering: by seq ascending.
	if runs[0].CallableName != "a" || runs[1].CallableName != "b" || runs[2].CallableName != "c" {
		t.Errorf("order wrong: %s,%s,%s",
			runs[0].CallableName, runs[1].CallableName, runs[2].CallableName)
	}
	if runs[2].Status != "running" {
		t.Errorf("c should still be running: %s", runs[2].Status)
	}
}

func TestStore_ListRunning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	day := store.Today()

	// Three runs, finish one, leave two running.
	for _, fn := range []string{"a", "b", "c"} {
		seq, _ := s.AllocateRun(ctx, day)
		s.RecordStart(ctx, store.StartParams{
			Day: day, Seq: seq,
			CallableName: fn, CallableKind: "function",
			StartedAt: time.Now(),
		})
		if fn == "a" {
			s.FinishRun(ctx, store.FinishParams{
				Day: day, Seq: seq, Status: "ok", FinishedAt: time.Now(),
			})
		}
	}

	running, err := s.ListRunning(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 {
		t.Fatalf("got %d running, want 2", len(running))
	}
	for _, r := range running {
		if r.Status != "running" {
			t.Errorf("got status %q in running list", r.Status)
		}
	}
}

func TestStore_FormatDay(t *testing.T) {
	tm := time.Date(2026, 5, 22, 14, 30, 15, 0, time.UTC)
	got := store.FormatDay(tm)
	if got != "20260522" {
		t.Errorf("got %q, want 20260522", got)
	}
}

func TestStore_Today_FormatIsValid(t *testing.T) {
	d := store.Today()
	if len(d) != 8 {
		t.Errorf("Today() = %q (len %d), want 8 chars", d, len(d))
	}
	// Smoke-check parse.
	if _, err := time.Parse("20060102", d); err != nil {
		t.Errorf("Today() = %q is not YYYYMMDD: %v", d, err)
	}
}

func TestStore_Migration_PicksUpFromExistingDB(t *testing.T) {
	root := t.TempDir()
	// First open creates schema at v1.
	s1, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Second open: should not error, should not re-run migrations.
	s2, err := store.Open(root)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()

	ctx := context.Background()
	seq, err := s2.AllocateRun(ctx, "20260522")
	if err != nil {
		t.Fatalf("AllocateRun after reopen: %v", err)
	}
	if seq != 1 {
		t.Errorf("seq after reopen = %d, want 1", seq)
	}
}
