// Command demo drives every network endpoint the phase-one single process (CW-0010 Unit 11)
// currently exposes, end to end, against a disposable PostgreSQL and Redis: it registers a device
// channel, defines an audience segment, authors and publishes a message, then synchronizes,
// confirms, and reports back on it exactly as a client would. See docs/demo.md for prerequisites and
// how to run it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	adminv1 "github.com/0x0c/citywalk/gen/citywalk/admin/v1"
	"github.com/0x0c/citywalk/gen/citywalk/admin/v1/adminv1connect"
	channelv1 "github.com/0x0c/citywalk/gen/citywalk/channel/v1"
	"github.com/0x0c/citywalk/gen/citywalk/channel/v1/channelv1connect"
	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	eventv1 "github.com/0x0c/citywalk/gen/citywalk/event/v1"
	"github.com/0x0c/citywalk/gen/citywalk/event/v1/eventv1connect"

	"github.com/0x0c/citywalk/internal/audience/predicate"
	"github.com/0x0c/citywalk/internal/audience/registry"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/segment"
	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
	"github.com/0x0c/citywalk/internal/platform/postgres"
	"github.com/0x0c/citywalk/internal/platform/redisclient"
	"github.com/0x0c/citywalk/migrations"
)

// adminAPIKey is the demo's own StaticKeyAuthenticator credential (CW-0010 Unit 9): editor rank
// satisfies both AdminService's editor-required mutations and its viewer-required reads.
const adminAPIKey = "demo-editor-key"

// tokenSigningSecret signs the device access tokens ChannelService.Register issues. Fixed here
// because the demo has no deployment to keep a real secret for — never reuse it outside this
// program.
var tokenSigningSecret = []byte("citywalk-demo-token-signing-secret-do-not-use-elsewhere")

func main() {
	postgresDSN := flag.String("postgres-dsn", "postgres://citywalk:citywalk@localhost:5432/citywalk?sslmode=disable",
		"PostgreSQL connection string (matches deploy/docker-compose.yml)")
	redisAddr := flag.String("redis-addr", "localhost:6379", "Redis address (matches deploy/docker-compose.yml)")
	listenAddr := flag.String("listen-addr", "127.0.0.1:8085", "address the demo's Connect server listens on")
	flag.Parse()

	if err := run(*postgresDSN, *redisAddr, *listenAddr); err != nil {
		log.Fatalf("demo: %v", err)
	}
}

var stepNum int

// step prints a numbered narration line so a reader can follow which network call or setup action
// produced the output beneath it.
func step(format string, args ...any) {
	stepNum++
	fmt.Printf("\n[%d] %s\n", stepNum, fmt.Sprintf(format, args...))
}

