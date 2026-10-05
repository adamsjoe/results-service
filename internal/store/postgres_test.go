//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/adamsjoe/results-service/internal/service"
	"github.com/adamsjoe/results-service/internal/store"
	"github.com/adamsjoe/results-service/internal/storetest"
)

var (
	databaseURL string
	admin       *pgxpool.Pool // direct SQL access for setup and assertions
)

// TestMain uses TEST_DATABASE_URL when set (as `make test` does); otherwise it
// starts a Postgres container with testcontainers-go (as CI does).
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	databaseURL = os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		url, stop, err := startPostgres(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "start postgres container:", err)
			return 1
		}
		defer stop()
		databaseURL = url
	}

	if _, err := store.Migrate(ctx, databaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}

	var err error
	admin, err = pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer admin.Close()

	return m.Run()
}

// newStore empties the tables and returns a store connected to them.
func newStore(t *testing.T) *store.Postgres {
	t.Helper()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, "TRUNCATE runs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s, err := store.NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) service.Store { return newStore(t) })
}

func TestMigrate_SecondRunAppliesNothing(t *testing.T) {
	applied, err := store.Migrate(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("expected nothing to apply, got %v", applied)
	}
}

func TestMigrate_RecordsAppliedVersions(t *testing.T) {
	var exists bool
	err := admin.QueryRow(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = '0001_create_runs_and_results')",
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Error("expected 0001_create_runs_and_results to be recorded")
	}
}

func TestAddResults_BatchIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run, err := s.CreateRun(ctx, "x", "main", "abc")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// The service would reject the second result; calling the store directly
	// shows the database constraint stops the whole batch, not just that row.
	batch := []service.TestResult{
		{Name: "valid", Status: service.StatusPassed, DurationMS: 1},
		{Name: "negative", Status: service.StatusPassed, DurationMS: -1},
	}
	err = s.AddResults(ctx, run.ID, batch)
	if err == nil || errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected a constraint error, got %v", err)
	}

	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Passed != 0 {
		t.Errorf("expected no results stored, got %d passed", got.Passed)
	}
}

func TestAddResults_ErrorMessageIsNullWhenEmpty(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run, err := s.CreateRun(ctx, "x", "main", "abc")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	err = s.AddResults(ctx, run.ID, []service.TestResult{
		{Name: "ok", Status: service.StatusPassed, DurationMS: 1},
		{Name: "bad", Status: service.StatusFailed, DurationMS: 1, ErrorMessage: "boom"},
	})
	if err != nil {
		t.Fatalf("AddResults: %v", err)
	}

	var okMsg, badMsg *string
	err = admin.QueryRow(ctx, `SELECT
		(SELECT error_message FROM results WHERE run_id = $1 AND name = 'ok'),
		(SELECT error_message FROM results WHERE run_id = $1 AND name = 'bad')`, run.ID,
	).Scan(&okMsg, &badMsg)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if okMsg != nil {
		t.Errorf("expected NULL for a passing result, got %q", *okMsg)
	}
	if badMsg == nil || *badMsg != "boom" {
		t.Errorf("expected \"boom\" for the failing result, got %v", badMsg)
	}
}

func TestSchema_RejectsUnknownStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run, err := s.CreateRun(ctx, "x", "main", "abc")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	_, err = admin.Exec(ctx,
		"INSERT INTO results (run_id, name, status, duration_ms) VALUES ($1, 'x', 'BROKEN', 1)", run.ID)
	if code := pgCode(err); code != "23514" { // check_violation
		t.Errorf("expected check violation (23514), got %v", err)
	}
}

func TestSchema_DeletingRunDeletesItsResults(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run, err := s.CreateRun(ctx, "x", "main", "abc")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.AddResults(ctx, run.ID, []service.TestResult{{Name: "a", Status: service.StatusPassed}}); err != nil {
		t.Fatalf("AddResults: %v", err)
	}

	if _, err := admin.Exec(ctx, "DELETE FROM runs WHERE id = $1", run.ID); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	var remaining int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM results WHERE run_id = $1", run.ID).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("expected results to be deleted with the run, %d remain", remaining)
	}
}
