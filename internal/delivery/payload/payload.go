// Package payload implements CW-0002 Units 1 through 3: assembling the payload a device receives
// from the definitions it is eligible for, per the boundary rule (audience data never crosses into
// the payload) and the payload contract (complete, self-expiring, size-capped). It also implements
// CW-0006 Unit 6's size ceiling and cohort-labeled truncation metric — CW-0002's payload contract
// and CW-0006's transport protocol share this one assembly path rather than each keeping its own.
package payload

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/0x0c/citywalk/internal/definition/model"
	"github.com/0x0c/citywalk/internal/definition/store"
	deliverysync "github.com/0x0c/citywalk/internal/delivery/sync"
	"github.com/0x0c/citywalk/internal/event/ingest"
	eventmodel "github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/experiment/assign"
	"github.com/0x0c/citywalk/internal/membership/reverse"
)

// Entry is one eligible message, projected onto exactly the fields CW-0002 Unit 2 allows across the
// boundary: no audience predicate, no segment identifier, no attribute value, and no other user's
// data. Content is the same wire document CW-0003's Variant.MarshalContentColumn produces, so a
// device's schema_version handling (CW-0003 Unit 4) applies unchanged.
type Entry struct {
	MessageID         string
	Version           int
	VariantID         string
	SchemaVersion     model.SchemaVersion
	Priority          int
	Content           []byte
	Triggers          []model.Trigger
	DisplayConditions []model.DisplayCondition
	ControlPolicy     model.ControlPolicy
	ExpiresAt         time.Time
}

// Payload is the complete, self-expiring answer to "what is this channel eligible for right now" —
// CW-0002 Unit 2's contract.
type Payload struct {
	Entries    []Entry
	NextSyncAt time.Time
	// ProjectBudgetRemaining is CW-0007 Unit 5's issuance: the estimated remaining project-wide
	// impression budget as of this assembly, for the device to spend locally. nil means the caller
	// (deliver.Sync) has no budget configured for this synchronization — distinct from a real zero,
	// which means the budget is fully spent. Build itself never sets this; it is out of payload
	// assembly's own scope (CW-0002/CW-0006) and is filled in by whichever caller has a budget
	// configuration to apply.
	ProjectBudgetRemaining *int
}

// truncationCounter records CW-0010 Unit 10's named payload-truncation-count metric: a truncation
// that goes unrecorded looks exactly like a campaign nobody qualified for.
var truncationCounter = mustCounter()

func mustCounter() metric.Int64Counter {
	c, err := otel.Meter("citywalk/delivery/payload").Int64Counter(
		"citywalk.delivery.payload_truncation_count",
		metric.WithDescription("Count of payload entries dropped for exceeding the size ceiling"),
	)
	if err != nil {
		// Int64Counter only fails on a malformed instrument name, which is fixed at compile time —
		// a real failure here would mean this package itself is broken, not a runtime condition.
		panic(err)
	}
	return c
}

