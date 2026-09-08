# Langfuse observability

The server exports LLM traces to a Langfuse project for the three loops that
call or drive models: the inbound Coordinator short loop, the Scene Memory
flush loop, and agent task execution (the Daemon side). The exporter lives in
`server/internal/langfuse` and speaks OTLP/HTTP to
`<LANGFUSE_BASE_URL>/api/public/otel/v1/traces`; Langfuse marks its legacy
`/api/public/ingestion` batch API as deprecated, so OTLP is the only path used.

## Configuration

| Key | Meaning |
| --- | --- |
| `LANGFUSE_PUBLIC_KEY` | Project public key (`pk-lf-...`). Required. |
| `LANGFUSE_SECRET_KEY` | Project secret key (`sk-lf-...`). Required, secret. |
| `LANGFUSE_BASE_URL` | Langfuse origin. `LANGFUSE_HOST` is accepted as an alias. Required. |
| `LANGFUSE_ENVIRONMENT` | Langfuse environment label. Defaults to `AONE_ENV_TYPE`, then `APP_ENV`. |
| `LANGFUSE_TRACING_ENABLED` | `false` keeps the keys but stops exporting. |

All keys are whitelisted in `src/main.sh` (`RUNTIME_CONFIG_KEYS`) and are read
from the Aone environment trait like every other runtime key. Missing keys
disable tracing; a malformed base URL logs `langfuse_config_invalid` and the
server boots without tracing. The exporter batches spans in memory, drops on a
full queue, retries transient HTTP failures with backoff, logs
`langfuse_export_failed` on give-up, and flushes on graceful shutdown. Nothing
in the request path waits on Langfuse.

Boot logs `langfuse_enabled` with the resolved endpoint and environment, or
`langfuse_disabled`.

## Trace model and lookup keys

Every trace carries the same lookup keys the SLS index uses, as first-level
Langfuse trace metadata (filterable in the UI and the `traces` API), plus user,
session, and tags on every span:

