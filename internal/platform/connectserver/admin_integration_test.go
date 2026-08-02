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
	"google.golang.org/protobuf/types/known/timestamppb"

	adminv1 "github.com/0x0c/citywalk/gen/citywalk/admin/v1"
	"github.com/0x0c/citywalk/gen/citywalk/admin/v1/adminv1connect"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/migrations"
)

func adminTestPool(t *testing.T) *pgxpool.Pool {
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
	for _, table := range []string{"message_audit_log", "delivery_change_log", "messages"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	return pool
}

func adminAuthed[T any](req *connect.Request[T], apiKey string) *connect.Request[T] {
	req.Header().Set("Authorization", "Bearer "+apiKey)
	return req
}

func newMessageDefinition(now time.Time) *adminv1.MessageDefinition {
	return &adminv1.MessageDefinition{
		Name:        "Admin API campaign",
		WindowStart: timestamppb.New(now.Add(-time.Hour)),
		WindowEnd:   timestamppb.New(now.Add(time.Hour)),
		VariantsJson: []byte(`[{
			"id": "", "weight": 100, "language": "en",
			"content_column": {
				"schema_version": {"major": 1, "minor": 0},
				"content": {"layout": "dialog", "heading": "", "body": "", "colors": {"background": "", "text": ""}, "corner_radius": 0}
			}
		}]`),
	}
}

// TestCreateMessageRPCPersistsAValidatedDraft exercises CW-0001 Unit 1's administrative surface end
// to end: an editor's CreateMessage call validates (CW-0003 Unit 5) and persists a message, always
// starting in draft regardless of what the request set (FR-MSG-01).
func TestCreateMessageRPCPersistsAValidatedDraft(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	resp, err := client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}), "test-editor-key"))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if resp.Msg.GetMessageId() == "" {
		t.Fatal("MessageId is empty, want a generated message id")
	}

	got, err := client.GetMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.GetMessageRequest{
		MessageId: resp.Msg.GetMessageId(),
	}), "test-viewer-key"))
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.Msg.GetMessage().GetState() != "draft" {
		t.Errorf("State = %q, want %q", got.Msg.GetMessage().GetState(), "draft")
	}
}

// TestCreateMessageRPCRejectsAnInvalidDefinition demonstrates CW-0003 Unit 5's save-time validation
// runs over the wire: a window that has already closed is rejected rather than persisted.
func TestCreateMessageRPCRejectsAnInvalidDefinition(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	def := newMessageDefinition(now)
	def.WindowEnd = timestamppb.New(now.Add(-time.Hour)) // already closed

	_, err = client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: def,
	}), "test-editor-key"))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateMessage (closed window): err = %v, want CodeInvalidArgument", err)
	}
}

// TestCreateMessageRPCRejectsAViewer is CW-0010 Unit 9's role authorization enforced over the wire:
// a viewer cannot perform a mutation CreateMessage requires at least editor for.
func TestCreateMessageRPCRejectsAViewer(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	_, err = client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}), "test-viewer-key"))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateMessage (viewer): err = %v, want CodePermissionDenied", err)
	}
}

// TestCreateMessageRPCRejectsAnUnauthenticatedCaller demonstrates CW-0010 Unit 9's administrative
// authentication is actually enforced: a call with no recognized bearer credential is rejected
// before it ever reaches store.InsertMessage.
func TestCreateMessageRPCRejectsAnUnauthenticatedCaller(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	_, err = client.CreateMessage(ctx, connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("CreateMessage (no credential): err = %v, want CodeUnauthenticated", err)
	}
}

// TestUpdateMessageStateRPCRecordsTheAuthenticatedActor is CW-0001 Unit 1's kill switch over the
// wire, and CW-0010 Unit 9's administrative authentication feeding it: the audit trail names the
// authenticated principal's subject as the actor, not anything the request itself could claim (the
// wire request carries no actor field at all).
func TestUpdateMessageStateRPCRecordsTheAuthenticatedActor(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	created, err := client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}), "test-editor-key"))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	messageID := created.Msg.GetMessageId()

	if _, err := client.UpdateMessageState(ctx, adminAuthed(connect.NewRequest(&adminv1.UpdateMessageStateRequest{
		MessageId: messageID, NewState: "scheduled",
	}), "test-editor-key")); err != nil {
		t.Fatalf("UpdateMessageState: %v", err)
	}

	audit, err := client.ListAuditLog(ctx, adminAuthed(connect.NewRequest(&adminv1.ListAuditLogRequest{
		MessageId: messageID,
	}), "test-viewer-key"))
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	if len(audit.Msg.GetEntries()) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(audit.Msg.GetEntries()))
	}
	entry := audit.Msg.GetEntries()[0]
	if entry.GetFromState() != "draft" || entry.GetToState() != "scheduled" {
		t.Errorf("entry = %+v, want draft -> scheduled", entry)
	}
	if entry.GetActor() != "test-editor" {
		t.Errorf("Actor = %q, want %q (the authenticated principal's subject)", entry.GetActor(), "test-editor")
	}
}