// Build assembles channelID's payload: every active, in-window message whose audience segment
// appears in the channel's reverse-index bitmap (CW-0005 Unit 3), truncated to sizeCeilingBytes in
// priority order, with the next synchronization time CW-0002 Unit 3 requires.
func Build(
	ctx context.Context, pool *pgxpool.Pool, redisClient *redis.Client,
	channelID, language string, now time.Time, sizeCeilingBytes int, syncInterval time.Duration, syncJitterFraction float64,
) (Payload, error) {
	nextSync := deliverysync.NextSyncAt(now, syncInterval, syncJitterFraction)

	segmentBM, err := reverse.Get(ctx, redisClient, channelID)
	if err != nil {
		return Payload{}, fmt.Errorf("payload: read reverse index: %w", err)
	}
	if segmentBM.IsEmpty() {
		return Payload{NextSyncAt: nextSync}, nil
	}

	ordinals := make([]int64, 0, segmentBM.GetCardinality())
	it := segmentBM.Iterator()
	for it.HasNext() {
		ordinals = append(ordinals, int64(it.Next()))
	}

	segmentIDs, err := segmentIDsForOrdinals(ctx, pool, ordinals)
	if err != nil {
		return Payload{}, err
	}
	if len(segmentIDs) == 0 {
		return Payload{NextSyncAt: nextSync}, nil
	}

	messageIDs, err := eligibleMessageIDs(ctx, pool, segmentIDs, now)
	if err != nil {
		return Payload{}, err
	}

	declaredMajor, err := supportedSchemaMajor(ctx, pool, channelID)
	if err != nil {
		return Payload{}, err
	}

	var entries []Entry
	for _, id := range messageIDs {
		msg, err := store.GetMessage(ctx, pool, id)
		if err != nil {
			return Payload{}, fmt.Errorf("payload: load message %s: %w", id, err)
		}
		entry, included, err := buildEntry(ctx, pool, msg, language, channelID, declaredMajor, now)
		if err != nil {
			return Payload{}, fmt.Errorf("payload: build entry for message %s: %w", id, err)
		}
		if !included {
			// Either channelID landed in msg's own holdout (CW-0008 Unit 4), or msg carries no variant
			// whose schema major the channel declared support for at registration (CW-0003 Unit 4).
			// Both leave the channel eligible in every other respect but receiving no content, exactly
			// like a channel that never qualified at all.
			continue
		}
		entries = append(entries, entry)
	}

	entries, truncated := truncate(entries, sizeCeilingBytes)
	if truncated > 0 {
		// Labeled by language — the cohort dimension Build actually has on hand — per CW-0006 Unit
		// 6: the metric must name the cohort, not just the count, so a project that has quietly
		// outgrown the ceiling for one locale doesn't hide behind an aggregate that looks fine.
		truncationCounter.Add(ctx, int64(truncated), metric.WithAttributes(
			attribute.String("citywalk.delivery.language", language),
		))
	}

	return Payload{Entries: entries, NextSyncAt: nextSync}, nil
}

// buildEntry projects msg onto the device-safe Entry shape, first narrowing to the variants whose
// schema major the channel declared support for at registration (CW-0003 Unit 4: "each device
// receives the highest version its SDK declares support for"), then selecting among those by
// language and finally by CW-0008's deterministic experiment assignment — CW-0003 Unit 1's full
// selection rule. included is false either when msg carries no variant compatible with
// declaredMajor at all, or when identity landed in msg's own holdout (CW-0008 Unit 4): eligible in
// every respect, but the caller must not include an entry for it. The holdout case is the only one
// CW-0008 Unit 5 requires a record for — a schema-major mismatch is a compatibility gap the SDK, not
// the server, is positioned to report (docs/requirements.md places SDK behavior out of this
// repository's scope) — so buildEntry emits a KindHoldoutQualified event only for that case. When no
// compatible variant matches language, buildEntry falls back to the first compatible variant with no
// assignment and no holdout — the same safety net this path has always had for a campaign with no
// content in the device's language.
func buildEntry(ctx context.Context, pool *pgxpool.Pool, msg model.Message, language, identity string, declaredMajor int, now time.Time) (Entry, bool, error) {
	if len(msg.Variants) == 0 {
		return Entry{}, false, fmt.Errorf("message has no variants")
	}

	compatibleVariants := variantsSupportingMajor(msg.Variants, declaredMajor)
	if len(compatibleVariants) == 0 {
		return Entry{}, false, nil
	}

	variant := compatibleVariants[0]
	if languageVariants := variantsForLanguage(compatibleVariants, language); len(languageVariants) > 0 {
		selected, isHoldout, err := assignVariant(msg, languageVariants, identity)
		if err != nil {
			return Entry{}, false, err
		}
		if isHoldout {
			if err := recordHoldoutQualified(ctx, pool, msg, identity, now); err != nil {
				return Entry{}, false, err
			}
			return Entry{}, false, nil
		}
		variant = selected
	}

	content, err := variant.MarshalContentColumn()
	if err != nil {
		return Entry{}, false, fmt.Errorf("encode variant content: %w", err)
	}

	return Entry{
		MessageID:         msg.ID,
		Version:           msg.Version,
		VariantID:         variant.ID,
		SchemaVersion:     variant.SchemaVersion,
		Priority:          msg.Priority,
		Content:           content,
		Triggers:          msg.Triggers,
		DisplayConditions: msg.DisplayConditions,
		ControlPolicy:     msg.ControlPolicy,
		ExpiresAt:         msg.Window.End,
	}, true, nil
}

