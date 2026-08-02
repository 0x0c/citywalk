package observability_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/0x0c/citywalk/internal/platform/observability"
)

// TestSetupRegistersBothProvidersGlobally is what CW-0010 Unit 10 relies on for every package that
// records a signal: ingest, payload, and the rest all reach for otel.Meter/otel.Tracer rather than
// being handed a Providers value, so a Setup that built providers without installing them globally
// would leave every one of those instruments writing to a no-op.
func TestSetupRegistersBothProvidersGlobally(t *testing.T) {
	ctx := context.Background()

	providers, err := observability.Setup(ctx, "citywalk-test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() {
		if err := providers.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if providers.TracerProvider == nil || providers.MeterProvider == nil {
		t.Fatalf("Setup returned TracerProvider=%v MeterProvider=%v, want both set", providers.TracerProvider, providers.MeterProvider)
	}
	if otel.GetTracerProvider() != providers.TracerProvider {
		t.Error("otel.GetTracerProvider() is not the provider Setup returned, want it installed globally")
	}
	if otel.GetMeterProvider() != providers.MeterProvider {
		t.Error("otel.GetMeterProvider() is not the provider Setup returned, want it installed globally")
	}
}

// TestSetupProvidersRecordSpansAndMeasurements exercises the providers end to end rather than only
// asserting they are non-nil: the stdout exporters CW-0010 Unit 11 uses for phase one are batched, so
// a misconfigured pipeline surfaces on the recording and flush path, not at construction.
func TestSetupProvidersRecordSpansAndMeasurements(t *testing.T) {
	ctx := context.Background()

	providers, err := observability.Setup(ctx, "citywalk-test")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	_, span := providers.TracerProvider.Tracer("citywalk/test").Start(ctx, "unit-test-span")
	span.End()

	counter, err := providers.MeterProvider.Meter("citywalk/test").Int64Counter("citywalk.test.count")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(ctx, 1)

	// Shutdown is the flush: an exporter that cannot write is reported here, which is why the
	// process-stop path returns an error at all rather than being fire-and-forget.
	if err := providers.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