// TestUpdateMessageStateRPCRecordsTheChangeLog exercises CW-0006 Unit 3's write hook end to end: a
// real kill switch call, over the wire, produces the change log rows internal/delivery/deliver's
// delta computation depends on — an upsert when the message crosses into active, a tombstone when it
// crosses back out — matching admin.go's own reasoning for why UpdateMessageState is the only hook
// this pass needs (CreateMessage always creates in draft, never eligible).
func TestUpdateMessageStateRPCRecordsTheChangeLog(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	created, err := client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}), "test-editor-key"))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	messageID := created.Msg.GetMessageId()

	kindsAfterCreate, err := changeLogKinds(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("changeLogKinds: %v", err)
	}
	if len(kindsAfterCreate) != 0 {
		t.Fatalf("change log kinds after CreateMessage = %v, want none (drafts are never eligible)", kindsAfterCreate)
	}

	if _, err := client.UpdateMessageState(ctx, adminAuthed(connect.NewRequest(&adminv1.UpdateMessageStateRequest{
		MessageId: messageID, NewState: "active",
	}), "test-editor-key")); err != nil {
		t.Fatalf("UpdateMessageState (activate): %v", err)
	}
	kinds, err := changeLogKinds(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("changeLogKinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "upsert" {
		t.Fatalf("change log kinds after activating = %v, want [upsert]", kinds)
	}

	if _, err := client.UpdateMessageState(ctx, adminAuthed(connect.NewRequest(&adminv1.UpdateMessageStateRequest{
		MessageId: messageID, NewState: "paused",
	}), "test-editor-key")); err != nil {
		t.Fatalf("UpdateMessageState (pause): %v", err)
	}
	kinds, err = changeLogKinds(ctx, pool, messageID)
	if err != nil {
		t.Fatalf("changeLogKinds: %v", err)
	}
	if len(kinds) != 2 || kinds[1] != "tombstone" {
		t.Fatalf("change log kinds after pausing = %v, want [upsert tombstone]", kinds)
	}
}

// changeLogKinds returns messageID's delivery_change_log rows' kind values in seq order.
func changeLogKinds(ctx context.Context, pool *pgxpool.Pool, messageID string) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT kind FROM delivery_change_log WHERE message_id = $1 ORDER BY seq`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		kinds = append(kinds, kind)
	}
	return kinds, rows.Err()
}

// TestUpdateMessageStateRPCRejectsABackwardTransition demonstrates FR-MSG-01's forward-only rule
// fails closed over the wire.
func TestUpdateMessageStateRPCRejectsABackwardTransition(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()
	now := time.Now()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	created, err := client.CreateMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: newMessageDefinition(now),
	}), "test-editor-key"))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	_, err = client.UpdateMessageState(ctx, adminAuthed(connect.NewRequest(&adminv1.UpdateMessageStateRequest{
		MessageId: created.Msg.GetMessageId(), NewState: "completed",
	}), "test-editor-key"))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateMessageState (draft -> completed): err = %v, want CodeFailedPrecondition", err)
	}
}

// TestGetMessageRPCRejectsAnUnknownMessage demonstrates the fail-closed contract over the wire.
func TestGetMessageRPCRejectsAnUnknownMessage(t *testing.T) {
	pool := adminTestPool(t)
	ctx := context.Background()

	mux, err := connectserver.NewMux(pool, nil, testTokenSecret, testAdminAuthenticator)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminServiceClient(server.Client(), server.URL)

	_, err = client.GetMessage(ctx, adminAuthed(connect.NewRequest(&adminv1.GetMessageRequest{
		MessageId: "00000000-0000-0000-0000-000000000000",
	}), "test-viewer-key"))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetMessage (unknown): err = %v, want CodeNotFound", err)
	}
}