// recordHoldoutQualified emits CW-0008 Unit 5's counterfactual record: identity qualified for msg but
// was withheld, so the comparison a holdout exists for has a denominator. Both identity (as
// ChannelID) and msg.ID are already known to reference live rows by the time buildEntry runs, so a
// failure here — unlike the exclusion itself — is a real error rather than something to swallow.
func recordHoldoutQualified(ctx context.Context, pool *pgxpool.Pool, msg model.Message, identity string, now time.Time) error {
	event := eventmodel.Event{
		ID:         uuid.NewString(),
		ChannelID:  identity,
		Kind:       eventmodel.KindHoldoutQualified,
		DeviceTime: now,
		MessageID:  msg.ID,
	}
	if err := ingest.Record(ctx, pool, event, now); err != nil {
		return fmt.Errorf("payload: record holdout qualified for message %s: %w", msg.ID, err)
	}
	return nil
}

// variantsSupportingMajor returns every variant in variants whose SchemaVersion.SupportsMajor
// reports true for declaredMajor (CW-0003 Unit 4).
func variantsSupportingMajor(variants []model.Variant, declaredMajor int) []model.Variant {
	var out []model.Variant
	for _, v := range variants {
		if v.SchemaVersion.SupportsMajor(declaredMajor) {
			out = append(out, v)
		}
	}
	return out
}

// supportedSchemaMajor reads back the schema major channelID declared support for at registration
// (CW-0010 Unit 9's Register, CW-0003 Unit 4).
func supportedSchemaMajor(ctx context.Context, pool *pgxpool.Pool, channelID string) (int, error) {
	var major int
	if err := pool.QueryRow(ctx, `SELECT supported_schema_major FROM channels WHERE id = $1`, channelID).Scan(&major); err != nil {
		return 0, fmt.Errorf("payload: read supported_schema_major for channel %s: %w", channelID, err)
	}
	return major, nil
}

// variantsForLanguage returns every variant in variants matching language, in no particular order.
func variantsForLanguage(variants []model.Variant, language string) []model.Variant {
	var out []model.Variant
	for _, v := range variants {
		if v.Language == language {
			out = append(out, v)
		}
	}
	return out
}

// assignVariant runs CW-0008 Units 2 through 4 over languageVariants: a range table sized to
// msg.HoldoutFraction's reserved holdout and each variant's percentage weight (already validated to
// sum to 100 within a language group, CW-0003 Unit 5), looked up under msg.ExperimentSalt and
// identity. identity is CW-0008 Unit 1's stable identity; this schema has no user-account linkage
// yet, so it is always the channel identifier, never a user identifier, until that linkage exists.
func assignVariant(msg model.Message, languageVariants []model.Variant, identity string) (model.Variant, bool, error) {
	ids := make([]string, len(languageVariants))
	byID := make(map[string]model.Variant, len(languageVariants))
	weightPercent := make(map[string]int, len(languageVariants))
	for i, v := range languageVariants {
		ids[i] = v.ID
		byID[v.ID] = v
		weightPercent[v.ID] = v.Weight
	}
	// Sorted independently of the order GetMessage's query happened to return: CW-0008's whole
	// premise is that the same inputs produce the same table anywhere, and "the order the database
	// returned rows in" is not a stable input.
	sort.Strings(ids)

	holdoutBuckets := int(math.Round(msg.HoldoutFraction * float64(assign.BucketCount)))
	nonHoldoutBuckets := assign.BucketCount - holdoutBuckets
	bucketWeights := allocateExperimentBuckets(ids, weightPercent, nonHoldoutBuckets)

	table, err := assign.NewRangeTable(ids, bucketWeights, holdoutBuckets)
	if err != nil {
		return model.Variant{}, false, fmt.Errorf("build range table for message %s: %w", msg.ID, err)
	}
	result, err := assign.Assign(table, msg.ExperimentSalt, identity)
	if err != nil {
		return model.Variant{}, false, fmt.Errorf("assign variant for message %s: %w", msg.ID, err)
	}
	if result.IsHoldout {
		return model.Variant{}, true, nil
	}
	return byID[result.Variant], false, nil
}

