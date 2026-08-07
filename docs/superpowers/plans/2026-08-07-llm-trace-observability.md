# LLM Trace Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture each sandbox LLM request and response as one ordered trace record, send it to Router with a task-scoped three-day write capability that stops working after terminal completion, and expose the data through a dedicated Dashboard panel.

**Architecture:** Router owns the trace capability, ingestion table, terminal revocation decision, and separate read API. Multica persists the Router callback material and the terminal execution summary in its reliable completion outbox. Runtime tees the upstream response while preserving streaming behavior and posts request/response pairs directly to Router. Dashboard keeps ordinary trace detail small and loads LLM pairs only when the new panel is opened.

**Tech Stack:** Java 17/Spring/PostgreSQL Router, Go/PostgreSQL Multica, Python runtime proxy, React/TypeScript Dashboard, Aone CI/CD.

---

## Cross-project wire contract

Router dispatches the following optional callback fields. Old Multica versions must ignore them.

```json
{
  "completionCallback": {
    "url": "/api/v1/dispatch-tasks/dispatch-1/execution-result",
    "updateUrl": "/api/v1/dispatch-tasks/dispatch-1/execution-update",
    "telemetryUrl": "https://router.example.test/api/v1/dispatch-tasks/dispatch-1/llm-traces",
    "telemetryToken": "opaque-task-write-capability",
    "telemetryExpiresAt": 1786377600000
  }
}
```

Multica extends the existing terminal callback with an immutable summary captured into the completion outbox:

```json
{
  "requestId": "multica-terminal:task-id",
  "externalTaskId": "task-id",
  "executionStatus": "completed",
  "executionSummary": {
    "task_id": "task-id",
    "status": "completed",
    "created_at": "2026-08-07T00:00:00Z",
    "completed_at": "2026-08-07T00:01:00Z",
    "duration_ms": 60000,
    "provider": "openai",
    "model": "model-id",
    "message_count": 8,
    "tool_call_count": 2,
    "usage_details": [],
    "runtime": null
  }
}
```

Runtime posts one paired event. Router derives `task_id` from the trusted URL/token instead of accepting it from the body.

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

## Task 1: Router protocol, capability, and persistence

**Files:** Locate current equivalents before editing; protocol truth remains the public DTOs and `docs/contracts/api-reference.md`.

- [ ] Add failing DTO/dispatch tests proving `telemetryUrl`, `telemetryToken`, and `telemetryExpiresAt` are present and remain optional.
- [ ] Add a task-scoped HMAC capability with `scope=llm_trace:write`, dispatch task ID, issued/expiry timestamps, and nonce. Use a distinct signature domain from service-to-service dispatch credentials.
- [ ] Add the five-column `llm_trace(task_id, sequence, request, response, updated_at)` table with primary key `(task_id, sequence)` and matching repository tests.
- [ ] Add failing controller/service tests for valid write, wrong task, expired token, terminal task, exact replay, and conflicting replay.
- [ ] Implement `POST /api/v1/dispatch-tasks/{id}/llm-traces`. Exact replay returns success; differing content on the same sequence returns 409; terminal or cancelled tasks return 410.
- [ ] Extend execution-result DTO with optional `executionSummary`; persist it into existing task audit/runtime snapshot fields without launching the old post-terminal Multica summary fetch.
- [ ] Add a dedicated observability method for LLM trace pairs. Do not add pairs to `getObservabilityTrace` and do not reuse the Transcript method.
- [ ] Update protocol/design docs and append dated history entries describing the callback, capability lifecycle, separate query, and completion-summary push.
- [ ] Run focused tests, the Router package suite, JDK 17 build, and `git diff --check`; commit and push an atomic feature branch based on current `origin/master`.
- [ ] Create/submit the Router change and monitor its delivery pipeline until build and deployment stages succeed.

## Task 2: Multica callback propagation and completion summary

**Files:** Expected areas include `server/internal/handler/agent_dispatch_v2.go`, `server/internal/service/task_completion*`, `server/internal/integrations/agentmessagerouter/*`, migrations, sqlc queries/generated files, FC/ASB task launchers, and protocol docs.

