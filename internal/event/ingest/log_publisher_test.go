package ingest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0x0c/citywalk/internal/event/ingest"
	"github.com/0x0c/citywalk/internal/event/model"
)

// fakeLogProducer records every key/value LogPublisher hands it and, past failAt, returns an error —
// enough to unit test LogPublisher.Publish's contract without a live broker.
type fakeLogProducer struct {
	published []publishedRecord
	failAt    int // index at which Publish starts failing; -1 means never
}

type publishedRecord struct {
	key   string
	value []byte
}

func (f *fakeLogProducer) Publish(_ context.Context, key, value []byte) error {
	if f.failAt >= 0 && len(f.published) == f.failAt {
		return errors.New("fake producer: broker unreachable")
	}
	f.published = append(f.published, publishedRecord{key: string(key), value: value})
	return nil
}

// TestLogPublisherPublishesEveryEventKeyedByChannel confirms CW-0009 Unit 3's partitioning
// requirement at the publisher's own boundary: every event reaches the fake producer keyed by its own
// channel identifier (eventlog.PartitionKey), stamped with the batch's server receipt time, and the
// reported count is every event in the batch since the log does no write-time dedup.
func TestLogPublisherPublishesEveryEventKeyedByChannel(t *testing.T) {
	fake := &fakeLogProducer{failAt: -1}
	publisher := ingest.LogPublisher{Producer: fake}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	events := []model.Event{
		{ID: "e1", ChannelID: "channel-a", Kind: model.KindCustom, Name: "launch", DeviceTime: now},
		{ID: "e2", ChannelID: "channel-b", Kind: model.KindCustom, Name: "launch", DeviceTime: now},
	}

	accepted, err := publisher.Publish(context.Background(), events, now)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if accepted != 2 {
		t.Fatalf("Publish() accepted = %d, want 2", accepted)
	}
	if len(fake.published) != 2 {
		t.Fatalf("producer received %d records, want 2", len(fake.published))
	}
	if fake.published[0].key != "channel-a" || fake.published[1].key != "channel-b" {
		t.Errorf("record keys = %q, %q, want %q, %q", fake.published[0].key, fake.published[1].key, "channel-a", "channel-b")
	}

	for i, rec := range fake.published {
		decoded, err := model.DecodeLogEvent(rec.value)
		if err != nil {
			t.Fatalf("DecodeLogEvent(record %d): %v", i, err)
		}
		if !decoded.ServerTime.Equal(now) {
			t.Errorf("record %d ServerTime = %v, want %v (the batch's receipt time)", i, decoded.ServerTime, now)
		}
		if decoded.ID != events[i].ID {
			t.Errorf("record %d ID = %q, want %q", i, decoded.ID, events[i].ID)
		}
	}
}

// TestLogPublisherStopsAndReportsCountOnFirstFailure confirms Publish does not silently skip a
// failure partway through a batch: it stops publishing and returns the count that did succeed
// alongside a non-nil error, so a caller cannot mistake a partial batch for a complete one.
func TestLogPublisherStopsAndReportsCountOnFirstFailure(t *testing.T) {
	fake := &fakeLogProducer{failAt: 1}
	publisher := ingest.LogPublisher{Producer: fake}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	events := []model.Event{
		{ID: "e1", ChannelID: "channel-a", Kind: model.KindCustom, Name: "launch", DeviceTime: now},
		{ID: "e2", ChannelID: "channel-b", Kind: model.KindCustom, Name: "launch", DeviceTime: now},
		{ID: "e3", ChannelID: "channel-c", Kind: model.KindCustom, Name: "launch", DeviceTime: now},
	}

	accepted, err := publisher.Publish(context.Background(), events, now)
	if err == nil {
		t.Fatal("Publish: got nil error, want one from the fake producer's failure")
	}
	if accepted != 1 {
		t.Errorf("Publish() accepted = %d, want 1 (only the event published before the failure)", accepted)
	}
	if len(fake.published) != 1 {
		t.Errorf("producer received %d records, want 1", len(fake.published))
	}
}
