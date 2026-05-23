// Package main is the demo backend: a minimal HTTP server that
// connects to Postgres and exposes a couple of /api endpoints the
// frontend can hit. Kept deliberately small to stay readable in a
// demo context — not a production template.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id         SERIAL PRIMARY KEY,
	body       TEXT        NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

const seed = `
INSERT INTO messages (body) VALUES
	('hello from postgres'),
	('this row was inserted on first boot'),
	('edit me via psql to see live data')
ON CONFLICT DO NOTHING;
`

type message struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type server struct {
	db *sql.DB
}

func main() {
	dsn := buildDSN()

	db, err := openWithRetry(dsn, 30, 2*time.Second)
	if err != nil {
		log.Fatalf("postgres unreachable after retries: %v", err)
	}
	defer db.Close()

	if err := bootstrap(db); err != nil {
		log.Fatalf("schema bootstrap failed: %v", err)
	}

	srv := &server{db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", srv.handleHealth)
	mux.HandleFunc("/api/messages", srv.handleMessages)

	addr := ":" + getenv("PORT", "8080")
	log.Printf("backend listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// buildDSN constructs the libpq DSN from env vars set by the k8s
// Deployment (see templates/app.yaml.jinja).
func buildDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		getenv("DB_HOST", "postgres"),
		getenv("DB_PORT", "5432"),
		getenv("DB_USER", "appuser"),
		getenv("DB_PASSWORD", "apppass"),
		getenv("DB_NAME", "appdb"),
	)
}

// openWithRetry keeps trying until postgres is reachable. k8s starts
// pods in roughly the right order but postgres can take 5-15 seconds
// to be query-ready even after the pod reports Ready.
func openWithRetry(dsn string, attempts int, gap time.Duration) (*sql.DB, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		db, err := sql.Open("postgres", dsn)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = db.PingContext(ctx)
			cancel()
			if err == nil {
				return db, nil
			}
			db.Close()
		}
		lastErr = err
		log.Printf("postgres not ready (attempt %d/%d): %v", i+1, attempts, err)
		time.Sleep(gap)
	}
	return nil, lastErr
}

func bootstrap(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := db.Exec(seed); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "db unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, body, created_at FROM messages ORDER BY id DESC LIMIT 50`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := make([]message, 0, 50)
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.ID, &m.Body, &m.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, m)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
