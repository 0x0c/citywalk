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

	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/postgres"
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

	mux, err := connectserver.NewMux(pool)
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

	mux, err := connectserver.NewMux(pool)
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
