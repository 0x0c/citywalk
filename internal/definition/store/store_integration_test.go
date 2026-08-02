//go:build integration

// Run with: go test -tags=integration ./internal/definition/store/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
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

// TestInsertAndGetMessageRoundTrip demonstrates CW-0003 Unit 3's storage representation end to end:
// every entity a Message owns survives a write and a read against the real schema, including the
// tagged-union content document stored in the variants.content column.
func TestInsertAndGetMessageRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	msg := &model.Message{
		Name:            "Route complete",
		State:           model.MessageStateDraft,
		Priority:        5,
		Window:          model.Window{Start: now, End: now.Add(24 * time.Hour)},
		AudienceRef:     "segment-123",
		HoldoutFraction: 0.1,
		ConversionEvent: "route_completed",
		ControlPolicy: model.ControlPolicy{
			PerMessageCap:        3,
			MinIntervalBetween:   2 * time.Hour,
			ExemptFromProjectCap: false,
		},
		Triggers: []model.Trigger{
			{Kind: "route_finished", OccurrenceGoal: 1, EventPredicate: `event.distance_km > 5.0`},
		},
		DisplayConditions: []model.DisplayCondition{
			{
				Delay:                10 * time.Second,
				ScreenFilterMode:     model.ScreenFilterDeny,
				Screens:              []string{"onboarding"},
				ConnectivityRequired: model.ConnectivityOnline,
			},
		},
		Variants: []model.Variant{
			{
				Weight:        100,
				Language:      "en",
				SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor, Minor: 0},
				Content: model.DialogContent{
					Presentation: model.Presentation{
						Heading: "Nice walk!",
						Body:    "You just finished a 5 km route.",
						Colors:  model.Colors{Background: "#FFFFFF", Text: "#000000"},
						Buttons: []model.Button{
							{Label: "Share", Actions: []model.Action{
								model.EmitEventAction{EventName: "share_tapped"},
							}},
						},
					},
				},
			},
		},
	}

	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if msg.ID == "" {
		t.Fatal("InsertMessage: msg.ID is empty after insert")
	}

	got, err := store.GetMessage(ctx, pool, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}

	if got.Name != msg.Name {
		t.Errorf("Name = %q, want %q", got.Name, msg.Name)
	}
	if got.State != msg.State {
		t.Errorf("State = %q, want %q", got.State, msg.State)
	}
	if got.AudienceRef != msg.AudienceRef {
		t.Errorf("AudienceRef = %q, want %q", got.AudienceRef, msg.AudienceRef)
	}
	if !got.Window.Start.Equal(msg.Window.Start) || !got.Window.End.Equal(msg.Window.End) {
		t.Errorf("Window = %+v, want %+v", got.Window, msg.Window)
	}
	if got.ControlPolicy != msg.ControlPolicy {
		t.Errorf("ControlPolicy = %+v, want %+v", got.ControlPolicy, msg.ControlPolicy)
	}

	if len(got.Triggers) != 1 || got.Triggers[0].Kind != "route_finished" || got.Triggers[0].EventPredicate != `event.distance_km > 5.0` {
		t.Errorf("Triggers = %+v, want one route_finished trigger with the distance predicate", got.Triggers)
	}

	if len(got.DisplayConditions) != 1 {
		t.Fatalf("len(DisplayConditions) = %d, want 1", len(got.DisplayConditions))
	}
	dc := got.DisplayConditions[0]
	if dc.Delay != 10*time.Second || dc.ScreenFilterMode != model.ScreenFilterDeny || dc.ConnectivityRequired != model.ConnectivityOnline {
		t.Errorf("DisplayConditions[0] = %+v, want delay=10s deny online", dc)
	}
	if len(dc.Screens) != 1 || dc.Screens[0] != "onboarding" {
		t.Errorf("DisplayConditions[0].Screens = %v, want [onboarding]", dc.Screens)
	}

	if len(got.Variants) != 1 {
		t.Fatalf("len(Variants) = %d, want 1", len(got.Variants))
	}
	dialog, ok := got.Variants[0].Content.(model.DialogContent)
	if !ok {
		t.Fatalf("Variants[0].Content type = %T, want model.DialogContent", got.Variants[0].Content)
	}
	if dialog.Heading != "Nice walk!" {
		t.Errorf("Variants[0].Content.Heading = %q, want %q", dialog.Heading, "Nice walk!")
	}
	if len(dialog.Buttons) != 1 || len(dialog.Buttons[0].Actions) != 1 {
		t.Fatalf("Variants[0].Content.Buttons = %+v, want one button with one action", dialog.Buttons)
	}
	if _, ok := dialog.Buttons[0].Actions[0].(model.EmitEventAction); !ok {
		t.Errorf("Variants[0].Content.Buttons[0].Actions[0] type = %T, want model.EmitEventAction", dialog.Buttons[0].Actions[0])
	}

	if got.ExperimentSalt == "" {
		t.Error("ExperimentSalt is empty, want a non-empty value generated at insert (CW-0008 Unit 2)")
	}
}

