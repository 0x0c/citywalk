//go:build integration

// Run with: go test -tags=integration ./internal/platform/connectserver/... with
// CITYWALK_TEST_POSTGRES_DSN pointing at a scratch PostgreSQL database.
package connectserver_test

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/membership/segment"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
	"github.com/0x0c/citywalk/migrations"
)

// TestConfirmRPCApprovesAnActiveMessage exercises CW-0002 Unit 4's concrete endpoint end to end:
// a real Connect client call over HTTP, through the registered handler, into confirm.Confirm and
// back out.
func TestConfirmRPCApprovesAnActiveMessage(t *testing.T) {
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

	now := time.Now()
	msg := &model.Message{
		Name: "Confirm me", State: model.MessageStateActive,
		Window: model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		ControlPolicy: model.ControlPolicy{
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

	mux, err := connectserver.NewMux(pool, nil)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)
	resp, err := client.Confirm(ctx, connect.NewRequest(&deliveryv1.ConfirmRequest{MessageId: msg.ID}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !resp.Msg.GetApproved() {
		t.Error("Approved = false, want true for an active, in-window, server-confirmed message")
	}
}

// TestConfirmRPCDeniesAnUnknownMessage demonstrates the fail-closed contract over the wire: an
// unknown message produces an RPC error, and the response is never a false-but-successful Approved.
func TestConfirmRPCDeniesAnUnknownMessage(t *testing.T) {
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

	mux, err := connectserver.NewMux(pool, nil)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)
	_, err = client.Confirm(ctx, connect.NewRequest(&deliveryv1.ConfirmRequest{
		MessageId: "00000000-0000-0000-0000-000000000000",
	}))
	if err == nil {
		t.Fatal("Confirm: got nil error for an unknown message, want an RPC error")
	}
}

// TestSyncRPCWithoutRedisFailsCleanly demonstrates that Sync reports CodeUnavailable rather than
// panicking when the process was started without Redis (CW-0010 Unit 11: both stores are optional).
func TestSyncRPCWithoutRedisFailsCleanly(t *testing.T) {
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

	mux, err := connectserver.NewMux(pool, nil)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)
	_, err = client.Sync(ctx, connect.NewRequest(&deliveryv1.SyncRequest{ChannelId: "any"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Sync: err = %v, want CodeUnavailable", err)
	}
}

// TestSyncRPCReturnsAPayloadThenUnchanged exercises CW-0006 Unit 2 over a real Connect RPC call:
// the first synchronization returns entries and a tag, and a second call with that tag returns
// Unchanged with no entries.
func TestSyncRPCReturnsAPayloadThenUnchanged(t *testing.T) {
	pgDSN := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("CITYWALK_TEST_REDIS_ADDR")
	if pgDSN == "" || redisAddr == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR must both be set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, pgDSN)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	redisClient, err := redisclient.New(ctx, redisAddr)
	if err != nil {
		t.Fatalf("redisclient.New: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })
	for _, table := range []string{"conversion_attributions", "message_audit_log", "events_log", "segment_membership", "segments", "channel_ordinals", "channels", "messages"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE membership_generation SET generation = 0`); err != nil {
		t.Fatalf("reset generation: %v", err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	now := time.Now()
	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ($1) RETURNING id`,
		map[string]any{"country": "JP"},
	).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if _, err := ordinal.Allocate(ctx, pool, channelID); err != nil {
		t.Fatalf("allocate ordinal: %v", err)
	}
	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()
	seg, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save: %v", err)
	}
	msg := &model.Message{
		Name: "Konnichiwa", State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		Variants: []model.Variant{{
			Weight: 100, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor},
			Content: model.DialogContent{},
		}},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	mux, err := connectserver.NewMux(pool, redisClient)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)

	first, err := client.Sync(ctx, connect.NewRequest(&deliveryv1.SyncRequest{ChannelId: channelID, Language: "en"}))
	if err != nil {
		t.Fatalf("Sync (first): %v", err)
	}
	if first.Msg.GetUnchanged() || len(first.Msg.GetEntries()) != 1 {
		t.Fatalf("first Sync: unchanged=%v entries=%d, want unchanged=false entries=1", first.Msg.GetUnchanged(), len(first.Msg.GetEntries()))
	}
	// CW-0007 Unit 5's issuance: a fresh channel with no recorded impressions gets the full cap back.
	if first.Msg.ProjectBudgetRemaining == nil || first.Msg.GetProjectBudgetRemaining() != 2 {
		t.Errorf("first Sync: ProjectBudgetRemaining = %v (present=%v), want 2 (present=true)",
			first.Msg.GetProjectBudgetRemaining(), first.Msg.ProjectBudgetRemaining != nil)
	}

	second, err := client.Sync(ctx, connect.NewRequest(&deliveryv1.SyncRequest{
		ChannelId: channelID, Language: "en", Etag: first.Msg.GetEtag(),
	}))
	if err != nil {
		t.Fatalf("Sync (second): %v", err)
	}
	if !second.Msg.GetUnchanged() || len(second.Msg.GetEntries()) != 0 {
		t.Fatalf("second Sync: unchanged=%v entries=%d, want unchanged=true entries=0", second.Msg.GetUnchanged(), len(second.Msg.GetEntries()))
	}
}

func strictConfirmTestDeps(t *testing.T) (*pgxpool.Pool, *redis.Client) {
	t.Helper()
	pgDSN := os.Getenv("CITYWALK_TEST_POSTGRES_DSN")
	redisAddr := os.Getenv("CITYWALK_TEST_REDIS_ADDR")
	if pgDSN == "" || redisAddr == "" {
		t.Skip("CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR must both be set")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, pgDSN)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	redisClient, err := redisclient.New(ctx, redisAddr)
	if err != nil {
		t.Fatalf("redisclient.New: %v", err)
	}
	t.Cleanup(func() { _ = redisClient.Close() })
	for _, table := range []string{"conversion_attributions", "events_log", "channels", "messages"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
	return pool, redisClient
}

func insertStrictMessage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, now time.Time, exempt bool) string {
	t.Helper()
	msg := &model.Message{
		Name: "Strict campaign", State: model.MessageStateActive,
		Window: model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		ControlPolicy: model.ControlPolicy{
			RequiresServerConfirmation: true,
			ExemptFromProjectCap:       exempt,
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

// TestConfirmRPCEnforcesTheProjectBudgetAndReportsExhaustion is CW-0007 Unit 6 end to end: a
// non-exempt, strict campaign's confirmation atomically spends the project-wide budget, denies once
// it's exhausted, and records the denial as a suppression event carrying CW-0009's project_budget
// reason.
func TestConfirmRPCEnforcesTheProjectBudgetAndReportsExhaustion(t *testing.T) {
	pool, redisClient := strictConfirmTestDeps(t)
	ctx := context.Background()
	now := time.Now()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	messageID := insertStrictMessage(t, ctx, pool, now, false)

	mux, err := connectserver.NewMux(pool, redisClient)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)

	// The phase-one default project budget cap is 2 (connectserver.defaultProjectBudgetCap): the
	// first two confirmations must succeed and spend it.
	for i := 0; i < 2; i++ {
		resp, err := client.Confirm(ctx, connect.NewRequest(&deliveryv1.ConfirmRequest{
			MessageId: messageID, ChannelId: channelID,
		}))
		if err != nil {
			t.Fatalf("Confirm (call %d): %v", i, err)
		}
		if !resp.Msg.GetApproved() {
			t.Fatalf("Confirm (call %d) = false, want true (within the budget)", i)
		}
	}

	third, err := client.Confirm(ctx, connect.NewRequest(&deliveryv1.ConfirmRequest{
		MessageId: messageID, ChannelId: channelID,
	}))
	if err != nil {
		t.Fatalf("Confirm (3rd call): %v", err)
	}
	if third.Msg.GetApproved() {
		t.Fatal("Confirm (3rd call) = true, want false (budget of 2 already exhausted)")
	}

	var suppressionCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events_log WHERE channel_id = $1 AND kind = 'suppression' AND suppression_reason = 'project_budget'`,
		channelID,
	).Scan(&suppressionCount); err != nil {
		t.Fatalf("count suppression events: %v", err)
	}
	if suppressionCount != 1 {
		t.Errorf("suppression events with reason project_budget = %d, want 1", suppressionCount)
	}
}

// TestConfirmRPCBypassesTheBudgetForExemptCampaigns is CW-0007 Unit 5's exemption bypass: a campaign
// flagged exempt from the project cap keeps approving past the point a non-exempt one would be
// denied.
func TestConfirmRPCBypassesTheBudgetForExemptCampaigns(t *testing.T) {
	pool, redisClient := strictConfirmTestDeps(t)
	ctx := context.Background()
	now := time.Now()

	var channelID string
	if err := pool.QueryRow(ctx, `INSERT INTO channels (attributes) VALUES ('{}') RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	messageID := insertStrictMessage(t, ctx, pool, now, true)

	mux, err := connectserver.NewMux(pool, redisClient)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := deliveryv1connect.NewDeliveryServiceClient(server.Client(), server.URL)

	for i := 0; i < 3; i++ {
		resp, err := client.Confirm(ctx, connect.NewRequest(&deliveryv1.ConfirmRequest{
			MessageId: messageID, ChannelId: channelID,
		}))
		if err != nil {
			t.Fatalf("Confirm (call %d): %v", i, err)
		}
		if !resp.Msg.GetApproved() {
			t.Errorf("Confirm (call %d) = false, want true (exempt campaigns bypass the budget entirely)", i)
		}
	}
}
