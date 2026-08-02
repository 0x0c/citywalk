// CW-0006 Unit 4: the two-tier assembly cache. Build's eligibility-and-content computation splits
// into a shared half — which messages are eligible, and what their content is, which depends only on
// segment membership, locale, and the channel's declared schema major — and a per-channel overlay —
// CW-0008's variant assignment and holdout, which depends on the requesting channel's own identity.
// This file caches the shared half as a bundle, keyed by the inputs that actually drive it, so a
// distinct combination of those inputs is computed once in Redis rather than once per request.
package payload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	"github.com/0x0c/citywalk/internal/membership/reverse"
)

// bundleVariant is the shared, cacheable half of a model.Variant: its content already marshaled once
// (MarshalContentColumn is deterministic and identity-independent, so doing it at bundle-assembly
// time rather than per request is itself part of Unit 4's saving), plus everything CW-0008's
// assignment needs to consider it a candidate.
type bundleVariant struct {
	ID            string
	Weight        int
	Language      string
	SchemaVersion model.SchemaVersion
	Content       []byte
}

// bundleMessage is one eligible message's shared assembly result: its schema-major-compatible
// variants already resolved and narrowed to language, everything else about it copied over, and
// nothing here computed from or scoped to any one channel.
type bundleMessage struct {
	MessageID         string
	Version           int
	Priority          int
	ExpiresAt         time.Time
	ControlPolicy     model.ControlPolicy
	Triggers          []model.Trigger
	DisplayConditions []model.DisplayCondition
	HoldoutFraction   float64
	ExperimentSalt    string
	// FallbackVariant is the first schema-major-compatible variant (CW-0003 Unit 1's language-
	// fallback safety net): served as-is, with no assignment and no holdout, whenever
	// LanguageVariants is empty — the same behavior the pre-Unit-4 buildEntry always gave a campaign
	// with no content in a channel's language.
	FallbackVariant bundleVariant
	// LanguageVariants is FallbackVariant's compatible-variant set narrowed to language. CW-0008's
	// per-channel assignment (applyOverlay) runs over exactly this set when it is non-empty.
	LanguageVariants []bundleVariant
}

// bundle is CW-0006 Unit 4's shared half of a payload: every message a channel with a given
// (language, declared schema major, segment membership) combination is eligible for, with content
// already resolved to the point where only CW-0008's per-channel step remains.
type bundle struct {
	Messages []bundleMessage
}

const (
	bundleKeyPrefix             = "delivery:bundle:"
	bundleSegmentIndexKeyPrefix = "delivery:bundle:segment-index:"
)

// bundleKey names the Redis key a (language, declaredMajor, membershipHash) combination caches under.
func bundleKey(language string, declaredMajor int, membershipHash string) string {
	return fmt.Sprintf("%s%s:%d:%s", bundleKeyPrefix, language, declaredMajor, membershipHash)
}

// bundleSegmentIndexKey names segmentID's secondary index: the set of bundle keys assembled from a
// segmentIDs list that included segmentID, which InvalidateCampaign reads to find every bundle a
// campaign edit needs to drop.
//
// Keyed by segment rather than by message id — the choice this unit's own instructions call out as
// a real design decision. A campaign's audience_ref is the one thing that determines which bundles it
// could ever affect (assembleShared's own eligibility query is "every message whose audience_ref is
// in segmentIDs"), and indexing by segment is also the only choice that covers a campaign transitioning
// into eligibility for the first time: a message-id-keyed index (the alternative the task's own
// prompt suggests) can only ever record bundles a message already appears in, so a campaign the kill
// switch has just activated — never in any bundle before that moment — would have no index entry to
// invalidate under, and every bundle already warm for its audience segment would keep serving the
// stale "not yet eligible" answer forever, since these bundles carry no expiry. Indexing by segment
// instead means a bundle is discoverable by every campaign that could ever become relevant to it, not
// only the ones it happens to already contain.
func bundleSegmentIndexKey(segmentID string) string {
	return bundleSegmentIndexKeyPrefix + segmentID
}

