package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/model"
	"github.com/0x0c/citywalk/internal/platform/eventlog"
)

// TestFromLogEventPreservesEveryRollupRelevantField confirms the one conversion this pass adds
// (decoded log event -> storedEvent) does not drop or mistranslate anything applyBatch reads, which
// would otherwise silently miscount a rollup the log-backed consumer feeds.
func TestFromLogEventPreservesEveryRollupRelevantField(t *testing.T) {
	deviceTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	serverTime := deviceTime.Add(time.Second)
	e := model.Event{
		ID: "01912d2c-0000-7000-8000-000000000001", ChannelID: "channel-a",
		Kind: model.KindImpression, Name: "ignored-for-non-custom",
		DeviceTime: deviceTime, ServerTime: serverTime,
		MessageID: "message-1", VariantID: "variant-1",
	}

	got := fromLogEvent(e)

	want := storedEvent{
		ID: e.ID, ChannelID: e.ChannelID, Kind: e.Kind, Name: e.Name,
		DeviceTime: deviceTime, ServerTime: serverTime,
		MessageID: e.MessageID, VariantID: e.VariantID, SuppressionReason: "",
	}
	if got != want {
		t.Errorf("fromLogEvent(%+v) = %+v, want %+v", e, got, want)
	}
}

// fakeLogPoller is a unit-testable stand-in for *eventlog.Consumer: RunOnceFromLog only needs Poll
// and Commit, so a fake of those two is enough to exercise its control flow without a broker.
type fakeLogPoller struct {
	pollRecords []eventlog.Record
	pollErr     error
	committed   []eventlog.Record
	commitErr   error
}

func (f *fakeLogPoller) Poll(context.Context) ([]eventlog.Record, error) {
	return f.pollRecords, f.pollErr
}

func (f *fakeLogPoller) Commit(_ context.Context, batch []eventlog.Record) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	f.committed = batch
	return nil
}

// TestRunOnceFromLogReturnsZeroWithoutTouchingPostgresWhenNothingPolled confirms an empty poll is a
// true no-op: RunOnceFromLog must return before ever opening a Postgres transaction, since pool is nil
// here and a real attempt to use it would panic.
func TestRunOnceFromLogReturnsZeroWithoutTouchingPostgresWhenNothingPolled(t *testing.T) {
	poller := &fakeLogPoller{}

	n, err := RunOnceFromLog(context.Background(), nil, poller, nil)
	if err != nil {
		t.Fatalf("RunOnceFromLog: %v", err)
	}
	if n != 0 {
		t.Errorf("RunOnceFromLog() = %d, want 0", n)
	}
	if poller.committed != nil {
		t.Errorf("Commit was called with %v, want no call for an empty poll", poller.committed)
	}
}

// TestRunOnceFromLogPropagatesAPollError confirms a broker-side poll failure surfaces to the caller
// rather than being swallowed as "nothing new."
func TestRunOnceFromLogPropagatesAPollError(t *testing.T) {
	poller := &fakeLogPoller{pollErr: errors.New("broker unreachable")}

	if _, err := RunOnceFromLog(context.Background(), nil, poller, nil); err == nil {
		t.Fatal("RunOnceFromLog: got nil error, want one from the poller's failure")
	}
}

// TestRunOnceFromLogPropagatesADecodeError confirms a malformed record on the log fails loudly rather
// than being silently skipped or applied as a zero-value event.
func TestRunOnceFromLogPropagatesADecodeError(t *testing.T) {
	poller := &fakeLogPoller{pollRecords: []eventlog.Record{{Key: []byte("channel-a"), Value: []byte("not json")}}}

	if _, err := RunOnceFromLog(context.Background(), nil, poller, nil); err == nil {
		t.Fatal("RunOnceFromLog: got nil error, want one for a malformed record")
	}
}
