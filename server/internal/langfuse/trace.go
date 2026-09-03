package langfuse

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Langfuse span attribute vocabulary. See
// https://langfuse.com/integrations/native/opentelemetry#property-mapping.
const (
	attrTraceName          = "langfuse.trace.name"
	attrTraceInput         = "langfuse.trace.input"
	attrTraceOutput        = "langfuse.trace.output"
	attrTraceTags          = "langfuse.trace.tags"
	attrTraceMetadataPfx   = "langfuse.trace.metadata."
	attrUserID             = "langfuse.user.id"
	attrSessionID          = "langfuse.session.id"
	attrEnvironment        = "langfuse.environment"
	attrRelease            = "langfuse.release"
	attrObsType            = "langfuse.observation.type"
	attrObsLevel           = "langfuse.observation.level"
	attrObsStatusMessage   = "langfuse.observation.status_message"
	attrObsInput           = "langfuse.observation.input"
	attrObsOutput          = "langfuse.observation.output"
	attrObsMetadataPfx     = "langfuse.observation.metadata."
	attrObsModel           = "langfuse.observation.model.name"
	attrObsModelParameters = "langfuse.observation.model.parameters"
	attrObsUsageDetails    = "langfuse.observation.usage_details"
	attrObsCompletionStart = "langfuse.observation.completion_start_time"
	// OpenTelemetry GenAI semantic-convention usage counters, mapped by every
	// Langfuse version (the langfuse.* JSON form is newer).
	attrGenAIUsageInput     = "gen_ai.usage.input_tokens"
	attrGenAIUsageOutput    = "gen_ai.usage.output_tokens"
	attrGenAIUsageTotal     = "gen_ai.usage.total_tokens"
	attrGenAIUsageCacheRead = "gen_ai.usage.cache_read_input_tokens"
)

// ObservationType is the Langfuse observation kind. Unknown values fall back
// to "span" on the Langfuse side.
type ObservationType string

const (
	TypeSpan       ObservationType = "span"
	TypeGeneration ObservationType = "generation"
	TypeEvent      ObservationType = "event"
	TypeTool       ObservationType = "tool"
	TypeAgent      ObservationType = "agent"
	TypeChain      ObservationType = "chain"
	TypeRetriever  ObservationType = "retriever"
)

// Level is the Langfuse observation severity.
type Level string

const (
	LevelDefault Level = "DEFAULT"
	LevelDebug   Level = "DEBUG"
	LevelWarning Level = "WARNING"
	LevelError   Level = "ERROR"
)

// Usage is the token accounting for a generation. Zero fields are omitted;
// Total is derived from Input+Output when it is zero and either is known.
type Usage struct {
	Input      int64
	Output     int64
	Total      int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
}

func (u *Usage) empty() bool {
	return u == nil || (u.Input == 0 && u.Output == 0 && u.Total == 0 &&
		u.CacheRead == 0 && u.CacheWrite == 0 && u.Reasoning == 0)
}

func (u *Usage) details() map[string]int64 {
	out := map[string]int64{}
	if u == nil {
		return out
	}
	if u.Input > 0 {
		out["input"] = u.Input
	}
	if u.Output > 0 {
		out["output"] = u.Output
	}
	total := u.Total
	if total == 0 && (u.Input > 0 || u.Output > 0) {
		total = u.Input + u.Output
	}
	if total > 0 {
		out["total"] = total
	}
	if u.CacheRead > 0 {
		out["cache_read_input_tokens"] = u.CacheRead
	}
	if u.CacheWrite > 0 {
		out["cache_creation_input_tokens"] = u.CacheWrite
	}
	if u.Reasoning > 0 {
		out["reasoning_tokens"] = u.Reasoning
	}
	return out
}