- [ ] Add failing dispatch parsing/validation tests for optional telemetry callback fields and confirm callback URL/target idempotency remains unchanged.
- [ ] Persist telemetry callback values in task context. When `llm_trace.enabled` is true, a complete Router URL/token/expiry capability overrides the static Agent sink; otherwise a non-empty static sink remains backward compatible without a token. Never send the Router token to the static sink.
- [ ] Add failing terminal tests showing the same task-summary shape currently returned by the Multica summary endpoint is frozen into `task_completion_outbox` in the terminal transaction.
- [ ] Add the outbox JSONB column and regenerate sqlc. The worker must send the persisted summary on every retry rather than rebuilding a potentially changed sandbox snapshot.
- [ ] Extend `ExecutionResultRequest` with `executionSummary` and add worker retry/idempotency tests.
- [ ] Inject telemetry URL/token/expiry into FC/E2B and ASB exec environments without command-line logging; the runtime runner consumes and unsets the secret before launching the agent.
- [ ] Update the dispatch/completion protocol docs and append a dated history entry explaining replacement of Router pull enrichment with completion push.
- [ ] Run focused Go tests, `make test`, TypeScript checks affected by agent settings, and `git diff --check`.
- [ ] Commit and push to the existing `feature/20260807_agent_llm_trace` branch, submit the correct change, and monitor the Multica pre-release delivery pipeline through successful deployment.

## Task 3: Runtime paired capture and delivery

**Files:** `scripts/multica-provider-http-proxy`, `scripts/multica-fc-hermes-runner`, provider proxy tests, runner tests, smoke tests, and runtime documentation.

- [ ] Add failing proxy tests for ordinary JSON responses, SSE responses, 4xx/5xx responses, interrupted streams, capture truncation/hash, retry replay, and disabled/missing telemetry configuration.
- [ ] Allocate sequence at request start and persist the next value in the task generation state so proxy restart does not reuse a sequence.
- [ ] Tee response chunks to the caller and a bounded collector. Hash and count the full stream while retaining at most 1 MiB of request and 1 MiB of response body.
- [ ] Enqueue the completed pair for direct Router delivery with `Authorization: Bearer <telemetryToken>`; do not place the token in URL, logs, proxy error payloads, or provider requests.
- [ ] Retry transient delivery with the same sequence and treat 401/403/409/410 as terminal telemetry outcomes. Telemetry failure must not fail the LLM request.
- [ ] Add bounded shutdown flush before the runner exits. Unsent telemetry after the budget is dropped and must not block task completion.
- [ ] Preserve the current generation-header isolation and streaming behavior for Hermes, OpenCode, and Pi.
- [ ] Run proxy, runner, manifest, and runtime smoke tests locally where supported; commit and push to existing `codex/llm-trace-runtime-20260807`.
- [ ] Update the existing branch CI definitions only as required to point at the new Multica branch SHA. Trigger the branch pipelines and require successful FC/E2B template smoke plus ASB image verification.

## Task 4: Dashboard separate panel and API

**Files:** Locate the current trace detail drawer, Transcript card, LWP client schemas, and shared panel/resizer primitives before editing.

- [ ] Add failing API/schema tests for the new independent Agent inference detail response and malformed responses.
- [ ] Add failing component tests for the new `Agent推理详情` section immediately below `Agent环境`.
- [ ] Rename visible `Agent执行` copy to `Agent环境` without changing Router status semantics.
- [ ] Remove the `展开 Transcript` card and all Transcript-on-expand calls from the trace detail panel.
- [ ] Load the new LLM-pair API only when `Agent推理详情` is opened; preserve pagination by sequence and display request/response, status, truncation, sizes, and hashes.
- [ ] Make the trace detail side panel width draggable with a bounded minimum/maximum and persisted client-side width. Keyboard resizing and narrow-screen fallback must remain usable.
- [ ] Run focused tests, typecheck/build, and `git diff --check`; commit and push to the existing observability Dashboard branch.
- [ ] Submit the Dashboard change and monitor its build/deployment pipeline until successful.

## Task 5: PM acceptance

- [ ] Verify each remote branch SHA and that no unrelated changes or secrets were committed.
- [ ] Verify Router, Multica, Runtime, and Dashboard pipeline run IDs, source branch/SHA, trigger, and terminal stage statuses.
- [ ] Verify Runtime publishes an immutable image digest, READY E2B template, successful real-sandbox smoke, and successful ASB candidate image validation.
- [ ] Record protocol versions, migrations, rollback order, remote SHAs, pipeline evidence, and the deferred end-to-end test owned by the user.
- [ ] Do not claim overall integration success; the agreed completion gate is successful component deployment, with cross-service functional integration deferred.