| Langfuse field | Coordinator turn | Memory flush | Agent task |
| --- | --- | --- | --- |
| trace id | `coord_trace_id` without dashes | random | task chat trace id (task UUID for non-chat tasks) without dashes |
| name | `inbound_coordinator` | `scene_memory_flush` | `agent_task` |
| user id | `person_id` → `dws_uid` → Multica user id → sender name | (none) | `person_id` → `dws_uid` → originator → initiator |
| session id | `conversation_id` (openConversationId) → chat session id | `scene_key` (openConversationId) | `conversation_id` → chat session id |
| tags | `inbound_coordinator`, `source-*`, `kind-*`, `agent-<id>`, `agent_name-<name>`, `workspace-<id>`, `user-<id>` | `scene_memory`, `kind-*`, `agent-<id>`, `agent_name-<name>`, `workspace-<id>` | `agent_task`, `runtime-*`, `provider-*`, `channel-*`, `source-*`, `agent-<id>`, `agent_name-<name>`, `workspace-<id>`, `user-<id>`, `task-<id>`, `issue-<id>` (a coordinator-owned task repeats the turn's tags instead) |
| index events (under one `index` node; only ids no tag, session or trace id covers) | `idx.evidence_id.*`, `idx.chat_session_id.*` (turns inside a conversation), the user ids the `user-` tag does not carry (`idx.person_id.*`, `idx.dws_uid.*`, `idx.user_id.*`) | `idx.scene_memory_id.*`, `idx.coord_trace_id.*`, `idx.job_id.*` (when it differs) | `idx.runtime_id.*`, `idx.session_id.*`, `idx.parent_task_id.*`, `idx.autopilot_run_id.*`, `idx.trigger_comment_id.*`; task-owned traces add `idx.chat_session_id.*` (when a conversation is the session) and the user ids the tag does not carry; coordinator-owned tasks add `idx.task_id.*`, `idx.issue_id.*` |
| outcome (metadata) | `action`, `issue_id`, `tool_rounds`, `fail_open` | `status`, `caught_up`, `replace`, `error_code` | `status`, `failure_reason`, token totals |

Two behaviours of the deployed Langfuse build shape the attribute layout
(verified on 2026-09-03 against `unify-aipilot.dingtalk.com`, v3.123.1):

- Token usage is read from the OpenTelemetry GenAI counters
  (`gen_ai.usage.input_tokens` / `output_tokens` / `total_tokens`); the newer
  `langfuse.observation.usage_details` JSON attribute is stored but not mapped,
  so the exporter emits both.
- A trace's tags are frozen by whichever export batch creates the trace
  record and later batches never add to them, while user id, session id, and
  metadata merge per key (eventually, within about a minute). Tags therefore
  hold only what is known when the trace starts and ride on every span
  (children usually end, and export, before the root); outcome fields such as
  the coordinator `action`, the flush `status`, and the task `status` are
  first-level metadata instead (`metadata.action`, `metadata.status`).

Metadata keys present on every span of a trace include `coord_trace_id`,
`conversation_id`, `conversation_name`, `conversation_kind`, `sender_name`,
`person_id`, `dws_uid`, `dws_org_id`, `agent_id`, `agent_name`,
`workspace_id`, `evidence_id`, `source`, `model`, and for tasks `task_id`,
`issue_id`, `runtime_id`, `runtime_mode`, `provider`, `chat_session_id`,
`status`, and `failure_reason`. A coordinator-created Issue task carries the
coordinator's `coord_trace_id` (stamped into `task.context.coordinator_trace_id`),
so the two traces can be joined in either direction.

### Coordinator turn (`inbound_coordinator`)

- Root observation: type `agent`, input = the inbound message plus prompt
  context sizes, output = the decision (`action`, `issue_id`, `user_text`,
  `reason`, `tool_rounds`, `tools_used`, `elapsed_ms`). Fail-open decisions
  are `ERROR` with the loop error as status.
- `dws_chat_history` (`retriever`): the DingTalk history preflight.
- `coordinator.round.N` (`generation`): one per model round with the full
  message list as input, the assistant message as output, model parameters,
  and token usage from the completion.
- Tool observations named after the tool (`assoc_recall`, `assoc_bind`,
  `issue_get`, `issue_comment_list`, `issue_comment_add`, `finish`) with
  arguments as input and the tool result as output. Server-side rejections
  (`recall_required`, `purpose_required`, `issue_not_recalled`, ...) are
  `WARNING` tool events; `issue_busy` is `ERROR`.
- `coordinator.nudge` and `coordinator.loop_error` events.

A coordinator turn and the task it starts are one trace: web turns share the
chat trace with the Issue task they create, channel-engine turns reuse the
inbound chat trace, and Router-dispatched turns install the coordinator trace
id as the Issue task's chat trace (`inboundcoord.WithCoordinatorTrace`). The
coordinator's root observation and the task's `agent_task` root then sit side
by side under one trace id, with the sandbox generations beneath the task.
Because Langfuse resolves a trace's name and tags from whichever span it
processes last, the task repeats the turn's trace name (`inbound_coordinator`)
and tags (stamped into `task.context.coordinator_trace_tags`) on every span;
the task's runtime and provider remain as metadata, and the `agent_task` root
observation name still identifies the task inside the trace. Trace metadata
merges per key with the last writer winning, and this Langfuse build also folds
every root observation's metadata into the trace, so a coordinator-owned task
sends none of the turn's conversation-level keys (`loop`, `conversation_*`,
`sender_name`, `person_id`, `dws_uid`, `dws_org_id`, `agent_*`,
`workspace_id`, `chat_session_id`) on any of its spans; without that, the
DingTalk conversation type `single` replaced the turn's `conversation_kind`
`p2p`. The same folding is why the coordinator's prompt-context root metadata
(`persona`, history counts, `scene_memory`) shows up in the trace metadata.

For a DingTalk digital-employee turn handled by the durable coordinator job,
the job id is the turn's trace id: the worker pins it through
`inboundcoord.ContextWithTraceID`, the dispatch handler hands it to the channel
engine as the inbound chat trace, and the Scene Memory trigger recorded at
enqueue time stores the same id. `coord_trace_id` in SLS, the Langfuse trace
id, `job_id`, and the flush's `coord_trace_id` are therefore one value.

### Memory flush (`scene_memory_flush`)

- Root observation: type `chain`, input = current memory and cursor, output =
  `status`, `event_count`, `caught_up`, `replace`, `new_memory_revision`, and
  the new text when replaced. Retryable failures (history not yet visible,
  transient DWS errors) are `WARNING`; terminal failures are `ERROR`.
- `dws_history_range` (`retriever`) with the read window and event count.
- `memory_flush.round.N` (`generation`) per merge round, plus
  `memory_flush_commit` tool events (accepted or rejected with the reason) and
  `memory_flush.nudge` events.

### Agent task (`agent_task`) — the Daemon side

The daemon never talks to Langfuse and needs no new build. It already reports
messages, usage, and completion to the server, and FC runtime images with the
`llm_trace_v1` capability relay every model request/response pair to
`POST /api/daemon/tasks/{taskId}/llm-traces`. The server assembles the trace:

- `llm.call.N` (`generation`): emitted by the relay handler as each pair
  arrives, parented under the task root via a deterministic observation id
  (`service.TaskLangfuseRootSpanID`). OpenAI Chat Completions, OpenAI
  Responses, and Anthropic Messages bodies are parsed (plain JSON and SSE);
  model, parameters, messages, output, finish reason, and token usage are
  filled where the provider reports them. Upstream errors and interrupted
  streams are `ERROR` / `WARNING`. When the exporter is configured, capture is
  turned on for every task on a capable runtime image
  (`FCE2BLauncher.LLMTraceCaptureAlways`), independent of Router telemetry or
  the Agent static sink; those two destinations are unchanged. The observation
  id is derived from the task id and the runtime's sequence number, so the
  runtime's retries of one pair upsert the same generation, and a task with no
  dispatch context (a web-created Issue) is accepted with 204 once the
  observer has it instead of being rejected with 503.
