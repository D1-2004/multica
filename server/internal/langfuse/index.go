package langfuse

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Lookup index for business ids.
//
// The deployed Langfuse build exposes only a few searchable dimensions through
// its public API: the trace id, sessionId, exact tag matches, names, and
// environment. Its JSON `filter` parameter is accepted but ignored, the trace
// list never records a user id, and tags containing a colon never match. To
// keep every business id reachable from the API (and from the UI's name
// search), each trace carries:
//
//   - dash-style tags for the categorical keys known at trace start
//     (Tag("source", "web") -> "source-web"), and
//   - for the ids no tag, session or trace id covers, one zero-duration DEBUG
//     event named "idx.<key>.<value>" (IndexObservationName), so
//     GET /api/public/observations?name=idx.task_id.<uuid> returns the
//     observation and its traceId.
//
// A producer's index events sit under one DEBUG "index" span whose metadata
// lists the ids, so the trace tree shows a single collapsible node instead
// of one row per id. Index observations have deterministic ids, so every
// producer of a trace (the coordinator turn, the sandbox relay, the task
// completion hook) may emit them and Langfuse upserts instead of duplicating.
const (
	IndexNamePrefix    = "idx."
	IndexContainerName = "index"
	indexEventLevel    = LevelDebug
)

// IndexToken normalizes an id for use inside a tag or an index observation
// name: colons and whitespace, which the server's lookups cannot match, become
// underscores. Everything else (UUID dashes, base64 "+"/"=", CJK) is kept.
func IndexToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == ':' || unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, value)
}

// Tag renders a categorical key/value as the dash-style tag this deployment
// can filter on ("source-web", "agent-<uuid>"). An empty value yields "".
func Tag(key, value string) string {
	token := IndexToken(value)
	if token == "" || strings.TrimSpace(key) == "" {
		return ""
	}
	return strings.TrimSpace(key) + "-" + token
}

// IndexObservationName is the observation name that indexes one id.
func IndexObservationName(key, value string) string {
	token := IndexToken(value)
	if token == "" || strings.TrimSpace(key) == "" {
		return ""
	}
	return IndexNamePrefix + strings.TrimSpace(key) + "." + token
}

// Index emits one index event per non-empty id under the root observation.
func (t *Trace) Index(keys map[string]string) {
	if t == nil || t.ended {
		return
	}
	t.indexEvents(t.ctx, "", keys)
}

// IndexInTrace emits index events inside an existing trace without a root,
// parented on parentSpanID when it is set (see StartObservationInTrace).
func (c *Client) IndexInTrace(ctx context.Context, traceOpts TraceOptions, parentSpanID string, keys map[string]string) {
	if c == nil || len(keys) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, ok := ParseTraceID(traceOpts.TraceID)
	if !ok {
		traceID = randomTraceID()
	}
	detached := &Trace{
		client:     c,
		ctx:        withForcedIDs(ctx, forcedIDs{traceID: traceID}),
		id:         traceID,
		traceAttrs: c.traceAttributes(traceOpts),
		startedAt:  traceOpts.StartTime,
	}
	detached.indexEvents(detached.ctx, parentSpanID, keys)
}

func (t *Trace) indexEvents(ctx context.Context, parentSpanID string, keys map[string]string) {
	names := make([]string, 0, len(keys))
	values := make(map[string]any, len(keys))
	for key, value := range keys {
		if IndexObservationName(key, value) == "" {
			continue
		}
		names = append(names, key)
		values[key] = strings.TrimSpace(value)
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	at := t.startedAt
	if at.IsZero() {
		at = time.Now()
	}
	// One index node per producer: keyed by the parent it hangs from, so the
	// relay and the completion hook share the task's node while the
	// coordinator turn keeps its own.
	parentKey := parentSpanID
	if parentKey == "" && t.root != nil {
		parentKey = t.root.SpanContext().SpanID().String()
	}
	container := t.start(ctx, ObservationOptions{
		Type:         TypeSpan,
		Name:         IndexContainerName,
		StartTime:    at,
		Level:        indexEventLevel,
		Metadata:     values,
		SpanID:       DeterministicSpanID(t.id.String() + ":idx:" + parentKey),
		ParentSpanID: parentSpanID,
	})
	for _, key := range names {
		name := IndexObservationName(key, keys[key])
		// Explicit parent: a detached trace (no root in ctx) would otherwise
		// start each event as a new root.
		obs := t.start(ctx, ObservationOptions{
			Type:         TypeEvent,
			Name:         name,
			StartTime:    at,
			Level:        indexEventLevel,
			Metadata:     map[string]any{"index_key": key, "index_value": values[key]},
			SpanID:       DeterministicSpanID(t.id.String() + ":idx:" + name),
			ParentSpanID: container.SpanID(),
		})
		obs.End(EndOptions{EndTime: at})
	}
	container.End(EndOptions{EndTime: at})
}