// allocateExperimentBuckets converts each variant's percentage weight into a bucket count summing to
// exactly totalBuckets, using the largest-remainder method: floor each variant's proportional share,
// then hand the leftover buckets to the variants with the largest fractional remainder, breaking ties
// by variant ID. Determinism here is not a nicety but CW-0008's entire premise — the same inputs must
// produce the same table on any server — which is why ties break on the variant ID rather than
// anything about evaluation order.
func allocateExperimentBuckets(ids []string, weightPercent map[string]int, totalBuckets int) map[string]int {
	type share struct {
		id        string
		floor     int
		remainder float64
	}
	shares := make([]share, len(ids))
	flooredSum := 0
	for i, id := range ids {
		exact := float64(weightPercent[id]) * float64(totalBuckets) / 100
		floor := int(exact)
		shares[i] = share{id: id, floor: floor, remainder: exact - float64(floor)}
		flooredSum += floor
	}
	leftover := totalBuckets - flooredSum

	sort.SliceStable(shares, func(i, j int) bool {
		if shares[i].remainder != shares[j].remainder {
			return shares[i].remainder > shares[j].remainder
		}
		return shares[i].id < shares[j].id
	})

	buckets := make(map[string]int, len(ids))
	for i, s := range shares {
		buckets[s.id] = s.floor
		if i < leftover {
			buckets[s.id]++
		}
	}
	return buckets
}

// truncate keeps entries (already priority-ordered by the eligibleMessageIDs query) while their
// cumulative Content size stays within sizeCeilingBytes, and reports how many were dropped —
// CW-0002 Unit 2's size ceiling, truncated in priority order rather than silently.
func truncate(entries []Entry, sizeCeilingBytes int) ([]Entry, int) {
	if sizeCeilingBytes <= 0 {
		return entries, 0
	}
	total := 0
	for i, e := range entries {
		total += len(e.Content)
		if total > sizeCeilingBytes {
			return entries[:i], len(entries) - i
		}
	}
	return entries, 0
}

func segmentIDsForOrdinals(ctx context.Context, pool *pgxpool.Pool, ordinals []int64) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT id FROM segments WHERE ordinal = ANY($1)`, ordinals)
	if err != nil {
		return nil, fmt.Errorf("payload: resolve segment ordinals: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("payload: scan segment id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payload: read segment ids: %w", err)
	}
	return ids, nil
}

// eligibleMessageIDs returns, in priority order (highest first), every active message whose window
// covers now and whose audience_ref names one of segmentIDs — CW-0002 Unit 1's boundary rule
// realized as a query: the predicate that decided eligibility already ran (CW-0005's batch or
// incremental maintenance), so this is a plain state and window filter, nothing more.
func eligibleMessageIDs(ctx context.Context, pool *pgxpool.Pool, segmentIDs []string, now time.Time) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT id FROM messages
		WHERE state = 'active' AND window_start <= $1 AND window_end > $1 AND audience_ref = ANY($2)
		ORDER BY priority DESC, id
	`, now, segmentIDs)
	if err != nil {
		return nil, fmt.Errorf("payload: query eligible messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("payload: scan eligible message: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("payload: read eligible messages: %w", err)
	}
	return ids, nil
}
