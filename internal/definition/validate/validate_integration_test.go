//go:build integration

// Run with: go test -tags=integration ./internal/definition/validate/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package validate_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/definition/validate"
	"github.com/0x0c/citywalk/internal/membership/segment"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM segments`); err != nil {
		t.Fatalf("clear segments: %v", err)
	}
	return pool
}

// TestValidateAcceptsAnAudienceRefNamingARealSegment and
// TestValidateRejectsAnAudienceRefNamingNoSegment are CW-0003 Unit 5's referential integrity check
// end to end: a message's AudienceRef must name a row that actually exists in segments.
func TestValidateAcceptsAnAudienceRefNamingARealSegment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	seg, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}

	msg := validMessage(now)
	msg.AudienceRef = seg.ID

	if err := validate.Validate(ctx, pool, msg, now); err != nil {
		t.Errorf("Validate: %v, want nil for a real segment reference", err)
	}
}

func TestValidateRejectsAnAudienceRefNamingNoSegment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	msg := validMessage(now)
	msg.AudienceRef = "00000000-0000-0000-0000-000000000000"

	err := validate.Validate(ctx, pool, msg, now)
	if err == nil {
		t.Fatal("Validate: got nil error, want a referential integrity error")
	}
	if !strings.Contains(err.Error(), "does not name an existing segment") {
		t.Errorf("Validate error = %q, want it to mention the missing segment", err)
	}
}

func TestValidateAcceptsAnEmptyAudienceRef(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	msg := validMessage(now)
	msg.AudienceRef = ""

	if err := validate.Validate(ctx, pool, msg, now); err != nil {
		t.Errorf("Validate: %v, want nil for an empty audience_ref (reaches everyone)", err)
	}
}