// TraceOptions describes the root observation of a Langfuse trace plus the
// trace-level attributes (user, session, tags, metadata) that are copied onto
// every observation so Langfuse filters and aggregations work per span.
type TraceOptions struct {
	// TraceID pins the Langfuse trace id. UUIDs and 32-hex strings are
	// accepted; anything else yields a random id.
	TraceID string
	// RootSpanID optionally pins the root observation id (16 hex chars, see
	// DeterministicSpanID) so producers in other requests can parent under it.
	RootSpanID string
	// Name names the root observation and, unless TraceName is set, the
	// trace itself.
	Name string
	// TraceName names the trace when it differs from the root observation,
	// for example a task that joins the trace of the coordinator turn that
	// started it. Langfuse resolves a trace's name and tags from whichever
	// span it processes last, so every producer of one trace must send the
	// same TraceName and Tags.
	TraceName string
	Type      ObservationType
	UserID    string
	SessionID string
	// Tags must be complete when the trace starts: Langfuse freezes them on
	// the first span it ingests. Put outcome-dependent values in Metadata.
	Tags []string
	// Metadata becomes first-level, filterable trace metadata.
	Metadata map[string]any
	// RootMetadata is observation metadata of the root only.
	RootMetadata map[string]any
	Input        any
	StartTime    time.Time
}

// ObservationOptions describes one child observation.
type ObservationOptions struct {
	Type      ObservationType
	Name      string
	StartTime time.Time
	Input     any
	Metadata  map[string]any
	Level     Level
	// Model and ModelParameters apply to generations.
	Model           string
	ModelParameters map[string]any
	// SpanID pins the observation id; ParentSpanID parents the observation
	// under an id that is not carried by the Go context (see
	// DeterministicSpanID). Both are optional 16-hex strings.
	SpanID       string
	ParentSpanID string
}

// EndOptions closes an observation.
type EndOptions struct {
	EndTime             time.Time
	CompletionStartTime time.Time
	Output              any
	Usage               *Usage
	Level               Level
	StatusMessage       string
	Metadata            map[string]any
	// Err marks the observation as ERROR with the error text as status.
	Err error
}

// Trace is one Langfuse trace rooted at a single observation. A nil *Trace is
// valid: every method is a no-op, which is what callers get from a nil Client.
type Trace struct {
	client     *Client
	ctx        context.Context
	root       trace.Span
	id         trace.TraceID
	traceAttrs []attribute.KeyValue
	ended      bool
}

// StartTrace opens a new trace with its root observation. The returned Trace
// is nil when the client is disabled.
func (c *Client) StartTrace(ctx context.Context, opts TraceOptions) *Trace {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	forced := forcedIDs{}
	if id, ok := ParseTraceID(opts.TraceID); ok {
		forced.traceID = id
	}
	if id, ok := parseSpanID(opts.RootSpanID); ok {
		forced.spanID = id
	}
	traceAttrs := c.traceAttributes(opts)
	obsType := opts.Type
	if obsType == "" {
		obsType = TypeSpan
	}
	attrs := append([]attribute.KeyValue{}, traceAttrs...)
	attrs = append(attrs, attribute.String(attrObsType, string(obsType)))
	if opts.Input != nil {
		encoded := encodePayload(opts.Input)
		attrs = append(attrs,
			attribute.String(attrObsInput, encoded),
			attribute.String(attrTraceInput, encoded),
		)
	}
	attrs = append(attrs, metadataAttributes(attrObsMetadataPfx, opts.RootMetadata)...)
	start := opts.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	spanCtx, span := c.tracer.Start(withForcedIDs(ctx, forced), nonEmptyName(opts.Name, "trace"),
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(start),
		trace.WithAttributes(attrs...),
	)
	// Clear the pinned ids so children started from this context get fresh
	// span ids instead of reusing the root's.
	spanCtx = withForcedIDs(spanCtx, forcedIDs{})
	return &Trace{
		client:     c,
		ctx:        spanCtx,
		root:       span,
		id:         span.SpanContext().TraceID(),
		traceAttrs: traceAttrs,
	}
}

// traceAttributes are the trace-level attributes copied onto every span of a
// trace. Langfuse creates the trace record from whichever export batch
// arrives first and only merges user, session, and metadata keys afterwards;
// tags are frozen at creation. Every span therefore carries the full tag set,
// which is why TraceOptions.Tags must be known when the trace starts and
// outcome fields (action, status) are metadata rather than tags.
func (c *Client) traceAttributes(opts TraceOptions) []attribute.KeyValue {
	attrs := append([]attribute.KeyValue{}, c.baseAttrs...)
	traceName := strings.TrimSpace(opts.TraceName)
	if traceName == "" {
		traceName = strings.TrimSpace(opts.Name)
	}
	if traceName != "" {
		attrs = append(attrs, attribute.String(attrTraceName, traceName))
	}
	if user := strings.TrimSpace(opts.UserID); user != "" {
		attrs = append(attrs, attribute.String(attrUserID, user))
	}
	if session := strings.TrimSpace(opts.SessionID); session != "" {
		attrs = append(attrs, attribute.String(attrSessionID, session))
	}
	if tags := cleanTags(opts.Tags); len(tags) > 0 {
		attrs = append(attrs, attribute.StringSlice(attrTraceTags, tags))
	}
	attrs = append(attrs, metadataAttributes(attrTraceMetadataPfx, opts.Metadata)...)
	return attrs
}

// ID is the 32-hex Langfuse trace id, or "" for a nil trace.
func (t *Trace) ID() string {
	if t == nil {
		return ""
	}
	return t.id.String()
}

// RootSpanID is the root observation id.
func (t *Trace) RootSpanID() string {
	if t == nil {
		return ""
	}
	return t.root.SpanContext().SpanID().String()
}

// Context carries the root span for nested instrumentation.
func (t *Trace) Context() context.Context {
	if t == nil {
		return context.Background()
	}
	return t.ctx
}

// AddMetadata adds first-level trace metadata after the trace started (for
// example the issue id a decision produced). Children started earlier keep
// the attributes they were created with.
func (t *Trace) AddMetadata(metadata map[string]any) {
	if t == nil || t.ended {
		return
	}
	attrs := metadataAttributes(attrTraceMetadataPfx, metadata)
	if len(attrs) == 0 {
		return
	}
	t.traceAttrs = append(t.traceAttrs, attrs...)
	if t.root != nil {
		t.root.SetAttributes(attrs...)
	}
}

// StartObservationInTrace opens one observation inside an existing trace
// without creating a root observation. Producers that run in a different
// request than the trace owner (the sandbox LLM relay, for example) use it
// with a pinned trace id and a deterministic ParentSpanID so their spans land
// under the right parent whether or not that parent was exported yet.
func (c *Client) StartObservationInTrace(ctx context.Context, traceOpts TraceOptions, opts ObservationOptions) *Observation {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, ok := ParseTraceID(traceOpts.TraceID)
	if !ok {
		traceID = randomTraceID()
	}
	// Detached observations usually arrive before the root (the sandbox relay
	// runs during the task) and may create the trace record, so they carry
	// the same trace-level attributes, tags included. See traceAttributes.
	detached := &Trace{
		client:     c,
		ctx:        withForcedIDs(ctx, forcedIDs{traceID: traceID}),
		id:         traceID,
		traceAttrs: c.traceAttributes(traceOpts),
	}
	return detached.start(detached.ctx, opts)
}

// ContextWithTrace stores t so nested code can attach observations without
// threading the trace through every signature.
func ContextWithTrace(ctx context.Context, t *Trace) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, traceContextKey{}, t)
}

// TraceFromContext returns the trace stored by ContextWithTrace, or nil.
func TraceFromContext(ctx context.Context) *Trace {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(traceContextKey{}).(*Trace)
	return t
}

type traceContextKey struct{}

// StartObservation opens a child of the root observation.
func (t *Trace) StartObservation(opts ObservationOptions) *Observation {
	if t == nil {
		return nil
	}
	return t.start(t.ctx, opts)
}

// Event records a zero-duration observation under the root.
func (t *Trace) Event(opts ObservationOptions, end EndOptions) {
	if t == nil {
		return
	}
	if opts.Type == "" {
		opts.Type = TypeEvent
	}
	if end.EndTime.IsZero() {
		end.EndTime = opts.StartTime
	}
	t.start(t.ctx, opts).End(end)
}

// End closes the root observation and, with it, the trace.
func (t *Trace) End(end EndOptions) {
	if t == nil || t.ended || t.root == nil {
		return
	}
	t.ended = true
	if end.Output != nil {
		t.root.SetAttributes(attribute.String(attrTraceOutput, encodePayload(end.Output)))
	}
	finishSpan(t.root, end)
}

func (t *Trace) start(parent context.Context, opts ObservationOptions) *Observation {
	ctx := parent
	var startOpts []trace.SpanStartOption
	if parentID, ok := parseSpanID(opts.ParentSpanID); ok {
		ctx = trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    t.id,
			SpanID:     parentID,
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		}))
	} else if t.root == nil {
		// Detached observation: no root span in ctx, so pin the trace id and
		// start a new root that Langfuse shows as a top-level observation.
		startOpts = append(startOpts, trace.WithNewRoot())
	}
	forced := forcedFromContext(ctx)
	if spanID, ok := parseSpanID(opts.SpanID); ok {
		forced.spanID = spanID
	}
	ctx = withForcedIDs(ctx, forced)
	obsType := opts.Type
	if obsType == "" {
		obsType = TypeSpan
	}
	attrs := append([]attribute.KeyValue{}, t.traceAttrs...)
	attrs = append(attrs, attribute.String(attrObsType, string(obsType)))
	if opts.Input != nil {
		attrs = append(attrs, attribute.String(attrObsInput, encodePayload(opts.Input)))
	}
	if model := strings.TrimSpace(opts.Model); model != "" {
		attrs = append(attrs, attribute.String(attrObsModel, model))
	}
	if len(opts.ModelParameters) > 0 {
		attrs = append(attrs, attribute.String(attrObsModelParameters, encodePayload(opts.ModelParameters)))
	}
	if opts.Level != "" {
		attrs = append(attrs, attribute.String(attrObsLevel, string(opts.Level)))
	}
	attrs = append(attrs, metadataAttributes(attrObsMetadataPfx, opts.Metadata)...)
	start := opts.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	startOpts = append(startOpts,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithTimestamp(start),
		trace.WithAttributes(attrs...),
	)
	spanCtx, span := t.client.tracer.Start(ctx, nonEmptyName(opts.Name, string(obsType)), startOpts...)
	spanCtx = withForcedIDs(spanCtx, forcedIDs{})
	return &Observation{trace: t, ctx: spanCtx, span: span}
}

// Observation is one child observation (span, generation, tool, event, ...).
// A nil *Observation is valid and inert.
type Observation struct {
	trace *Trace
	ctx   context.Context
	span  trace.Span
	ended bool
}

// SpanID is the 16-hex observation id.
func (o *Observation) SpanID() string {
	if o == nil {
		return ""
	}
	return o.span.SpanContext().SpanID().String()
}

// StartObservation opens a nested child.
func (o *Observation) StartObservation(opts ObservationOptions) *Observation {
	if o == nil {
		return nil
	}
	return o.trace.start(o.ctx, opts)
}

// End closes the observation.
func (o *Observation) End(end EndOptions) {
	if o == nil || o.ended {
		return
	}
	o.ended = true
	finishSpan(o.span, end)
}

func finishSpan(span trace.Span, end EndOptions) {
	var attrs []attribute.KeyValue
	if end.Output != nil {
		attrs = append(attrs, attribute.String(attrObsOutput, encodePayload(end.Output)))
	}
	if !end.Usage.empty() {
		attrs = append(attrs, usageAttributes(end.Usage)...)
	}
	if !end.CompletionStartTime.IsZero() {
		attrs = append(attrs, attribute.String(attrObsCompletionStart, end.CompletionStartTime.UTC().Format(time.RFC3339Nano)))
	}
	level := end.Level
	status := strings.TrimSpace(end.StatusMessage)
	if end.Err != nil {
		level = LevelError
		if status == "" {
			status = end.Err.Error()
		}
		span.RecordError(end.Err)
	}
	if level != "" {
		attrs = append(attrs, attribute.String(attrObsLevel, string(level)))
	}
	if status != "" {
		attrs = append(attrs, attribute.String(attrObsStatusMessage, clipString(status, 2000)))
	}
	if level == LevelError {
		span.SetStatus(codes.Error, clipString(status, 2000))
	}
	attrs = append(attrs, metadataAttributes(attrObsMetadataPfx, end.Metadata)...)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	endTime := end.EndTime
	if endTime.IsZero() {
		endTime = time.Now()
	}
	span.End(trace.WithTimestamp(endTime))
}

// usageAttributes renders token usage in both vocabularies: the Langfuse JSON
// attribute newer servers map directly, and the OpenTelemetry GenAI semantic
// convention counters (gen_ai.usage.*) that this deployment's Langfuse build
// maps into usageDetails. Emitting both keeps usage visible across versions.
func usageAttributes(usage *Usage) []attribute.KeyValue {
	details := usage.details()
	attrs := []attribute.KeyValue{attribute.String(attrObsUsageDetails, encodePayload(details))}
	if input, ok := details["input"]; ok {
		attrs = append(attrs, attribute.Int64(attrGenAIUsageInput, input))
	}
	if output, ok := details["output"]; ok {
		attrs = append(attrs, attribute.Int64(attrGenAIUsageOutput, output))
	}
	if total, ok := details["total"]; ok {
		attrs = append(attrs, attribute.Int64(attrGenAIUsageTotal, total))
	}
	if cacheRead, ok := details["cache_read_input_tokens"]; ok {
		attrs = append(attrs, attribute.Int64(attrGenAIUsageCacheRead, cacheRead))
	}
	return attrs
}

// maxPayloadBytes bounds a single input/output attribute. Langfuse accepts
// large payloads, but a runaway transcript should never make one span
// exceed the exporter batch budget.
const maxPayloadBytes = 64 << 10

// encodePayload renders an input/output value as the JSON string Langfuse
// expects. Strings pass through unchanged; everything else is marshaled.
func encodePayload(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return clipString(v, maxPayloadBytes)
	case []byte:
		return clipString(string(v), maxPayloadBytes)
	case json.RawMessage:
		return clipString(string(v), maxPayloadBytes)
	case fmt.Stringer:
		return clipString(v.String(), maxPayloadBytes)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return clipString(fmt.Sprintf("%v", value), maxPayloadBytes)
	}
	return clipString(string(raw), maxPayloadBytes)
}

// metadataAttributes flattens metadata under prefix. Empty strings and nils
// are dropped so optional lookup keys never appear as blank filters.
func metadataAttributes(prefix string, metadata map[string]any) []attribute.KeyValue {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		name := prefix + strings.TrimSpace(key)
		switch v := metadata[key].(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			attrs = append(attrs, attribute.String(name, clipString(v, 4000)))
		case bool:
			attrs = append(attrs, attribute.Bool(name, v))
		case int:
			attrs = append(attrs, attribute.Int64(name, int64(v)))
		case int32:
			attrs = append(attrs, attribute.Int64(name, int64(v)))
		case int64:
			attrs = append(attrs, attribute.Int64(name, v))
		case float64:
			attrs = append(attrs, attribute.Float64(name, v))
		case []string:
			if len(v) > 0 {
				attrs = append(attrs, attribute.StringSlice(name, v))
			}
		default:
			attrs = append(attrs, attribute.String(name, encodePayload(v)))
		}
	}
	return attrs
}

func cleanTags(tags []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func nonEmptyName(name, fallback string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return fallback
}

// clipString truncates s to at most max bytes on a rune boundary and marks
// the cut so a reader knows the payload is partial.
func clipString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…[truncated " + strconv.Itoa(len(s)-cut) + " bytes]"
}
