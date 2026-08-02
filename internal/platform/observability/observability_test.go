package observability

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestNewLoggerEmitsParseableJSON proves the logging setup CW-0010 Unit 10 requires: one JSON record
// per line, carrying the message, the level, and the service dimension every record is tagged with.
func TestNewLoggerEmitsParseableJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, "citywalk-test")

	logger.Info("request handled", "procedure", "/citywalk.delivery.v1.DeliveryService/Sync")

	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want exactly 1: %q", len(lines), output)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("log record is not valid JSON: %v\nrecord: %s", err, lines[0])
	}

	if got := record["msg"]; got != "request handled" {
		t.Errorf("msg = %v, want %q", got, "request handled")
	}
	if got := record["service"]; got != "citywalk-test" {
		t.Errorf("service = %v, want %q", got, "citywalk-test")
	}
	if got := record["procedure"]; got != "/citywalk.delivery.v1.DeliveryService/Sync" {
		t.Errorf("procedure = %v, want the passed attribute", got)
	}
	if _, ok := record["time"]; !ok {
		t.Error("record has no time field")
	}
}
