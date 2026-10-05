// Package store implements service.Store on Postgres.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/adamsjoe/results-service/internal/service"
)

// Postgres is a service.Store backed by a pgx connection pool.
type Postgres struct {
	pool *pgxpool.Pool
}

var _ service.Store = (*Postgres)(nil)

// NewPostgres connects to databaseURL and checks the connection.
func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close releases the pool's connections.
func (p *Postgres) Close() {
	p.pool.Close()
}

var statusText = map[service.Status]string{
	service.StatusPassed:  "PASSED",
	service.StatusFailed:  "FAILED",
	service.StatusSkipped: "SKIPPED",
}

// CreateRun implements service.Store.
func (p *Postgres) CreateRun(ctx context.Context, suite, branch, commitSHA string) (service.Run, error) {
	run := service.Run{Suite: suite, Branch: branch, CommitSHA: commitSHA}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO runs (suite, branch, commit_sha) VALUES ($1, $2, $3)
		 RETURNING id::text, started_at`,
		suite, branch, commitSHA,
	).Scan(&run.ID, &run.StartedAt)
	if err != nil {
		return service.Run{}, fmt.Errorf("create run: %w", err)
	}
	run.StartedAt = run.StartedAt.UTC()
	return run, nil
}

// runColumns selects a run with its result counts; used with a LEFT JOIN on
// results and GROUP BY r.id.
const runColumns = `
	r.id::text, r.suite, r.branch, r.commit_sha, r.started_at,
	count(*) FILTER (WHERE res.status = 'PASSED'),
	count(*) FILTER (WHERE res.status = 'FAILED'),
	count(*) FILTER (WHERE res.status = 'SKIPPED')`

func scanRun(row pgx.Row) (service.Run, error) {
	var r service.Run
	var started time.Time
	if err := row.Scan(&r.ID, &r.Suite, &r.Branch, &r.CommitSHA, &started, &r.Passed, &r.Failed, &r.Skipped); err != nil {
		return service.Run{}, err
	}
	r.StartedAt = started.UTC()
	return r, nil
}

// GetRun implements service.Store.
func (p *Postgres) GetRun(ctx context.Context, id string) (service.Run, error) {
	row := p.pool.QueryRow(ctx, `
		SELECT `+runColumns+`
		FROM runs r LEFT JOIN results res ON res.run_id = r.id
		WHERE r.id = $1
		GROUP BY r.id`, id)

	run, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return service.Run{}, fmt.Errorf("run %q: %w", id, service.ErrNotFound)
	}
	if err != nil {
		return service.Run{}, fmt.Errorf("get run: %w", err)
	}
	return run, nil
}

// AddResults implements service.Store. The existence check and the insert run
// in one transaction, so a batch is stored completely or not at all.
func (p *Postgres) AddResults(ctx context.Context, runID string, results []service.TestResult) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }() // no-op after commit

	var exists bool
	err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1)", runID).Scan(&exists)
	if isInvalidUUID(err) || (err == nil && !exists) {
		return fmt.Errorf("run %q: %w", runID, service.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check run: %w", err)
	}

	if len(results) > 0 {
		_, err = tx.CopyFrom(ctx,
			pgx.Identifier{"results"},
			[]string{"run_id", "name", "status", "duration_ms", "error_message"},
			pgx.CopyFromSlice(len(results), func(i int) ([]any, error) {
				r := results[i]
				var errMsg any // NULL unless there is a message
				if r.ErrorMessage != "" {
					errMsg = r.ErrorMessage
				}
				return []any{runID, r.Name, statusText[r.Status], r.DurationMS, errMsg}, nil
			}),
		)
		if isForeignKeyViolation(err) { // run deleted since the check
			return fmt.Errorf("run %q: %w", runID, service.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("insert results: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListRuns implements service.Store.
func (p *Postgres) ListRuns(ctx context.Context, q service.ListRunsQuery) ([]service.Run, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM runs r LEFT JOIN results res ON res.run_id = r.id
		WHERE $1 = '' OR r.suite = $1
		GROUP BY r.id
		ORDER BY r.started_at DESC, r.id DESC
		LIMIT $2 OFFSET $3`, q.Suite, q.Limit, q.Offset)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	runs := []service.Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("list runs: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// isInvalidUUID reports an id Postgres could not parse as a UUID. Such an id
// cannot match any run, so callers treat it as not found.
func isInvalidUUID(err error) bool { return pgCode(err) == "22P02" }

func isForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }
