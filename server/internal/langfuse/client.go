package langfuse

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName = "multica-server"
	tracerName  = "github.com/multica-ai/multica/server/internal/langfuse"
)

// Client owns one TracerProvider bound to a Langfuse project. It is safe for
// concurrent use. A nil *Client is valid and turns every method into a no-op,
// which is how an unconfigured deployment is represented.
type Client struct {
	exporter  sdktrace.SpanExporter
	provider  *sdktrace.TracerProvider
	tracer    trace.Tracer
	endpoint  string
	baseAttrs []attribute.KeyValue
	env       string
	release   string
}

// New builds a Client from cfg. It returns (nil, nil) when the configuration is
// incomplete or disabled so wiring stays simple: callers keep the nil client
// and every instrumentation call becomes a no-op. An invalid base URL is the
// only boot-time error.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	cfg = cfg.withDefaults()
	endpoint, err := cfg.endpointURL()
	if err != nil {
		return nil, err
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization":                cfg.authorization(),
			"x-langfuse-ingestion-version": ingestionVersionValue,
		}),
		otlptracehttp.WithTimeout(cfg.ExportTimeout),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("langfuse: build OTLP exporter: %w", err)
	}
	client := newClient(cfg, sdktrace.WithBatcher(exporter,
		sdktrace.WithBatchTimeout(cfg.BatchTimeout),
		sdktrace.WithExportTimeout(cfg.ExportTimeout),
		sdktrace.WithMaxQueueSize(cfg.MaxQueueSize),
		sdktrace.WithMaxExportBatchSize(cfg.MaxBatchSize),
	))
	client.exporter = exporter
	client.endpoint = endpoint
	return client, nil
}

// NewWithExporter builds a Client that hands every finished span synchronously
// to exporter. It exists for tests and never touches the network.
func NewWithExporter(cfg Config, exporter sdktrace.SpanExporter) *Client {
	client := newClient(cfg.withDefaults(), sdktrace.WithSyncer(exporter))
	client.exporter = exporter
	return client
}

func newClient(cfg Config, processor sdktrace.TracerProviderOption) *Client {
	installErrorHandler()
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", strings.TrimSpace(cfg.Release)),
	)
	provider := sdktrace.NewTracerProvider(
		processor,
		sdktrace.WithResource(res),
		sdktrace.WithIDGenerator(idGenerator{}),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	client := &Client{
		provider: provider,
		tracer:   provider.Tracer(tracerName),
		env:      cfg.Environment,
		release:  strings.TrimSpace(cfg.Release),
	}
	if client.env != "" {
		client.baseAttrs = append(client.baseAttrs, attribute.String(attrEnvironment, client.env))
	}
	if client.release != "" {
		client.baseAttrs = append(client.baseAttrs, attribute.String(attrRelease, client.release))
	}
	return client
}

var errorHandlerOnce sync.Once

// installErrorHandler routes exporter failures (network, 4xx/5xx) into the
// server log once instead of the SDK's default stderr printer, so a Langfuse
// outage shows up as ordinary warnings and never as a crash.
func installErrorHandler() {
	errorHandlerOnce.Do(func() {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			if err == nil {
				return
			}
			slog.Warn("langfuse export failed", "event", "langfuse_export_failed", "error", err)
		}))
	})
}

// Enabled reports whether traces will be exported.
func (c *Client) Enabled() bool { return c != nil }

// Endpoint is the resolved OTLP traces URL (empty for test clients).
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// Environment is the normalized Langfuse environment stamped on every span.
func (c *Client) Environment() string {
	if c == nil {
		return ""
	}
	return c.env
}

// ForceFlush pushes every queued span to Langfuse and waits for the export.
func (c *Client) ForceFlush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.provider.ForceFlush(ctx)
}

// Shutdown flushes and stops the exporter. Call it during graceful shutdown
// after every producer has exited.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	return c.provider.Shutdown(ctx)
}

// forcedIDs lets a caller pin the trace and/or span identifier of the next
// span started from a context. The coordinator turn id and the task id then
// become the Langfuse trace id directly, which makes the SLS/Multica ids
// pasteable into the Langfuse search box.
type forcedIDs struct {
	traceID trace.TraceID
	spanID  trace.SpanID
}

type forcedIDsKey struct{}

func withForcedIDs(ctx context.Context, ids forcedIDs) context.Context {
	return context.WithValue(ctx, forcedIDsKey{}, ids)
}

func forcedFromContext(ctx context.Context) forcedIDs {
	ids, _ := ctx.Value(forcedIDsKey{}).(forcedIDs)
	return ids
}

type idGenerator struct{}

func (idGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	forced := forcedFromContext(ctx)
	traceID := forced.traceID
	if !traceID.IsValid() {
		traceID = randomTraceID()
	}
	spanID := forced.spanID
	if !spanID.IsValid() {
		spanID = randomSpanID()
	}
	return traceID, spanID
}

func (idGenerator) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	if forced := forcedFromContext(ctx); forced.spanID.IsValid() {
		return forced.spanID
	}
	return randomSpanID()
}

func randomTraceID() trace.TraceID {
	var id trace.TraceID
	for !id.IsValid() {
		_, _ = rand.Read(id[:])
	}
	return id
}

func randomSpanID() trace.SpanID {
	var id trace.SpanID
	for !id.IsValid() {
		_, _ = rand.Read(id[:])
	}
	return id
}

// ParseTraceID accepts a UUID (with or without dashes) or a 32-character hex
// string and returns the OpenTelemetry trace id. ok is false for any other
// input, in which case callers should let the SDK generate a random id.
func ParseTraceID(raw string) (trace.TraceID, bool) {
	cleaned := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
	if len(cleaned) != 32 {
		return trace.TraceID{}, false
	}
	id, err := trace.TraceIDFromHex(cleaned)
	if err != nil || !id.IsValid() {
		return trace.TraceID{}, false
	}
	return id, true
}

// TraceIDHex is the Langfuse trace id for a Multica UUID: the 32 lowercase hex
// characters without dashes. It returns "" for anything ParseTraceID rejects.
func TraceIDHex(raw string) string {
	id, ok := ParseTraceID(raw)
	if !ok {
		return ""
	}
	return id.String()
}

// DeterministicSpanID derives a stable 8-byte span id from key. Producers that
// cannot share a context (the sandbox LLM relay and the task completion hook
// run in different requests) use it to agree on a parent observation before
// either of them has been exported.
func DeterministicSpanID(key string) string {
	sum := sha256.Sum256([]byte(key))
	var id trace.SpanID
	copy(id[:], sum[:8])
	if !id.IsValid() {
		id[0] = 1
	}
	return hex.EncodeToString(id[:])
}

func parseSpanID(raw string) (trace.SpanID, bool) {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	if len(cleaned) != 16 {
		return trace.SpanID{}, false
	}
	id, err := trace.SpanIDFromHex(cleaned)
	if err != nil || !id.IsValid() {
		return trace.SpanID{}, false
	}
	return id, true
}