// sharedBundleFor returns the shared bundle CW-0006 Unit 4 caches for a channel whose segment
// membership is segmentBM, narrowed to language and declaredMajor. The cache key is derived from
// segmentBM's own hash (internal/membership/reverse.Hash) rather than the channel's identity, so
// every channel with the same three inputs shares one Redis lookup instead of one assembly each.
func sharedBundleFor(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	segmentBM *roaring.Bitmap, language string, declaredMajor int, now time.Time,
) (bundle, error) {
	membershipHash, err := reverse.Hash(segmentBM)
	if err != nil {
		return bundle{}, fmt.Errorf("payload: hash membership: %w", err)
	}
	key := bundleKey(language, declaredMajor, membershipHash)

	cached, hit, err := getCachedBundle(ctx, redisClient, key)
	if err != nil {
		return bundle{}, err
	}
	if hit {
		return cached, nil
	}

	ordinals := make([]int64, 0, segmentBM.GetCardinality())
	it := segmentBM.Iterator()
	for it.HasNext() {
		ordinals = append(ordinals, int64(it.Next()))
	}
	segmentIDs, err := segmentIDsForOrdinals(ctx, pool, ordinals)
	if err != nil {
		return bundle{}, err
	}

	b, err := assembleShared(ctx, pool, segmentIDs, language, declaredMajor, now)
	if err != nil {
		return bundle{}, err
	}
	if err := setCachedBundle(ctx, redisClient, key, segmentIDs, b); err != nil {
		return bundle{}, err
	}
	return b, nil
}

// assembleShared computes the shared bundle from scratch: every active, in-window message whose
// audience segment is in segmentIDs (CW-0002 Unit 1's boundary rule, the same query Build always
// ran), narrowed per message to declaredMajor- and language-compatible variants. Nothing here reads
// or depends on any channel's own identity, which is what makes the result safe to cache and reuse
// across every channel sharing segmentIDs, language, and declaredMajor.
func assembleShared(ctx context.Context, pool *pgxpool.Pool, segmentIDs []string, language string, declaredMajor int, now time.Time) (bundle, error) {
	if len(segmentIDs) == 0 {
		return bundle{}, nil
	}
	messageIDs, err := eligibleMessageIDs(ctx, pool, segmentIDs, now)
	if err != nil {
		return bundle{}, err
	}

	var messages []bundleMessage
	for _, id := range messageIDs {
		msg, err := store.GetMessage(ctx, pool, id)
		if err != nil {
			return bundle{}, fmt.Errorf("payload: load message %s: %w", id, err)
		}
		bm, included, err := toBundleMessage(ctx, msg, language, declaredMajor)
		if err != nil {
			return bundle{}, fmt.Errorf("payload: build bundle entry for message %s: %w", id, err)
		}
		if !included {
			// msg carries no variant whose schema major declaredMajor supports (CW-0003 Unit 4): an
			// SDK compatibility gap that holds for every channel sharing declaredMajor, not something
			// any one channel's identity could change, so it is excluded from the bundle itself
			// rather than left for applyOverlay to discover per request.
			continue
		}
		messages = append(messages, bm)
	}
	return bundle{Messages: messages}, nil
}

// toBundleMessage is the shared half of the pre-Unit-4 buildEntry's selection rule (CW-0003 Unit 1):
// narrow to the variants whose schema major declaredMajor supports (CW-0003 Unit 4), then to those
// matching language. included is false only when msg carries no variant compatible with declaredMajor
// at all — CW-0010 Unit 10's schema-version-skip metric, so a compatibility gap that holds for every
// channel sharing declaredMajor is visible rather than looking like a segment nobody qualified for.
func toBundleMessage(ctx context.Context, msg model.Message, language string, declaredMajor int) (bundleMessage, bool, error) {
	if len(msg.Variants) == 0 {
		return bundleMessage{}, false, fmt.Errorf("message has no variants")
	}
	compatibleVariants := variantsSupportingMajor(msg.Variants, declaredMajor)
	if len(compatibleVariants) == 0 {
		schemaVersionSkipCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.Int("citywalk.delivery.declared_schema_major", declaredMajor),
		))
		return bundleMessage{}, false, nil
	}

	fallback, err := toBundleVariant(compatibleVariants[0])
	if err != nil {
		return bundleMessage{}, false, err
	}
	var languageVariants []bundleVariant
	for _, v := range variantsForLanguage(compatibleVariants, language) {
		bv, err := toBundleVariant(v)
		if err != nil {
			return bundleMessage{}, false, err
		}
		languageVariants = append(languageVariants, bv)
	}

	return bundleMessage{
		MessageID: msg.ID, Version: msg.Version, Priority: msg.Priority, ExpiresAt: msg.Window.End,
		ControlPolicy: msg.ControlPolicy, Triggers: msg.Triggers, DisplayConditions: msg.DisplayConditions,
		HoldoutFraction: msg.HoldoutFraction, ExperimentSalt: msg.ExperimentSalt,
		FallbackVariant: fallback, LanguageVariants: languageVariants,
	}, true, nil
}

