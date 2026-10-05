package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/adamsjoe/results-service/migrations"
)

// migrationLockID is an arbitrary constant used as a Postgres advisory lock,
// so two processes migrating at once cannot both apply the same file.
const migrationLockID = 727274

// Migrate applies any migration files not yet recorded in schema_migrations,
// in name order, each in its own transaction. It returns the versions applied
// by this call; an up-to-date database returns none.
func Migrate(ctx context.Context, databaseURL string) ([]string, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	done, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if done[version] {
			continue
		}
		if err := applyOne(ctx, conn, name, version); err != nil {
			return applied, err
		}
		applied = append(applied, version)
	}
	return applied, nil
}

func appliedVersions(ctx context.Context, conn *pgx.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	done := make(map[string]bool, len(versions))
	for _, v := range versions {
		done[v] = true
	}
	return done, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, name, version string) error {
	sql, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		return err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }() // no-op after commit

	// No arguments, so pgx uses the simple protocol, which allows a file
	// to hold several statements.
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return tx.Commit(ctx)
}
