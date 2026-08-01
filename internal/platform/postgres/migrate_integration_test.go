//go:build integration

// Run with: go test -tags=integration ./internal/platform/postgres/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package postgres_test

import (
	"context"
	"os"
	"testing"
	"testing/fstest"

	"github.com/0x0c/citywalk/internal/platform/postgres"
)

func TestMigrateAppliesOnceAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN not set")
	}

	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	migrationFS := fstest.MapFS{
		"0001_create_widgets.sql": {Data: []byte(`CREATE TABLE widgets (id serial PRIMARY KEY)`)},
	}

	if err := postgres.Migrate(ctx, pool, migrationFS); err != nil {
		t.Fatalf("Migrate (first run): %v", err)
	}
	// Applying the same migration set again must be a no-op: schema_migrations already records the
	// filename, so CREATE TABLE never runs twice and never errors on the second run.
	if err := postgres.Migrate(ctx, pool, migrationFS); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'widgets'`).Scan(&count); err != nil {
		t.Fatalf("query widgets: %v", err)
	}
	if count != 1 {
		t.Errorf("widgets table count = %d, want 1", count)
	}

	if _, err := pool.Exec(ctx, `DROP TABLE widgets`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
