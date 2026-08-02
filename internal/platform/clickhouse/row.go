package clickhouse

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// RawEvent is one row of events_raw (schema.go). Struct tags name the destination column for
// driver.Batch.AppendStruct, so this struct's fields and RawEventsDDL's column list must stay in
// step; schema_test.go and row_test.go both pin pieces of that pairing.
type RawEvent struct {
	ID                uuid.UUID `ch:"id"`
	ChannelID         uuid.UUID `ch:"channel_id"`
	Kind              string    `ch:"kind"`
	Name              string    `ch:"name"`
	DeviceTime        time.Time `ch:"device_time"`
	ServerTime        time.Time `ch:"server_time"`
	Properties        string    `ch:"properties"`
	MessageID         uuid.UUID `ch:"message_id"`
	VariantID         uuid.UUID `ch:"variant_id"`
	SuppressionReason string    `ch:"suppression_reason"`
}

// LoggedEvent is the shape internal/event/mirror reads one events_log row into before converting it
// to RawEvent: plain strings and a raw JSON payload, matching what a pgx COALESCE(...,"")/::text scan
// off events_log already produces (internal/event/consumer.fetchSince reads the same table the same
// way). Keeping this shape here rather than depending on internal/event/consumer's unexported
// storedEvent means this package's only external dependency is google/uuid, not pgx.
type LoggedEvent struct {
	ID         string
	ChannelID  string
	Kind       string
	Name       string
	DeviceTime time.Time
	ServerTime time.Time
	// Properties is the raw JSON text of events_log.properties, passed through unchanged.
	Properties string
	// MessageID and VariantID are empty when the event carries none — CW-0009 Unit 1 restricts both to
	// the impression family, and internal/event/model.Event represents "none" the same way.
	MessageID         string
	VariantID         string
	SuppressionReason string
}

// ToRawEvent converts e into events_raw's row shape. MessageID and VariantID default to the zero UUID
// (uuid.Nil) when e carries none, per schema.go's ORDER BY comment on why events_raw avoids
// Nullable(UUID) in its sorting key. ID and ChannelID are always required (events_log itself enforces
// this — id is its primary key, channel_id is NOT NULL), so an unparsable value in either is treated
// as a bug worth failing loudly on, not silently substituted.
func (e LoggedEvent) ToRawEvent() (RawEvent, error) {
	id, err := uuid.Parse(e.ID)
	if err != nil {
		return RawEvent{}, fmt.Errorf("clickhouse: parse event id %q: %w", e.ID, err)
	}
	channelID, err := uuid.Parse(e.ChannelID)
	if err != nil {
		return RawEvent{}, fmt.Errorf("clickhouse: parse channel id %q: %w", e.ChannelID, err)
	}
	messageID, err := parseOptionalUUID(e.MessageID)
	if err != nil {
		return RawEvent{}, fmt.Errorf("clickhouse: parse message id %q: %w", e.MessageID, err)
	}
	variantID, err := parseOptionalUUID(e.VariantID)
	if err != nil {
		return RawEvent{}, fmt.Errorf("clickhouse: parse variant id %q: %w", e.VariantID, err)
	}

	return RawEvent{
		ID:                id,
		ChannelID:         channelID,
		Kind:              e.Kind,
		Name:              e.Name,
		DeviceTime:        e.DeviceTime,
		ServerTime:        e.ServerTime,
		Properties:        e.Properties,
		MessageID:         messageID,
		VariantID:         variantID,
		SuppressionReason: e.SuppressionReason,
	}, nil
}

// parseOptionalUUID returns uuid.Nil for an empty string (CW-0009 Unit 1's "this field does not apply
// to this event kind") and otherwise parses s, so a genuinely malformed non-empty value still fails
// loudly rather than being silently coerced to the same sentinel as "absent."
func parseOptionalUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(s)
}
