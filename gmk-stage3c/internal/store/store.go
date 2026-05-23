// Package store is the SQLite-backed persistence layer for gmk.
//
// It owns one .gmk-cache/gmk.db file per project and exposes a small,
// stable API:
//
//   AllocateRun(day, callable) -> seq           atomic seq allocation
//   RecordStart(day, seq, ...details)           inserts the runs row
//   FinishRun(day, seq, status, ...)            updates on completion
//   GetRun, ListRunsForDay                      diagnostic readers
//
// The DB is opened in WAL mode at the first call to Open. Schema
// migrations live in migrations/ as numbered .sql files; they're
// embedded via go:embed and applied in order at open time, with the
// current version tracked in SQLite's PRAGMA user_version. Adding a
// new migration is a single new file plus bumping the latestVersion
// constant.
//
// The package is safe for concurrent use: AllocateRun is implemented
// as a single INSERT...ON CONFLICT...RETURNING statement, which SQLite
// serializes via its writer lock. Concurrent gmk processes get
// unique seqs without any application-level coordination.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/suhrut/gmk/internal/logger"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// latestVersion is the user_version corresponding to the latest
// migration file in migrations/. Bumped when a new migration lands.
//
// Each migration file must be named NNNN_description.sql where NNNN
// is the version it advances to. So 0001_initial.sql brings a fresh
// DB to user_version=1, 0002_add_cache.sql brings 1->2, etc.
const latestVersion = 1