// TestGetMessageNotFound demonstrates GetMessage reports store.ErrNotFound for an ID nothing wrote.
func TestGetMessageNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := store.GetMessage(ctx, pool, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage: err = %v, want it to wrap store.ErrNotFound", err)
	}
}

func insertDraftMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time) string {
	t.Helper()
	msg := &model.Message{
		Name: "Kill switch fixture", State: model.MessageStateActive,
		Window: model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
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

// TestUpdateStateAppliesAnAllowedTransitionAndRecordsAuditLog is CW-0001 Unit 1's kill switch end to
// end: pausing an active campaign updates its state and leaves an audit trail of the move.
func TestUpdateStateAppliesAnAllowedTransitionAndRecordsAuditLog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	messageID := insertDraftMessage(t, ctx, pool, now)

	if err := store.UpdateState(ctx, pool, messageID, model.MessageStatePaused, "alice", now); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	got, err := store.GetMessage(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.State != model.MessageStatePaused {
		t.Errorf("State = %q, want %q", got.State, model.MessageStatePaused)
	}

	entries, err := store.ListAuditLog(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].FromState != model.MessageStateActive || entries[0].ToState != model.MessageStatePaused {
		t.Errorf("entries[0] = %+v, want active -> paused", entries[0])
	}
	if entries[0].Actor != "alice" {
		t.Errorf("entries[0].Actor = %q, want %q", entries[0].Actor, "alice")
	}
	if !entries[0].OccurredAt.Equal(now) {
		t.Errorf("entries[0].OccurredAt = %v, want %v", entries[0].OccurredAt, now)
	}
}

// TestUpdateStateRejectsABackwardTransition demonstrates FR-MSG-01's "a transition only moves
// forward" fails closed: the state is left untouched and no audit entry is written.
func TestUpdateStateRejectsABackwardTransition(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	messageID := insertDraftMessage(t, ctx, pool, now)

	if err := store.UpdateState(ctx, pool, messageID, model.MessageStateDraft, "alice", now); err == nil {
		t.Fatal("UpdateState: got nil error moving active -> draft, want an error")
	}

	got, err := store.GetMessage(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.State != model.MessageStateActive {
		t.Errorf("State = %q, want unchanged %q after a rejected transition", got.State, model.MessageStateActive)
	}

	entries, err := store.ListAuditLog(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("len(entries) = %d, want 0 (a rejected transition leaves no audit trail)", len(entries))
	}
}

// TestUpdateStateOnUnknownMessage demonstrates the same fail-closed contract GetMessage has.
func TestUpdateStateOnUnknownMessage(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	err := store.UpdateState(ctx, pool, "00000000-0000-0000-0000-000000000000", model.MessageStatePaused, "alice", time.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateState: err = %v, want it to wrap store.ErrNotFound", err)
	}
}
