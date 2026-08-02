package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// logWireEvent is Event's wire format on the Kafka-compatible log (CW-0010 Unit 5): explicit,
// stable, snake_case field names independent of this file's own Go field names, so a consumer
// outside this module — or someone inspecting the topic directly — can read a record without linking
// against this package, and so a future rename of Event's Go fields cannot silently change the wire
// format already-produced records were written in.
type logWireEvent struct {
	ID                string         `json:"id"`
	ChannelID         string         `json:"channel_id"`
	Kind              string         `json:"kind"`
	Name              string         `json:"name,omitempty"`
	DeviceTime        time.Time      `json:"device_time"`
	ServerTime        time.Time      `json:"server_time"`
	Properties        map[string]any `json:"properties,omitempty"`
	MessageID         string         `json:"message_id,omitempty"`
	VariantID         string         `json:"variant_id,omitempty"`
	SuppressionReason string         `json:"suppression_reason,omitempty"`
}

// EncodeLog serializes e as the log's wire format. e.ServerTime must already be set (the publisher's
// receipt clock), mirroring internal/event/ingest's Postgres publisher, which also stamps it before
// the write rather than leaving it to whatever the caller happened to set.
func (e Event) EncodeLog() ([]byte, error) {
	data, err := json.Marshal(logWireEvent{
		ID:                e.ID,
		ChannelID:         e.ChannelID,
		Kind:              string(e.Kind),
		Name:              e.Name,
		DeviceTime:        e.DeviceTime,
		ServerTime:        e.ServerTime,
		Properties:        e.Properties,
		MessageID:         e.MessageID,
		VariantID:         e.VariantID,
		SuppressionReason: string(e.SuppressionReason),
	})
	if err != nil {
		return nil, fmt.Errorf("model: encode event %s for log: %w", e.ID, err)
	}
	return data, nil
}

// DecodeLogEvent parses data as EncodeLog's format.
func DecodeLogEvent(data []byte) (Event, error) {
	var w logWireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return Event{}, fmt.Errorf("model: decode log event: %w", err)
	}
	return Event{
		ID:                w.ID,
		ChannelID:         w.ChannelID,
		Kind:              Kind(w.Kind),
		Name:              w.Name,
		DeviceTime:        w.DeviceTime,
		ServerTime:        w.ServerTime,
		Properties:        w.Properties,
		MessageID:         w.MessageID,
		VariantID:         w.VariantID,
		SuppressionReason: SuppressionReason(w.SuppressionReason),
	}, nil
}