- The root observation (type `agent`) is emitted once the terminal status
  commits, from `TaskService.captureTaskCompleted/Failed/Cancelled`, on a
  detached goroutine: issue title and trigger as input, result or error as
  output, aggregated token usage, runtime/provider/model metadata.
- Children from the persisted transcript: `tool_use` rows become `tool`
  observations closed by their `tool_result`; assistant `text` and `thinking`
  become events. Capped at 300 messages per task.

Local (persistent) daemons therefore get the task tree without model bodies;
FC sandboxes get both. The runtime image is only involved through the existing
`llm_trace_v1` proxy contract in `docs/llm-trace.md`.

## When an image rebuild is needed

None of the above changes the `multica` binary that runs inside the FC image:
the daemon's task engine and the runtime proxy contract are untouched. A
rebuild through the FC candidate loop (`.agents/skills/fc-runtime-dev-loop`,
Aone pipeline `295064` with `multica_ref=<40-char commit>`, then
`switch`/`create` of a candidate Runtime and a task canary) is only required
if the daemon itself must emit new telemetry, for example per-provider events
that never reach the server today. Because the sandbox already relays bodies
and reports transcripts, the server-side path was chosen so pre-release gets
traces from a normal application deploy.

## Finding a trace

Use the `inspect-langfuse-trace` skill
(`.agents/skills/inspect-langfuse-trace/`); its script wraps the public API
and reads the keys from `LANGFUSE_*` environment variables. What the deployed
Langfuse build can and cannot filter on was measured on 2026-09-04:

| API capability | Result |
| --- | --- |
| `GET /api/public/traces/{id}`, `sessionId=`, `name=`, `environment=`, `tags=` (AND) | works |
| `tags=` values containing a colon | never match, so tags use `key-value` |
| `userId=` | returns nothing: the trace list stores no user id (the detail does) |
| `filter=` JSON (metadata and friends) | accepted but ignored |
| `GET /api/public/observations?name=` | exact match; `=`, `+`, CJK fine, `:` and whitespace not |

Hence two index layers per trace: dash-style tags for the categorical keys
known at trace start, and, only for the ids no tag, session or trace id
covers, one zero-duration `DEBUG` event per id named `idx.<key>.<value>`
(colons and whitespace in the value become `_`; `langfuse.IndexToken`). A
producer's index events hang from one `DEBUG` span named `index` under its
root observation, whose metadata lists the ids, so the trace tree shows a
single collapsible node rather than one row per id. Every producer emits its
index with deterministic ids, so the relay can index a task before it
finishes and the completion hook upserts the same node.

| Id in hand | Lookup |
| --- | --- |
| `coord_trace_id`, digital-employee coordinator job id, chat trace id | trace id = the UUID without dashes |
| `task_id` | non-chat task: trace id = the task UUID without dashes; task-owned trace: `task-<uuid>` tag; task inside a coordinator trace: `idx.task_id.<uuid>` |
| `issue_id` | `issue-<uuid>` tag on task-owned traces; `idx.issue_id.<uuid>` inside a coordinator trace |
| openConversationId / cid / scene_key | `sessionId=<cid>` (turns, flushes, and tasks of the scene) |
| web chat session id | `sessionId=<uuid>` (web turns and tasks) or `idx.chat_session_id.<uuid>` (turns and tasks inside a DingTalk conversation) |
| DingTalk uid / dws_uid / Multica user id | `user-<id>` tag; the trace's other user ids as `idx.person_id.*`, `idx.dws_uid.*`, `idx.user_id.*` |
| `agent_id` / `workspace_id` | `agent-<uuid>` / `workspace-<uuid>` tags |
| `evidence_id` (openMsgId), `scene_memory_id`, trigger `job_id`, `runtime_id`, provider `session_id` | `idx.evidence_id.*`, `idx.scene_memory_id.*`, `idx.job_id.*` (when it differs from `coord_trace_id`), `idx.runtime_id.*`, `idx.session_id.*` |

The skill's `key <name> <value>` command applies these fallbacks itself, so
any key of this table works with it.

Cross-links: a coordinator turn and the task it starts share one trace; a
memory flush's `coord_trace_id` (and `idx.coord_trace_id`) names the turn that
triggered it; a task's `issue_id` names its Issue.

Prompts, DingTalk history, tool arguments, and model bodies are exported as
observation input/output. The same content already reaches SLS and the
existing Router/static LLM trace sinks; treat the Langfuse project as
sensitive debugging data with the same access rules.
