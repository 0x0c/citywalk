package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate applies every migration in migrationFS that schema_migrations does not yet record,
// in filename order, each in its own transaction. It implements the migration-before-code rule
// CW-0010 Unit 3 sets: a migration ships in the release before the code that reads it, so a
// rollback is possible at every point.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrationFS fs.FS) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    text PRIMARY KEY,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}

	var filenames []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		filenames = append(filenames, entry.Name())
	}
	sort.Strings(filenames)

	for _, filename := range filenames {
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`, filename,
		).Scan(&applied); err != nil {
			return fmt.Errorf("postgres: check %s: %w", filename, err)
		}
		if applied {
			continue
		}

		contents, err := fs.ReadFile(migrationFS, filename)
		if err != nil {
			return fmt.Errorf("postgres: read %s: %w", filename, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("postgres: begin %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: apply %s: %w", filename, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, filename,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("postgres: record %s: %w", filename, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("postgres: commit %s: %w", filename, err)
		}
	}

	return nil
}
