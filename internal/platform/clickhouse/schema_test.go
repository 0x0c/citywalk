package clickhouse_test

import (
	"strings"
	"testing"

	"github.com/0x0c/citywalk/internal/platform/clickhouse"
)

func TestRawEventsDDLDeclaresTheReplacingMergeTreeEngineKeyedByServerTime(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	want := "ENGINE = ReplacingMergeTree(server_time)"
	if !strings.Contains(ddl, want) {
		t.Errorf("RawEventsDDL() = %q, want it to contain %q (CW-0009 Unit 4's merge-time dedup)", ddl, want)
	}
}

func TestRawEventsDDLOrdersByMessageDeviceTimeThenID(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	want := "ORDER BY (message_id, device_time, id)"
	if !strings.Contains(ddl, want) {
		t.Errorf("RawEventsDDL() = %q, want it to contain %q (Unit 4's report-scan order, id appended for dedup)", ddl, want)
	}
}

func TestRawEventsDDLPartitionsByDay(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	want := "PARTITION BY toDate(device_time)"
	if !strings.Contains(ddl, want) {
		t.Errorf("RawEventsDDL() = %q, want it to contain %q", ddl, want)
	}
}

func TestRawEventsDDLSetsA13MonthTTLOnDeviceTime(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	want := "TTL device_time + INTERVAL 13 MONTH"
	if !strings.Contains(ddl, want) {
		t.Errorf("RawEventsDDL() = %q, want it to contain %q (CW-0010 Unit 6's retention figure)", ddl, want)
	}
	if clickhouse.RawEventRetentionMonths != 13 {
		t.Errorf("RawEventRetentionMonths = %d, want 13", clickhouse.RawEventRetentionMonths)
	}
}

func TestRawEventsDDLIsIdempotent(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	if !strings.Contains(ddl, "CREATE TABLE IF NOT EXISTS events_raw") {
		t.Errorf("RawEventsDDL() = %q, want a CREATE TABLE IF NOT EXISTS events_raw statement", ddl)
	}
}

func TestRawEventsDDLDeclaresEveryEventsLogColumnEventsRawNeeds(t *testing.T) {
	ddl := clickhouse.RawEventsDDL()
	for _, column := range []string{
		"id", "channel_id", "kind", "name", "device_time", "server_time",
		"properties", "message_id", "variant_id", "suppression_reason",
	} {
		if !strings.Contains(ddl, column) {
			t.Errorf("RawEventsDDL() = %q, want it to declare column %q", ddl, column)
		}
	}
}
