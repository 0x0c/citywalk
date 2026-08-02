//go:build integration

// Run with: go test -tags=integration ./internal/delivery/payload/... with
// CITYWALK_TEST_POSTGRES_DSN and CITYWALK_TEST_REDIS_ADDR pointing at scratch instances.
package payload_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/internal/audience/audiencetest"
	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/delivery/payload"
	"github.com/0x0c/citywalk/internal/experiment/assign"
	"github.com/0x0c/citywalk/internal/membership/batch"
	"github.com/0x0c/citywalk/internal/membership/ordinal"
	"github.com/0x0c/citywalk/internal/membership/segment"
)

// insertChannelWithID inserts a channel at a caller-chosen id — CW-0006 Unit 4's tests need to
// control identity directly, to pick two that CW-0008's deterministic assignment sends to different
// variants, which a server-assigned id would not let a test choose.
func insertChannelWithID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, attrs map[string]any) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO channels (id, attributes) VALUES ($1, $2)`, id, attrs); err != nil {
		t.Fatalf("insert channel %s: %v", id, err)
	}
	if _, err := ordinal.Allocate(ctx, pool, id); err != nil {
		t.Fatalf("allocate ordinal for %s: %v", id, err)
	}
}

// findDivergingIdentities searches deterministic, sequential UUID-shaped candidate identities
// (never a live random source, so this test's outcome never depends on an unrepeatable run) for the
// first pair table assigns to two different variants under salt. With an even 50/50 split the search
// converges within a handful of tries in practice; the 200-try ceiling exists only to fail loudly,
// deterministically, and immediately if the split or the hash ever stopped behaving as documented.
func findDivergingIdentities(t *testing.T, table assign.RangeTable, salt string) (a, b string) {
	t.Helper()
	var first, firstVariant string
	for i := 1; i <= 200; i++ {
		candidate := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		result, err := assign.Assign(table, salt, candidate)
		if err != nil {
			t.Fatalf("assign.Assign: %v", err)
		}
		if result.IsHoldout {
			continue
		}
		if first == "" {
			first, firstVariant = candidate, result.Variant
			continue
		}
		if result.Variant != firstVariant {
			return first, candidate
		}
	}
	t.Fatal("found no two candidate identities that diverge in assignment after 200 deterministic tries")
	return "", ""
}

// TestBuildAppliesVariantAssignmentFreshPerChannelFromASharedBundle is CW-0006 Unit 4's central
// claim demonstrated end to end: two channels with identical shared inputs (language, schema major,
// segment membership) share one cached bundle, yet each gets its own CW-0008 assignment computed
// fresh against its own identity — not the first channel's assignment replayed for the second, which
// is exactly the bug a bundle that accidentally cached the per-channel overlay would produce.
func TestBuildAppliesVariantAssignmentFreshPerChannelFromASharedBundle(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

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
		Name: "A/B test", State: model.MessageStateActive,
		Window:      model.Window{Start: now.Add(-time.Hour), End: now.Add(time.Hour)},
		AudienceRef: seg.ID,
		Variants: []model.Variant{
			{Weight: 50, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor}, Content: model.DialogContent{Presentation: model.Presentation{Heading: "A"}}},
			{Weight: 50, Language: "en", SchemaVersion: model.SchemaVersion{Major: model.CurrentMajor}, Content: model.DialogContent{Presentation: model.Presentation{Heading: "B"}}},
		},
	}
	if err := store.InsertMessage(ctx, pool, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	ids := []string{msg.Variants[0].ID, msg.Variants[1].ID}
	sort.Strings(ids)
	table, err := assign.NewRangeTable(ids, map[string]int{ids[0]: 5000, ids[1]: 5000}, 0)
	if err != nil {
		t.Fatalf("NewRangeTable: %v", err)
	}
	channelA, channelB := findDivergingIdentities(t, table, msg.ExperimentSalt)
	insertChannelWithID(t, ctx, pool, channelA, map[string]any{"country": "JP"})
	insertChannelWithID(t, ctx, pool, channelB, map[string]any{"country": "JP"})

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	pA, err := payload.Build(ctx, pool, redisClient, channelA, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (channel A): %v", err)
	}
	pB, err := payload.Build(ctx, pool, redisClient, channelB, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (channel B): %v", err)
	}
	if len(pA.Entries) != 1 || len(pB.Entries) != 1 {
		t.Fatalf("pA.Entries = %+v, pB.Entries = %+v, want one entry each", pA.Entries, pB.Entries)
	}
	if pA.Entries[0].VariantID == pB.Entries[0].VariantID {
		t.Fatalf("channel A and channel B both got variant %s despite being chosen to diverge — the "+
			"per-channel overlay is not running fresh against the shared bundle", pA.Entries[0].VariantID)
	}

	// Channel A's own repeat synchronization — now unambiguously a cache hit on the bundle — is
	// still deterministic and still its own assignment, CW-0008's whole premise applied on top of
	// Unit 4's cache.
	again, err := payload.Build(ctx, pool, redisClient, channelA, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (channel A again): %v", err)
	}
	if len(again.Entries) != 1 || again.Entries[0].VariantID != pA.Entries[0].VariantID {
		t.Errorf("channel A's repeated Build = %+v, want the same variant %s every time", again.Entries, pA.Entries[0].VariantID)
	}
}

// TestInvalidateCampaignDropsOnlyBundlesContainingTheEditedMessage is CW-0006 Unit 4's other central
// claim: a campaign edit invalidates exactly the bundles that contain it, not the whole cache. Two
// channels in two unrelated segments get two bundles under two different cache keys (different
// membership hashes); pausing both messages directly in Postgres but invalidating only one of them
// proves the scope precisely — the invalidated channel's next sync drops the paused message, while
// the other channel's still-cached bundle keeps serving its now-stale (but never invalidated)
// message, showing that bundle was never touched.
func TestInvalidateCampaignDropsOnlyBundlesContainingTheEditedMessage(t *testing.T) {
	pool, redisClient := testDeps(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	env, err := audiencetest.Env()
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	reg := audiencetest.Registry()

	channelJP := insertChannel(t, ctx, pool, map[string]any{"country": "JP"})
	segJP, err := segment.Save(ctx, pool, env, reg, "Japan", `country == "JP"`)
	if err != nil {
		t.Fatalf("segment.Save (JP): %v", err)
	}
	msgJPID := newMessage(t, ctx, pool, "Japan campaign", 1, segJP.ID, now, "JP heading")

	channelUS := insertChannel(t, ctx, pool, map[string]any{"country": "US"})
	segUS, err := segment.Save(ctx, pool, env, reg, "United States", `country == "US"`)
	if err != nil {
		t.Fatalf("segment.Save (US): %v", err)
	}
	msgUSID := newMessage(t, ctx, pool, "US campaign", 1, segUS.ID, now, "US heading")

	if _, err := batch.Recompute(ctx, pool, redisClient, reg); err != nil {
		t.Fatalf("batch.Recompute: %v", err)
	}

	// Populate both bundles.
	pJP, err := payload.Build(ctx, pool, redisClient, channelJP, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (JP, before): %v", err)
	}
	if len(pJP.Entries) != 1 || pJP.Entries[0].MessageID != msgJPID {
		t.Fatalf("Build (JP, before) = %+v, want exactly %s", pJP.Entries, msgJPID)
	}
	pUS, err := payload.Build(ctx, pool, redisClient, channelUS, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (US, before): %v", err)
	}
	if len(pUS.Entries) != 1 || pUS.Entries[0].MessageID != msgUSID {
		t.Fatalf("Build (US, before) = %+v, want exactly %s", pUS.Entries, msgUSID)
	}

	// Pause both messages directly — bypassing internal/platform/connectserver/admin.go's own
	// InvalidateCampaign hook, which is covered separately, since this test is about
	// InvalidateCampaign's own scoping, not about that wiring.
	if err := store.UpdateState(ctx, pool, msgJPID, model.MessageStatePaused, "test-actor", now); err != nil {
		t.Fatalf("UpdateState (JP): %v", err)
	}
	if err := store.UpdateState(ctx, pool, msgUSID, model.MessageStatePaused, "test-actor", now); err != nil {
		t.Fatalf("UpdateState (US): %v", err)
	}

	// Invalidate only the Japan campaign's bundle.
	if err := payload.InvalidateCampaign(ctx, pool, redisClient, msgJPID); err != nil {
		t.Fatalf("InvalidateCampaign: %v", err)
	}

	pJPAfter, err := payload.Build(ctx, pool, redisClient, channelJP, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (JP, after): %v", err)
	}
	if len(pJPAfter.Entries) != 0 {
		t.Errorf("Build (JP, after) = %+v, want none — the invalidated bundle should reflect the pause", pJPAfter.Entries)
	}

	pUSAfter, err := payload.Build(ctx, pool, redisClient, channelUS, "en", now, 0, 15*time.Minute, 0.2)
	if err != nil {
		t.Fatalf("Build (US, after): %v", err)
	}
	if len(pUSAfter.Entries) != 1 || pUSAfter.Entries[0].MessageID != msgUSID {
		t.Errorf("Build (US, after) = %+v, want the still-cached (and here, deliberately stale) %s — "+
			"InvalidateCampaign(msgJPID) must not have touched this unrelated bundle", pUSAfter.Entries, msgUSID)
	}
}