func toBundleVariant(v model.Variant) (bundleVariant, error) {
	content, err := v.MarshalContentColumn()
	if err != nil {
		return bundleVariant{}, fmt.Errorf("encode variant content: %w", err)
	}
	return bundleVariant{
		ID: v.ID, Weight: v.Weight, Language: v.Language, SchemaVersion: v.SchemaVersion, Content: content,
	}, nil
}

// applyOverlay is CW-0006 Unit 4's per-channel half, run fresh on every request regardless of whether
// bm came from a cache hit or a cache miss: CW-0008's deterministic variant assignment among
// bm.LanguageVariants, and the holdout exclusion and event that comes with it. included is false when
// identity landed in bm's own holdout — eligible in every respect, but no entry — mirroring the
// pre-Unit-4 buildEntry's contract exactly, since this is that same per-channel logic, only now fed
// from a bundle instead of a freshly loaded model.Message.
func applyOverlay(ctx context.Context, pool *pgxpool.Pool, bm bundleMessage, identity string, now time.Time) (Entry, bool, error) {
	variant := bm.FallbackVariant
	if len(bm.LanguageVariants) > 0 {
		selected, isHoldout, err := assignVariant(bm.asModelMessage(), toModelVariants(bm.LanguageVariants), identity)
		if err != nil {
			return Entry{}, false, err
		}
		if isHoldout {
			if err := recordHoldoutQualified(ctx, pool, bm.asModelMessage(), identity, now); err != nil {
				return Entry{}, false, err
			}
			return Entry{}, false, nil
		}
		found, ok := findBundleVariant(bm.LanguageVariants, selected.ID)
		if !ok {
			// assignVariant can only return an ID it was given (byID[result.Variant], built from
			// exactly bm.LanguageVariants); this would mean assignVariant's own contract broke, not
			// a condition this function can recover from.
			return Entry{}, false, fmt.Errorf("payload: assigned variant %s not found in message %s's bundle", selected.ID, bm.MessageID)
		}
		variant = found
	}

	return Entry{
		MessageID:         bm.MessageID,
		Version:           bm.Version,
		VariantID:         variant.ID,
		SchemaVersion:     variant.SchemaVersion,
		Priority:          bm.Priority,
		Content:           variant.Content,
		Triggers:          bm.Triggers,
		DisplayConditions: bm.DisplayConditions,
		ControlPolicy:     bm.ControlPolicy,
		ExpiresAt:         bm.ExpiresAt,
	}, true, nil
}

// asModelMessage reconstructs exactly the fields assignVariant and recordHoldoutQualified read from a
// model.Message (ID, HoldoutFraction, ExperimentSalt) — never the whole row, which the bundle does not
// carry and neither function needs.
func (bm bundleMessage) asModelMessage() model.Message {
	return model.Message{ID: bm.MessageID, HoldoutFraction: bm.HoldoutFraction, ExperimentSalt: bm.ExperimentSalt}
}

func toModelVariants(variants []bundleVariant) []model.Variant {
	out := make([]model.Variant, len(variants))
	for i, v := range variants {
		out[i] = model.Variant{ID: v.ID, Weight: v.Weight, Language: v.Language, SchemaVersion: v.SchemaVersion}
	}
	return out
}