// authed attaches token as a bearer credential — the header every AdminService, DeliveryService, and
// EventService call needs (CW-0010 Unit 9), whether the credential is an administrative API key or a
// device access token.
func authed[T any](req *connect.Request[T], token string) *connect.Request[T] {
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

// resetDemoData clears every table and Redis key an earlier run of this program could have left
// behind, in referencing-table-first order, so re-running the demo against a Postgres and Redis that
// still hold a previous run's data reproduces the same single-entry Sync result rather than also
// matching an earlier run's still-active message and still-registered channel. This is a demo-only
// convenience: a real deployment never truncates its own tables on startup.
func resetDemoData(ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client) error {
	tables := []string{
		"conversion_attributions", "message_audit_log", "events_log",
		"segment_membership", "segments", "channel_ordinals", "channels", "messages",
	}
	for _, table := range tables {
		if _, err := pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE membership_generation SET generation = 0`); err != nil {
		return fmt.Errorf("reset membership generation: %w", err)
	}
	if err := redisClient.FlushDB(ctx).Err(); err != nil {
		return fmt.Errorf("flush redis: %w", err)
	}
	return nil
}

func run(postgresDSN, redisAddr, listenAddr string) error {
	ctx := context.Background()

	step("Connecting to PostgreSQL and applying migrations/*.sql")
	pool, err := postgres.NewPool(ctx, postgresDSN)
	if err != nil {
		return fmt.Errorf("connect to postgres at %s (is `docker compose -f deploy/docker-compose.yml up -d` running? see docs/demo.md): %w", postgresDSN, err)
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	step("Connecting to Redis")
	redisClient, err := redisclient.New(ctx, redisAddr)
	if err != nil {
		return fmt.Errorf("connect to redis at %s: %w", redisAddr, err)
	}
	defer func() { _ = redisClient.Close() }()

	step("Clearing data a previous run of this demo left behind, so the walkthrough below is reproducible")
	if err := resetDemoData(ctx, pool, redisClient); err != nil {
		return fmt.Errorf("reset demo data: %w", err)
	}

	adminAuthenticator := adminauth.StaticKeyAuthenticator{
		Keys: map[string]adminauth.Principal{
			adminAPIKey: {Subject: "demo-campaign-author", Role: adminauth.RoleEditor},
		},
	}
	mux, err := connectserver.NewMux(pool, redisClient, tokenSigningSecret, adminAuthenticator)
	if err != nil {
		return fmt.Errorf("build connect mux: %w", err)
	}

	// Cleartext HTTP/2 with an HTTP/1.1 fallback, the same protocol set cmd/server offers the mobile
	// SDK and internal callers (CW-0010 Unit 2): the demo talks to itself the same way any other
	// client would.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Addr: listenAddr, Handler: mux, Protocols: protocols, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	select {
	case err := <-serveErr:
		return fmt.Errorf("start server on %s: %w", listenAddr, err)
	case <-time.After(200 * time.Millisecond):
		// Gave ListenAndServe a moment to fail fast on a bad address; it is otherwise still serving.
	}
	step("Serving every Connect service on http://%s", listenAddr)

	baseURL := "http://" + listenAddr
	httpClient := http.DefaultClient
	channelClient := channelv1connect.NewChannelServiceClient(httpClient, baseURL)
	adminClient := adminv1connect.NewAdminServiceClient(httpClient, baseURL)
	deliveryClient := deliveryv1connect.NewDeliveryServiceClient(httpClient, baseURL)
	eventClient := eventv1connect.NewEventServiceClient(httpClient, baseURL)

	step("Registering a device channel (ChannelService.Register) with attributes {\"country\": \"JP\"}")
	attributesJSON, err := json.Marshal(map[string]string{"country": "JP"})
	if err != nil {
		return fmt.Errorf("encode channel attributes: %w", err)
	}
	registered, err := channelClient.Register(ctx, connect.NewRequest(&channelv1.RegisterRequest{
		AttributesJson:       attributesJSON,
		SupportedSchemaMajor: int32(model.CurrentMajor),
	}))
	if err != nil {
		return fmt.Errorf("register channel: %w", err)
	}
	channelID := registered.Msg.GetChannelId()
	accessToken := registered.Msg.GetAccessToken()
	fmt.Printf("    channel_id = %s\n", channelID)

	// CW-0004's audience predicates and CW-0005's segment membership have no AdminService surface
	// yet — CW-0001 divides the platform into five services, and the administrative API this demo
	// otherwise drives end to end covers only the definition service's own surface so far. Defining
	// a segment and computing its membership is therefore a direct call into the same packages
	// AdminService itself will eventually sit in front of, the path
	// internal/platform/connectserver/delivery_integration_test.go's own Sync test already takes.
	step("Defining the \"Japan travelers\" audience segment (country == \"JP\") and computing membership")
	reg, err := registry.New(registry.Definition{
		Name: "country", Type: registry.TypeString, Source: registry.SourceChannelField,
	})
	if err != nil {
		return fmt.Errorf("build audience registry: %w", err)
	}
	env, err := predicate.BuildEnv(reg)
	if err != nil {
		return fmt.Errorf("build predicate environment: %w", err)
	}
	seg, err := segment.Save(ctx, pool, env, reg, "Japan travelers", `country == "JP"`)
	if err != nil {
		return fmt.Errorf("save segment: %w", err)
	}
	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		return fmt.Errorf("recompute segment membership: %w", err)
	}
	fmt.Printf("    segment_id = %s\n", seg.ID)

	step("Authoring a draft message (AdminService.CreateMessage) targeting that segment")
	now := time.Now()
	variantsJSON := []byte(`[{
		"id": "", "weight": 100, "language": "en",
		"content_column": {
			"schema_version": {"major": 1, "minor": 0},
			"content": {
				"layout": "dialog",
				"heading": "Welcome back to citywalk",
				"body": "You unlocked a new route through Shibuya.",
				"colors": {"background": "#1A1A2E", "text": "#FFFFFF"},
				"corner_radius": 16
			}
		}
	}]`)
	created, err := adminClient.CreateMessage(ctx, authed(connect.NewRequest(&adminv1.CreateMessageRequest{
		Message: &adminv1.MessageDefinition{
			Name:         "Welcome-back dialog",
			WindowStart:  timestamppb.New(now.Add(-time.Hour)),
			WindowEnd:    timestamppb.New(now.Add(time.Hour)),
			AudienceRef:  seg.ID,
			VariantsJson: variantsJSON,
		},
	}), adminAPIKey))
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	messageID := created.Msg.GetMessageId()
	fmt.Printf("    message_id = %s (state: draft)\n", messageID)

	step("Publishing it (AdminService.UpdateMessageState: draft -> active)")
	if _, err := adminClient.UpdateMessageState(ctx, authed(connect.NewRequest(&adminv1.UpdateMessageStateRequest{
		MessageId: messageID, NewState: "active",
	}), adminAPIKey)); err != nil {
		return fmt.Errorf("activate message: %w", err)
	}

	step("Reading the state-transition audit trail back (AdminService.ListAuditLog)")
	auditLog, err := adminClient.ListAuditLog(ctx, authed(connect.NewRequest(&adminv1.ListAuditLogRequest{
		MessageId: messageID,
	}), adminAPIKey))
	if err != nil {
		return fmt.Errorf("list audit log: %w", err)
	}
	for _, entry := range auditLog.Msg.GetEntries() {
		fmt.Printf("    %s -> %s, by %s, at %s\n",
			entry.GetFromState(), entry.GetToState(), entry.GetActor(), entry.GetOccurredAt().AsTime().Format(time.RFC3339))
	}

	step("Synchronizing the channel's payload (DeliveryService.Sync)")
	firstSync, err := deliveryClient.Sync(ctx, authed(connect.NewRequest(&deliveryv1.SyncRequest{
		ChannelId: channelID, Language: "en",
	}), accessToken))
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	entries := firstSync.Msg.GetEntries()
	if len(entries) != 1 {
		return fmt.Errorf("sync returned %d entries, want 1 — the channel may not have been recomputed into segment %s yet", len(entries), seg.ID)
	}
	entry := entries[0]
	fmt.Printf("    etag = %s\n    entries[0].content = %s\n", firstSync.Msg.GetEtag(), entry.GetContent())

	step("Synchronizing again with that etag (DeliveryService.Sync): the payload has not changed")
	secondSync, err := deliveryClient.Sync(ctx, authed(connect.NewRequest(&deliveryv1.SyncRequest{
		ChannelId: channelID, Language: "en", Etag: firstSync.Msg.GetEtag(),
	}), accessToken))
	if err != nil {
		return fmt.Errorf("sync (second): %w", err)
	}
	fmt.Printf("    unchanged = %v, entries = %d\n", secondSync.Msg.GetUnchanged(), len(secondSync.Msg.GetEntries()))

	step("Confirming the message may still display right now (DeliveryService.Confirm)")
	confirmed, err := deliveryClient.Confirm(ctx, authed(connect.NewRequest(&deliveryv1.ConfirmRequest{
		ChannelId: channelID, MessageId: messageID,
	}), accessToken))
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	fmt.Printf("    approved = %v\n", confirmed.Msg.GetApproved())

	step("Submitting an impression event for the delivered variant (EventService.Submit)")
	submitted, err := eventClient.Submit(ctx, authed(connect.NewRequest(&eventv1.SubmitRequest{
		ChannelId: channelID,
		Events: []*eventv1.Event{{
			Id:         uuid.NewString(),
			Kind:       "impression",
			DeviceTime: timestamppb.New(now),
			MessageId:  entry.GetMessageId(),
			VariantId:  entry.GetVariantId(),
		}},
	}), accessToken))
	if err != nil {
		return fmt.Errorf("submit event: %w", err)
	}
	fmt.Printf("    accepted = %d, rejected_invalid = %d, rejected_rate_limited = %d\n",
		submitted.Msg.GetAccepted(), submitted.Msg.GetRejectedInvalid(), submitted.Msg.GetRejectedRateLimited())

	fmt.Println("\nEvery call above went over the same Connect API a real client speaks. The server keeps")
	fmt.Printf("running at http://%s — try it yourself, for example:\n\n", listenAddr)
	fmt.Printf("  curl -s http://%s/citywalk.delivery.v1.DeliveryService/Sync \\\n", listenAddr)
	fmt.Println(`    -H 'Content-Type: application/json' \`)
	fmt.Printf("    -H 'Authorization: Bearer %s' \\\n", accessToken)
	fmt.Printf("    -d '{\"channel_id\": \"%s\", \"language\": \"en\"}'\n", channelID)
	fmt.Println("\nPress Ctrl+C to stop.")

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-sigCtx.Done():
		fmt.Println("\nShutting down.")
		return nil
	case err := <-serveErr:
		return fmt.Errorf("server error: %w", err)
	}
}
