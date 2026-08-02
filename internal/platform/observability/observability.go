// Package observability bootstraps the signals CW-0010 Unit 10 requires of every service: traces,
// metrics, and structured logs. The four platform-specific metrics that unit also names (payload
// truncation, membership reconciliation disagreement, suppression by reason, and unsupported schema
// version) are registered by the services that produce them, not here. Structured logging uses the
// standard library's log/slog rather than OpenTelemetry's own logging modules or a third-party
// logger — CW-0010 Unit 2's operational-simplicity bias, and slog is already sufficient for a single
// JSON-per-line stream with no separate log backend in phase one (Unit 11).
package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

// Providers holds the process-wide trace and metric providers, plus the shutdown that flushes and
// closes both when the process stops.
type Providers struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	Shutdown       func(ctx context.Context) error
}

// Setup installs stdout exporters as the phase-one default (CW-0010 Unit 11 runs a single process
// with no observability backend of its own yet) and registers both providers as the global ones so
// any package can call otel.Tracer/otel.Meter without threading Providers through every call site.
func Setup(ctx context.Context, serviceName string) (Providers, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
	))
	if err != nil {
		return Providers{}, fmt.Errorf("observability: resource: %w", err)
	}

	traceExporter, err := stdouttrace.New(stdouttrace.WithoutTimestamps())
	if err != nil {
		return Providers{}, fmt.Errorf("observability: trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	metricExporter, err := stdoutmetric.New()
	if err != nil {
		return Providers{}, fmt.Errorf("observability: metric exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	return Providers{
		TracerProvider: tracerProvider,
		MeterProvider:  meterProvider,
		Shutdown: func(ctx context.Context) error {
			if err := tracerProvider.Shutdown(ctx); err != nil {
				return fmt.Errorf("observability: shutdown tracer provider: %w", err)
			}
			if err := meterProvider.Shutdown(ctx); err != nil {
				return fmt.Errorf("observability: shutdown meter provider: %w", err)
			}
			return nil
		},
	}, nil
}

// NewLogger constructs the process-wide structured logger: JSON, one record per line, suitable for a
// log collector to parse rather than a human terminal. serviceName is attached to every record, the
// same resource dimension Setup gives traces and metrics, so a multi-service log stream filters down
// to this one service the same way a trace or a metric would.
func NewLogger(serviceName string) *slog.Logger {
	return newLogger(os.Stdout, serviceName)
}

func newLogger(w io.Writer, serviceName string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil)).With(slog.String("service", serviceName))
}
