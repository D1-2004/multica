# LLM trace delivery

Multica can enable model request/response tracing for one Agent task without
changing the model provider protocol. The runtime image's existing provider
reverse proxy captures the pair and posts it to the selected receiver on a
best-effort, fail-open path.

## Configuration and precedence

The Agent setting remains:

```json
{
  "llm_trace": {
    "enabled": true,
    "sink_url": "https://trace.example.test/ingest"
  }
}
```

Delivery is enabled only when `enabled` is true and the effective URL is
non-empty. For an Agent Dispatch task, a complete private callback containing
`telemetry_url`, `telemetry_token`, and `telemetry_expires_at` overrides the
static `sink_url`. An incomplete callback is ignored. A static receiver remains
compatible without a token, and the Router token is never attached to it.

Multica passes these values only in the task's sandbox execution environment:

- `MULTICA_LLM_TRACE_ENABLED`
- `MULTICA_LLM_TRACE_SINK_URL`
- `MULTICA_LLM_TRACE_TOKEN`
- `MULTICA_LLM_TRACE_EXPIRES_AT`

The selected Runtime must advertise `llm_trace_v1`. Multica injects none of
these variables into an older image, even when the Agent setting is enabled.
This capability check is the execution-time safety boundary; UI rollout flags
alone do not make an image compatible.

The runtime consumes the token from its protected generation configuration.
It must not place the token in a URL, provider header, trace body, error payload,
or log message.

## Router paired-event contract

For a task-scoped Router callback, the runtime sends `Authorization: Bearer`
and posts one stable sequence per upstream model call:

```json
{
  "sequence": 1,
  "request": {
    "body": "{\"model\":\"model-id\"}",
    "size": 20,
    "truncated": false,
    "sha256": "hex"
  },
  "response": {
    "body": "data: {...}\n\n",
    "size": 14,
    "truncated": false,
    "sha256": "hex",
    "status": 200,
    "complete": true
  }
}
```

`size` and `sha256` describe the complete byte stream. `body` is bounded by the
runtime capture limit and `truncated` says whether bytes were omitted. SSE is
stored as its raw streamed text. Interrupted responses use `complete=false`.
Retries reuse the same sequence and exact payload; telemetry failure never
changes the model response returned to the Agent.

Router derives the task identity from the authenticated URL and capability. A
sender cannot select another task in the body. The capability expires after at
most three days and Router rejects it as soon as the dispatch task reaches a
terminal state.

## Completion summary

When a task terminates, Multica writes an immutable JSON summary into
`task_completion_outbox.execution_summary` in the same transaction as the
terminal result. The completion worker sends it as top-level
`executionSummary` on every retry. It contains the existing task-summary shape:
timing, provider/model usage, message/tool counts, and runtime/current sandbox.

Router uses this pushed snapshot for Agent environment data. LLM request and
response pairs remain available through Router's separate inference-detail
query and are not added to the ordinary trace-detail response.

## Privacy and operations

Model bodies can contain prompts, tool schemas, credentials mistakenly supplied
by a caller, and model output. Receiver access and retention must therefore be
restricted as sensitive debugging data. Logs should contain only delivery
status, task identity, sequence, sizes, and bounded error classification.

## History

| Date | Change | Reason |
| --- | --- | --- |
| 2026-08-06 | Added optional Agent-controlled request mirroring through the existing provider proxy | Allow request-level troubleshooting without a second MITM proxy |
| 2026-08-07 | Upgraded delivery to paired request/response events, added Router task capabilities, and pushed the immutable execution summary with completion | Reconstruct the full reasoning timeline while avoiding broad sandbox credentials and post-terminal Router pulls |
| 2026-08-08 | Added `llm_trace_v1` Runtime capability negotiation and the complete runner environment allowlist | Keep old images running without Trace while preventing unsupported images from receiving task-scoped telemetry credentials |