// Store is a SQLite-backed persistence layer for a single project's
// .gmk-cache/gmk.db. Open with Open(); close with Close().
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (or creates) the SQLite database at projectRoot/.gmk-cache/gmk.db.
// Applies pending migrations atomically. Safe to call from multiple
// processes targeting the same path — SQLite's file locking serializes
// concurrent schema migrations correctly.
func Open(projectRoot string) (*Store, error) {
	cacheDir := filepath.Join(projectRoot, ".gmk-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", cacheDir, err)
	}
	dbPath := filepath.Join(cacheDir, "gmk.db")

	// _journal=WAL: readers never block writers and crashes don't corrupt.
	// _busy_timeout=5000: when contention happens, wait up to 5s rather
	//                    than failing immediately (matches what most
	//                    SQLite-using tools do).
	// _foreign_keys=on: defensive; we don't currently have FKs but
	//                   future tables (cache_entries -> runs?) will.
	dsn := "file:" + dbPath + "?_journal=WAL&_busy_timeout=5000&_foreign_keys=on"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dbPath, err)
	}
	// SQLite's database/sql driver doesn't open the file until the first
	// query; force a connection now so config errors surface at Open time.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", dbPath, err)
	}

	s := &Store{db: db, path: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database. Safe to call multiple times.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the absolute path to the underlying .db file.
// Useful for diagnostics and for the `sqlite3` CLI.
func (s *Store) Path() string { return s.path }

// migrate brings the DB up to latestVersion. Idempotent.
//
// Two passes:
//
//  1. Read PRAGMA user_version to determine current state.
//  2. Iterate numbered migrations in ascending order, applying each
//     whose target version is > current. Each runs in its own
//     transaction so a failure partway through doesn't leave a
//     half-applied migration.
//
// PRAGMA user_version is set inside the same transaction as the
// migration statements, so concurrent gmk processes can't observe
// a partial-migration state.
func (s *Store) migrate() error {
	var current int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if current > latestVersion {
		// User is running an older gmk binary against a DB last touched
		// by a newer one. We could try to operate forward-compatibly
		// (latest migrations are typically additive), but it's safer
		// to fail loudly so they don't see weird behavior.
		return fmt.Errorf("store: db at version %d, this gmk knows up to %d "+
			"(downgrade not supported; remove %s to re-bootstrap)",
			current, latestVersion, s.path)
	}
	if current == latestVersion {
		return nil // nothing to do
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	log := logger.Get("gmk.store")
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		log.Info("applying migration", "version", m.version, "name", m.name)
		if err := s.applyOne(m); err != nil {
			return fmt.Errorf("store: migration %04d: %w", m.version, err)
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads all embedded migration files, parses their
// version from the filename prefix, and returns them sorted ascending.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read embedded migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 1 {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(parts[0], "%d", &v); err != nil {
			return nil, fmt.Errorf("store: bad migration filename %q (expected NNNN_name.sql): %w",
				e.Name(), err)
		}
		data, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: read embedded %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: name, sql: string(data)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// applyOne runs one migration inside a transaction. The PRAGMA
// user_version update is part of the same tx so failures roll back
// cleanly.
func (s *Store) applyOne(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op if Commit succeeded

	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	// PRAGMA user_version=N must be a literal, not a parameter.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", m.version)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return tx.Commit()
}

// AllocateRun atomically reserves the next monotonic seq for the given
// day and returns it. Day is "YYYYMMDD" (UTC). The caller pairs this
// with a subsequent RecordStart, ideally in the same transaction;
// AllocateRun on its own merely consumes a number — the runs row is
// inserted by RecordStart.
//
// Why split into two calls instead of one? Because the caller needs
// the seq value to build the scratch dir name before it knows everything
// RecordStart wants (started_at, args_json, etc.). The seq is the
// stable identifier the rest of materialize/runner threads through.
//
// Returns the allocated seq (≥ 1).
func (s *Store) AllocateRun(ctx context.Context, day string) (int64, error) {
	if !looksLikeDay(day) {
		return 0, fmt.Errorf("store: invalid day %q (want YYYYMMDD)", day)
	}
	// One statement: INSERT-or-UPDATE with RETURNING.
	// On insert (first run of the day): next_seq becomes 2, returns 1.
	// On conflict (subsequent runs): next_seq += 1, returns the
	// pre-increment value.
	const q = `
        INSERT INTO day_counters (day, next_seq)
        VALUES (?, 2)
        ON CONFLICT(day) DO UPDATE SET next_seq = day_counters.next_seq + 1
        RETURNING next_seq - 1;
    `
	var seq int64
	if err := s.db.QueryRowContext(ctx, q, day).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: allocate seq for %s: %w", day, err)
	}
	return seq, nil
}

// StartParams bundles the fields needed to insert a runs row at the
// moment a callable begins executing.
type StartParams struct {
	Day           string
	Seq           int64
	CallableName  string
	CallableKind  string // "target" or "function"
	StartedAt     time.Time
	ArgsJSON      string
	PreludeJSON   string
	SourceFile    string
}

// RecordStart inserts the runs row in 'running' status.
func (s *Store) RecordStart(ctx context.Context, p StartParams) error {
	const q = `
        INSERT INTO runs (
            day, seq, callable_name, callable_kind, started_at,
            status, args_json, prelude_json, source_file, gmk_pid
        ) VALUES (?, ?, ?, ?, ?, 'running', ?, ?, ?, ?);
    `
	_, err := s.db.ExecContext(ctx, q,
		p.Day, p.Seq, p.CallableName, p.CallableKind,
		p.StartedAt.UnixNano(),
		nullIfEmpty(p.ArgsJSON), nullIfEmpty(p.PreludeJSON),
		nullIfEmpty(p.SourceFile),
		os.Getpid(),
	)
	if err != nil {
		return fmt.Errorf("store: record start %s/%d: %w", p.Day, p.Seq, err)
	}
	return nil
}

// FinishParams bundles the fields needed at run completion.
type FinishParams struct {
	Day        string
	Seq        int64
	FinishedAt time.Time
	Status     string // "ok", "fail", "error", "killed"
	ExitCode   int
	ResultJSON string
}

// FinishRun updates the runs row with completion details. Computes
// duration_ms from the difference between started_at and finished_at.
func (s *Store) FinishRun(ctx context.Context, p FinishParams) error {
	if !isValidStatus(p.Status) {
		return fmt.Errorf("store: invalid finish status %q", p.Status)
	}
	const q = `
        UPDATE runs
        SET finished_at = ?,
            duration_ms = (? - started_at) / 1000000,
            status = ?,
            exit_code = ?,
            result_json = ?
        WHERE day = ? AND seq = ?;
    `
	finishedNs := p.FinishedAt.UnixNano()
	res, err := s.db.ExecContext(ctx, q,
		finishedNs, finishedNs, p.Status, p.ExitCode,
		nullIfEmpty(p.ResultJSON),
		p.Day, p.Seq,
	)
	if err != nil {
		return fmt.Errorf("store: finish %s/%d: %w", p.Day, p.Seq, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("store: finish %s/%d: no such run (was RecordStart called?)",
			p.Day, p.Seq)
	}
	return nil
}

// Run is one row from the runs table. Time fields are time.Time
// (not raw unix-ns) for ergonomic callers. Nullable fields use
// pointer types so absence is distinguishable from zero value.
type Run struct {
	Day           string
	Seq           int64
	CallableName  string
	CallableKind  string
	StartedAt     time.Time
	FinishedAt    *time.Time
	DurationMS    *int64
	Status        string
	ExitCode      *int
	ArgsJSON      string
	PreludeJSON   string
	ResultJSON    string
	SourceFile    string
	GMKPid        int
}

// GetRun looks up one run by (day, seq). Returns sql.ErrNoRows if absent.
func (s *Store) GetRun(ctx context.Context, day string, seq int64) (*Run, error) {
	const q = `
        SELECT day, seq, callable_name, callable_kind, started_at,
               finished_at, duration_ms, status, exit_code,
               args_json, prelude_json, result_json, source_file, gmk_pid
        FROM runs
        WHERE day = ? AND seq = ?;
    `
	row := s.db.QueryRowContext(ctx, q, day, seq)
	return scanRun(row)
}

// ListRunsForDay returns all runs from the given day, ordered by seq.
// Returns an empty slice if no runs.
func (s *Store) ListRunsForDay(ctx context.Context, day string) ([]*Run, error) {
	const q = `
        SELECT day, seq, callable_name, callable_kind, started_at,
               finished_at, duration_ms, status, exit_code,
               args_json, prelude_json, result_json, source_file, gmk_pid
        FROM runs
        WHERE day = ?
        ORDER BY seq ASC;
    `
	rows, err := s.db.QueryContext(ctx, q, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRunning returns all runs currently in 'running' status.
// Useful for diagnosing stuck builds; future "gmk inspect --stuck" cmd.
func (s *Store) ListRunning(ctx context.Context) ([]*Run, error) {
	const q = `
        SELECT day, seq, callable_name, callable_kind, started_at,
               finished_at, duration_ms, status, exit_code,
               args_json, prelude_json, result_json, source_file, gmk_pid
        FROM runs
        WHERE status = 'running'
        ORDER BY started_at ASC;
    `
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for the shared scanRun
// helper. The stdlib doesn't expose a common interface, so we make one.
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner) (*Run, error) {
	var (
		r             Run
		startedNs     int64
		finishedNs    sql.NullInt64
		dur           sql.NullInt64
		exit          sql.NullInt64
		args          sql.NullString
		prelude       sql.NullString
		result        sql.NullString
		source        sql.NullString
	)
	if err := s.Scan(
		&r.Day, &r.Seq, &r.CallableName, &r.CallableKind, &startedNs,
		&finishedNs, &dur, &r.Status, &exit,
		&args, &prelude, &result, &source, &r.GMKPid,
	); err != nil {
		return nil, err
	}
	r.StartedAt = time.Unix(0, startedNs).UTC()
	if finishedNs.Valid {
		t := time.Unix(0, finishedNs.Int64).UTC()
		r.FinishedAt = &t
	}
	if dur.Valid {
		d := dur.Int64
		r.DurationMS = &d
	}
	if exit.Valid {
		e := int(exit.Int64)
		r.ExitCode = &e
	}
	r.ArgsJSON = args.String
	r.PreludeJSON = prelude.String
	r.ResultJSON = result.String
	r.SourceFile = source.String
	return &r, nil
}

// nullIfEmpty returns sql.NullString{Valid: false} for empty input so
// the stored column is SQL NULL rather than "". Avoids the "every
// row's JSON column has an empty-string placeholder" mess.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// looksLikeDay does a quick syntactic check on the day-string format.
// We don't try to validate the calendar date — SQLite will store
// whatever 8-char string we give it, and the caller (materialize)
// always formats via time.Now().Format("20060102") so calendar
// validity is implicit.
func looksLikeDay(d string) bool {
	if len(d) != 8 {
		return false
	}
	for _, c := range d {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isValidStatus(s string) bool {
	switch s {
	case "ok", "fail", "error", "killed":
		return true
	}
	return false
}

// FormatDay returns "YYYYMMDD" for the given time in UTC. Helper so
// callers don't repeat the format string and risk drift.
func FormatDay(t time.Time) string {
	return t.UTC().Format("20060102")
}

// Today returns the current UTC date formatted for use with this store.
func Today() string { return FormatDay(time.Now()) }

// ErrNotFound is returned when a lookup fails for a missing day/seq.
// Callers can check this with errors.Is to distinguish "no such run"
// from genuine database errors.
var ErrNotFound = errors.New("store: not found")
