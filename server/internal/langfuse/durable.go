package langfuse

import (
	"context"
	"errors"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ExportObservation synchronously delivers a durable projection to OTLP. The
// caller owns persistence/retry and must use a deterministic observation ID.
// It bypasses the lossy batch queue; success is transport acknowledgement,
// not a promise that Langfuse's asynchronous query index is already current.
func (c *Client) ExportObservation(ctx context.Context, traceOpts TraceOptions, opts ObservationOptions, end EndOptions) error {
	if c == nil || c.exporter == nil {
		return errors.New("Langfuse exporter unavailable")
	}
	if _, ok := ParseTraceID(traceOpts.TraceID); !ok {
		return errors.New("durable observation requires a valid trace ID")
	}
	if _, ok := parseSpanID(opts.SpanID); !ok {
		return errors.New("durable observation requires a stable span ID")
	}
	collector := &spanCollector{}
	local := NewWithExporter(Config{Environment: c.env, Release: c.release}, collector)
	defer local.Shutdown(context.Background())
	local.StartObservationInTrace(ctx, traceOpts, opts).End(end)
	return c.exporter.ExportSpans(ctx, collector.spans)
}

// Each collector belongs to a single synchronous observation export.
type spanCollector struct{ spans []sdktrace.ReadOnlySpan }

func (c *spanCollector) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.spans = append(c.spans, spans...)
	return nil
}
func (*spanCollector) Shutdown(context.Context) error { return nil }
