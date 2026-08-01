//go:build integration

// Run with: go test -tags=integration ./internal/delivery/confirm/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package confirm_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/delivery/confirm"
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
	return pool
}

func insertMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, state model.MessageState, window model.Window) string {
	t.Helper()
	msg := &model.Message{
		Name:   "Test",
		State:  state,
		Window: window,
		ControlPolicy: model.ControlPolicy{
			PerMessageCap:              1,
			MinIntervalBetween:         time.Hour,
			RequiresServerConfirmation: true,
		},
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	return msg.ID
}

func TestConfirmApprovesAnActiveInWindowMessage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	id := insertMessage(t, ctx, pool, model.MessageStateActive, model.Window{
		Start: now.Add(-time.Hour), End: now.Add(time.Hour),
	})

	approved, err := confirm.Confirm(ctx, pool, id, now)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !approved {
		t.Error("Confirm = false, want true for an active, in-window message")
	}
}

func TestConfirmDeniesAPausedMessage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	id := insertMessage(t, ctx, pool, model.MessageStatePaused, model.Window{
		Start: now.Add(-time.Hour), End: now.Add(time.Hour),
	})

	approved, err := confirm.Confirm(ctx, pool, id, now)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if approved {
		t.Error("Confirm = true, want false for a paused message")
	}
}

func TestConfirmDeniesAMessagePastItsWindow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	id := insertMessage(t, ctx, pool, model.MessageStateActive, model.Window{
		Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour),
	})

	approved, err := confirm.Confirm(ctx, pool, id, now)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if approved {
		t.Error("Confirm = true, want false for a message whose window already ended")
	}
}

// TestConfirmFailsClosedOnAnUnknownMessage demonstrates CW-0002 Unit 4: an error case (here, the
// message doesn't exist) never resolves to true.
func TestConfirmFailsClosedOnAnUnknownMessage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	approved, err := confirm.Confirm(ctx, pool, "00000000-0000-0000-0000-000000000000", time.Now())
	if err == nil {
		t.Fatal("Confirm: got nil error for an unknown message, want an error")
	}
	if approved {
		t.Error("Confirm = true on an error path, want false — this must always fail closed")
	}
}
