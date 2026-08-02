package clickhouse_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0x0c/citywalk/internal/platform/clickhouse"
)

func validLoggedEvent() clickhouse.LoggedEvent {
	return clickhouse.LoggedEvent{
		ID:         "01912d2c-1e2f-7abc-8def-000000000001",
		ChannelID:  "11111111-1111-1111-1111-111111111111",
		Kind:       "custom",
		Name:       "screen_view",
		DeviceTime: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		ServerTime: time.Date(2026, 1, 1, 12, 0, 1, 0, time.UTC),
		Properties: `{}`,
	}
}

func TestToRawEventDefaultsMissingMessageAndVariantToTheZeroUUID(t *testing.T) {
	e := validLoggedEvent()
	// e.MessageID and e.VariantID are left empty, as events_log's COALESCE(...,"") scan produces for
	// an event outside the impression family (CW-0009 Unit 1).

	row, err := e.ToRawEvent()
	if err != nil {
		t.Fatalf("ToRawEvent() error = %v, want nil", err)
	}
	if row.MessageID != uuid.Nil {
		t.Errorf("MessageID = %v, want the zero UUID", row.MessageID)
	}
	if row.VariantID != uuid.Nil {
		t.Errorf("VariantID = %v, want the zero UUID", row.VariantID)
	}
}

func TestToRawEventParsesMessageAndVariantWhenPresent(t *testing.T) {
	e := validLoggedEvent()
	e.Kind = "impression"
	e.MessageID = "22222222-2222-2222-2222-222222222222"
	e.VariantID = "33333333-3333-3333-3333-333333333333"

	row, err := e.ToRawEvent()
	if err != nil {
		t.Fatalf("ToRawEvent() error = %v, want nil", err)
	}
	if row.MessageID.String() != e.MessageID {
		t.Errorf("MessageID = %v, want %v", row.MessageID, e.MessageID)
	}
	if row.VariantID.String() != e.VariantID {
		t.Errorf("VariantID = %v, want %v", row.VariantID, e.VariantID)
	}
}

func TestToRawEventCarriesEveryScalarFieldThrough(t *testing.T) {
	e := validLoggedEvent()
	e.SuppressionReason = "project_budget"

	row, err := e.ToRawEvent()
	if err != nil {
		t.Fatalf("ToRawEvent() error = %v, want nil", err)
	}
	if row.ID.String() != e.ID {
		t.Errorf("ID = %v, want %v", row.ID, e.ID)
	}
	if row.ChannelID.String() != e.ChannelID {
		t.Errorf("ChannelID = %v, want %v", row.ChannelID, e.ChannelID)
	}
	if row.Kind != e.Kind {
		t.Errorf("Kind = %q, want %q", row.Kind, e.Kind)
	}
	if row.Name != e.Name {
		t.Errorf("Name = %q, want %q", row.Name, e.Name)
	}
	if !row.DeviceTime.Equal(e.DeviceTime) {
		t.Errorf("DeviceTime = %v, want %v", row.DeviceTime, e.DeviceTime)
	}
	if !row.ServerTime.Equal(e.ServerTime) {
		t.Errorf("ServerTime = %v, want %v", row.ServerTime, e.ServerTime)
	}
	if row.Properties != e.Properties {
		t.Errorf("Properties = %q, want %q", row.Properties, e.Properties)
	}
	if row.SuppressionReason != e.SuppressionReason {
		t.Errorf("SuppressionReason = %q, want %q", row.SuppressionReason, e.SuppressionReason)
	}
}

func TestToRawEventRejectsAnUnparsableEventID(t *testing.T) {
	e := validLoggedEvent()
	e.ID = "not-a-uuid"

	if _, err := e.ToRawEvent(); err == nil {
		t.Error("ToRawEvent() = nil error for an unparsable id, want one")
	}
}

func TestToRawEventRejectsAnUnparsableChannelID(t *testing.T) {
	e := validLoggedEvent()
	e.ChannelID = "not-a-uuid"

	if _, err := e.ToRawEvent(); err == nil {
		t.Error("ToRawEvent() = nil error for an unparsable channel id, want one")
	}
}

func TestToRawEventRejectsAnUnparsableNonEmptyMessageID(t *testing.T) {
	e := validLoggedEvent()
	e.MessageID = "not-a-uuid"

	if _, err := e.ToRawEvent(); err == nil {
		t.Error("ToRawEvent() = nil error for an unparsable message id, want one")
	}
}