func findBundleVariant(variants []bundleVariant, id string) (bundleVariant, bool) {
	for _, v := range variants {
		if v.ID == id {
			return v, true
		}
	}
	return bundleVariant{}, false
}

// getCachedBundle reads key's bundle back, reporting hit=false (never an error) on a cache miss.
func getCachedBundle(ctx context.Context, redisClient *redis.Client, key string) (bundle, bool, error) {
	data, err := redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return bundle{}, false, nil
		}
		return bundle{}, false, fmt.Errorf("payload: read cached bundle: %w", err)
	}
	var b bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return bundle{}, false, fmt.Errorf("payload: decode cached bundle: %w", err)
	}
	return b, true, nil
}

// setCachedBundle writes b at key with no expiry — CW-0006 Unit 4's own text: "a bundle is
// invalidated by campaign edits, not by time" — the same choice
// internal/membership/reverse.Set makes for the reverse index, whose correctness is likewise
// maintained entirely by explicit writes rather than a clock. It then indexes key under every segment
// in segmentIDs (the full eligibility query this bundle was assembled from, not just the segments its
// resulting messages happen to carry — see bundleSegmentIndexKey's own doc comment for why), so
// InvalidateCampaign can find it again.
func setCachedBundle(ctx context.Context, redisClient *redis.Client, key string, segmentIDs []string, b bundle) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("payload: encode bundle: %w", err)
	}
	if err := redisClient.Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("payload: cache bundle: %w", err)
	}
	if len(segmentIDs) == 0 {
		return nil
	}
	if _, err := redisClient.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, segmentID := range segmentIDs {
			pipe.SAdd(ctx, bundleSegmentIndexKey(segmentID), key)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("payload: index bundle: %w", err)
	}
	return nil
}

// InvalidateCampaign drops every cached bundle that could contain messageID — CW-0006 Unit 4's
// campaign-keyed invalidation. Call it wherever a campaign edit changes what a bundle for its
// audience could compute; today that is internal/platform/connectserver/admin.go's
// UpdateMessageState, the same trigger point CW-0006 Unit 3's change log uses, since it is the only
// mutation that can change a message's eligibility or content in this schema.
//
// Design choice — the secondary index (bundleSegmentIndexKey) is read here but never deleted, only
// the bundle keys it names are: deleting the index set itself would race a concurrent cache-miss
// recomputation that is mid-SADD into the same key. A DEL landing between that recomputation's own
// SADD and this function's read would silently drop the fresh bundle's index entry, leaving that one
// bundle permanently unable to be found by a future invalidation for this segment — a correctness
// bug, not just a missed optimization. Leaving the index set in place instead means a concurrent SADD
// always lands in a key that still exists, so it is never lost; the cost is that the index can
// accumulate entries for bundle keys a prior invalidation already deleted (Redis's own DEL is a
// no-op on a key that is not there), which is bounded by how many distinct (language, declaredMajor,
// membershipHash) combinations have ever queried against this one segment — small in practice, and
// not addressed by a periodic sweep in this pass, matching the "no new infrastructure" scope this
// unit was given.
func InvalidateCampaign(ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client, messageID string) error {
	var audienceRef *string
	if err := pool.QueryRow(ctx, `SELECT audience_ref FROM messages WHERE id = $1`, messageID).Scan(&audienceRef); err != nil {
		return fmt.Errorf("payload: read audience_ref for message %s: %w", messageID, err)
	}
	if audienceRef == nil {
		// No audience means no segment could ever make this message eligible for any channel
		// (assembleShared's own query never matches a NULL audience_ref), so no bundle could ever
		// depend on it.
		return nil
	}

	indexKey := bundleSegmentIndexKey(*audienceRef)
	keys, err := redisClient.SMembers(ctx, indexKey).Result()
	if err != nil {
		return fmt.Errorf("payload: read bundle index for segment %s: %w", *audienceRef, err)
	}
	if len(keys) == 0 {
		return nil
	}
	if err := redisClient.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("payload: invalidate bundles for segment %s: %w", *audienceRef, err)
	}
	return nil
}
