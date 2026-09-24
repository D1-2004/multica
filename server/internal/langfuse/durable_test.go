package langfuse

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type failingDurableExporter struct{ calls int }

func (e *failingDurableExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	e.calls++
	return errors.New("OTLP unavailable")
}
func (*failingDurableExporter) Shutdown(context.Context) error { return nil }

func TestDurableObservationReportsTransportFailure(t *testing.T) {
	exporter := &failingDurableExporter{}
	client := NewWithExporter(Config{}, exporter)
	defer client.Shutdown(context.Background())
	err := client.ExportObservation(context.Background(), TraceOptions{TraceID: "12345678123412341234123456789abc"}, ObservationOptions{SpanID: DeterministicSpanID("choice"), Name: "choice"}, EndOptions{})
	if err == nil || exporter.calls != 1 {
		t.Fatal("transport failure acknowledged", err, exporter.calls)
	}
	if client.ExportObservation(context.Background(), TraceOptions{}, ObservationOptions{}, EndOptions{}) == nil {
		t.Fatal("accepted random identity for durable export")
	}
}
