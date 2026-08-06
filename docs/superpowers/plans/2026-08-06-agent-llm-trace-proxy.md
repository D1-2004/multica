# Agent LLM Trace Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a workspace administrator enable LLM-request tracing per cloud Agent and configure a receiver URL.  When the switch is on and that URL is non-empty, every OpenAI-compatible model request through the sandbox provider proxy is asynchronously delivered as JSON containing its task ID and body, while the Agent's request/response path (including SSE token streaming) remains unchanged.

**Architecture:** Reuse the runtime image's existing sandbox-lifetime Python provider reverse proxy at `127.0.0.1:33123`; do not add `mitmproxy` or a second proxy.  Multica persists only `runtime_config.llm_trace`, turns it into task-scoped runner environment variables, and the runner writes them into its existing mode-`0600`, generation-scoped provider-proxy JSON file.  If and only if `enabled == true` and `sink_url` is non-empty, the provider proxy puts `{ "task_id": ..., "body": ... }` into a bounded asynchronous delivery queue before forwarding upstream.  The delivery worker POSTs that JSON to the configured receiver and never participates in the upstream request path.

**Tech Stack:** Go (Multica server), TypeScript/React (Web/Desktop shared views), PostgreSQL JSONB (existing `agent.runtime_config`, no migration), Bash + Python standard library (the existing `multica-fc-hermes-runtime` image), OpenAI-compatible HTTP/SSE.

---

## 1. Decision and boundaries

### 1.1 Why not package mitmproxy

`mitmproxy` could implement this as a reverse-proxy Python addon, but it is not the appropriate production seam here.  The runtime image already has `/usr/local/libexec/multica-provider-http-proxy`, a Python `ThreadingHTTPServer` which:

- receives every provider call because the task runner replaces `OPENAI_BASE_URL` with `http://127.0.0.1:33123`;
- keeps one listener for the complete warm-sandbox lifetime;
- selects immutable task configuration using `X-Multica-Provider-Generation` / `MULTICA_TRACE_ID`;
- passes successful upstream bodies through in small chunks rather than buffering SSE;
- has existing error redaction and sensitive-header handling.

Starting one `mitmdump` per `sandbox exec` would add a second hop, duplicate reverse-proxy logic, make the fixed loopback port and warm-sandbox cleanup racy, and risk making trace delivery affect the model path.  Extend the existing Python proxy instead.  This still gives the requested proxy-based trace, without TLS interception or changing every CLI implementation.

### 1.2 In-scope / non-goals

In scope:

- FC/E2B and ASB cloud Agent tasks that use an image advertising `llm_trace_v1`.
- A per-Agent enable switch and receiver URL.
- One receiver POST for each provider HTTP request, including failed/retried model requests.
- A JSON delivery containing only the task ID and the model request body.
- Non-blocking, fail-open delivery plus explicit loss/error observability.

Out of scope for v1:

- Local runtime / user-machine daemon tracing.
- Capturing model responses, tool execution traffic, or general sandbox egress.
- Replaying requests from Multica, mutating requests, or changing provider authentication.
- User-configured trace authorization headers or tokens.  A receiver must authenticate the sandbox through its HTTPS ingress, mTLS, workload identity, or network policy.
- Retrofitting an already-published runtime image.  Old images do not advertise the capability and cannot enable the feature.

### 1.3 Configuration contract

The existing `agent.runtime_config` JSONB remains the storage location; no database migration is needed.  The only new persisted member is:

```json
{
  "llm_trace": {
    "enabled": true,
    "sink_url": "https://llm-trace.example.internal/v1/ingest"
  }
}
```

Rules:

1. `llm_trace` is optional.  For a capable image, every task receives the two trace exec variables: an absent value or `enabled: false` becomes `MULTICA_LLM_TRACE_ENABLED=false` and an empty URL.  An image without `llm_trace_v1` receives neither variable and cannot mirror.
2. At execution time the complete delivery condition is exactly `enabled == true && sink_url != ""`.  These two values are passed only through that task's `sandbox exec` environment; no Diamond/runtime configuration is read or written for LLM trace.
3. The setting may only be changed by a workspace `owner` or `admin` who otherwise has permission to update the Agent.  It is a prompt-export control, not ordinary Agent cosmetics.
4. The UI must preserve every other `runtime_config` key exactly.  In particular, it must not use the lossy OpenClaw form parser.
5. No API key, cookie, authorization header, or custom trace secret is stored in Agent JSON, emitted in task launch logs, or copied to the receiver.

The timeout and bounded queue are image implementation constants, not Multica deployment configuration.  The receiver URL is intentionally task-scoped: it is stored with the Agent setting, then injected into the one FC/E2B `e2b sandbox exec -e ...` command or ASB `/command` `envs` object that runs the task.

### 1.4 Receiver wire contract: `multica-llm-trace-v1`

For every intercepted provider request, the image sends an independent HTTP request:

```http
POST {sink_url}
Content-Type: application/json

{
  "task_id": "<MULTICA_TASK_ID>",
  "body": {
    "model": "...",
    "messages": [],
    "tools": []
  }
}
```

The delivery has exactly two application fields: `task_id` and `body`; it has no correlation headers or wrapper metadata.  For the normal OpenAI-compatible JSON request, `body` is the original request JSON value, embedded without parsing/re-serializing it, so tool definitions, messages, field ordering, and whitespace inside that value are retained.  An empty request becomes `"body": null`.  The trace receiver must accept each delivery independently; a client retry creates another delivery with the same task ID.

Original provider headers are deliberately not mirrored: they can contain `Authorization`, API keys, cookies, and non-portable hop-by-hop values.  Original query strings are also not mirrored.  There is no per-request body-size omission rule in v1: the proxy already reads the complete body to forward it upstream, and the trace envelope carries that same body.

Only 2xx receiver responses count as delivery success.  A timeout, DNS/TLS error, non-2xx response, or a full delivery queue must produce a redacted structured runtime log / metric and must never fail, delay, retry inline, or modify the original provider request.  The first version uses best-effort delivery, not a persistent outbox; sandbox teardown can also drop queued events and must be observable.

### 1.5 End-to-end sequence

```mermaid
sequenceDiagram
    participant UI as "Agent settings UI"
    participant S as "Multica server"
    participant P as "Sandbox platform (FC/E2B or ASB)"
    participant R as "Runner"
    participant X as "Persistent provider proxy :33123"
    participant L as "LLM provider"
    participant T as "Configured trace receiver"

    UI->>S: "PATCH Agent runtime_config.llm_trace"
    S->>S: "authorize owner/admin; validate capability and allowed origin"
    S->>P: "sandbox exec with task-scoped trace env"
    P->>R: "run fixed image entrypoint"
    R->>X: "write generation JSON (0600), then set OPENAI_BASE_URL=loopback"
    R->>X: "Agent CLI request + generation header"
    X->>X: "copy body to bounded queue"
    X->>L: "original request with original provider auth"
    X-->>R: "unchanged streaming response"
    X-->>T: "asynchronous POST of {task_id, body} JSON"
```

## 2. File map

### Multica repository — `/Users/wangxin/.codex/worktrees/25e9/dt-fde-multica`

| File | Change |
|---|---|
| `server/internal/service/llm_trace_config.go` | Create typed Agent-config parser, capability gate, and task-scoped exec-env builder. |
| `server/internal/service/llm_trace_config_test.go` | Unit-test the pure config and env boundary. |
| `server/internal/service/fc_e2b.go` | Add task trace variables to the existing extra-env construction and allowed-runner-env list. |
| `server/internal/service/asb_launcher.go` | Reuse the same task env when building ASB `/command` environment. |
| `server/internal/service/fc_e2b_test.go`, `server/internal/service/asb_launcher_test.go` | Assert enabled/disabled, old-image, and invalid-config launch behavior. |
| `server/internal/handler/agent.go` and its constructor/config file | Validate and authorize `llm_trace` on Agent create/patch; publish only availability, never allowlist details, to the frontend config. |
| `server/internal/handler/agent_test.go` (or existing focused Agent handler test) | Test owner/admin authorization, bad URL/origin rejection, and preservation of unrelated runtime config. |
| `packages/core/agents/llm-trace-runtime-config.ts` | Create non-lossy parse/merge/equality helpers. |
| `packages/core/agents/llm-trace-runtime-config.test.ts` | Cover malformed data and preservation of `gateway`/unknown members. |
| `packages/views/agents/components/tabs/llm-trace-tab.tsx` | Create the settings UI and privacy warning. |
| `packages/views/agents/components/tabs/llm-trace-tab.test.tsx` | Test validation, saving and read-only/no-capability state. |
| `packages/views/agents/components/agent-overview-pane.tsx` and tests | Add the gated `LLM trace` tab for cloud Agents on capable runtimes. |
| `packages/views/locales/{en,zh}/agents.json` | Add labels, warning, errors and delivery semantics. |
| `docs/llm-trace.md` | Add the receiver contract, privacy/operations guide, rollout and audit history. |

### Runtime-image repository — `/Users/wangxin/Documents/projects/work/multica-fc-hermes-runtime`

This is a separate repository.  Its changes must be implemented in a separate task titled **`[HF from dt-fde-multica] LLM trace image proxy`**, based on that repository's main branch; do not modify it from the Multica implementation task.

| File | Change |
|---|---|
| `scripts/multica-fc-hermes-runner` | Write v2 generation configuration from the new task env variables, without logging them or putting `OPENAI_API_KEY` in the file. |
| `scripts/multica-provider-http-proxy` | Add v2 config parsing and a bounded asynchronous trace delivery worker while preserving current upstream streaming. |
| `tests/multica-fc-hermes-runner-test.sh` | Assert v2 config, mode `0600`, and that provider credentials remain absent. |
| Python proxy tests (add `tests/provider_http_proxy_trace_test.py`) | Test `{task_id, body}` delivery, queue/full/failure fail-open behavior and streaming regression. |
| `Dockerfile`, `scripts/runtime_manifest_alias.py`, `scripts/build-template-from-image.py`, `scripts/runtime-smoke-test`, and their tests | Advertise and verify `llm_trace_v1` in the exact ordered manifest capability list. |

## 3. Implementation tasks

### Task 1 — Define the Agent JSON and task-exec environment boundary

**Files:**

- Create: `server/internal/service/llm_trace_config.go`
- Create: `server/internal/service/llm_trace_config_test.go`

- [ ] Create pure types instead of parsing `map[string]any` throughout launch code:

```go
const LLMTraceCapability = "llm_trace_v1"

type AgentLLMTraceConfig struct {
    Enabled bool
    SinkURL string
}

func ParseAgentLLMTraceConfig(raw []byte) (AgentLLMTraceConfig, error)
func LLMTraceEnvForTask(runtime db.Runtime, agent db.Agent) (map[string]string, bool)
```

`ParseAgentLLMTraceConfig` accepts an absent member as disabled, requires `llm_trace` to be an object when present, and rejects unknown members inside that object.  It does **not** reject unrelated root keys, so existing provider configuration remains forward-compatible.  For a capable runtime, `LLMTraceEnvForTask` always returns the two exec variables below with the saved boolean and URL (or `false`/empty defaults).  It returns `false` only for an image that lacks `llm_trace_v1`; it must not fail task launch.

- [ ] Do not add `runtime.llm_trace` to `server/pkg/runtimeconfig`, `server/cmd/server/runtime_config.go`, Diamond, or any startup environment.  The source is the Agent's saved `runtime_config`; the transport is the task's explicit exec environment.

- [ ] Start with focused RED tests:

```bash
cd server
go test ./internal/service -run 'LLMTrace|TraceEnv' -count=1
```

Cover: disabled switch produces `false` plus an empty URL; enabled switch with an empty URL; enabled switch with a non-empty URL; arbitrary `runtime_config` siblings preserved; unsupported image produces no env; environment contains no provider API key.  Assert the returned env is exactly `MULTICA_LLM_TRACE_ENABLED=<true|false>` and `MULTICA_LLM_TRACE_SINK_URL=<Agent setting or empty>`.

### Task 2 — Enforce the management boundary and retain configuration losslessly

**Files:**

- Modify: `server/internal/handler/agent.go`
- Modify/Create: the focused Agent handler tests next to the existing create/update tests

- [ ] On Agent create and update, marshal `RuntimeConfig` as today, but before persistence parse only its `llm_trace` member and validate the selected cloud Runtime's `llm_trace_v1` capability when the trace switch is enabled.  An enabled switch with an empty URL is valid and deliberately results in no trace delivery.  Do not read Diamond or require a deployment-level trace configuration.

- [ ] Require both existing Agent update permission and workspace `owner`/`admin` when the request adds, changes, or removes an enabled `llm_trace` member.  A request which replays an unchanged disabled configuration retains existing permissions.  Return a stable 403 for insufficient role and a 422-style validation error for non-cloud Runtime or missing `llm_trace_v1` capability; do not reject an empty URL.

- [ ] Preserve the current `preserveMaskedGatewayToken` behavior.  Do not deserialize/serialize through `OpenClawRuntimeConfig`: unknown JSON needs to survive an LLM-trace-only PATCH.

- [ ] Add handler tests for:

  - workspace owner enables a non-empty URL on a capable cloud Agent;
  - Agent owner who is not workspace admin/owner receives 403;
  - local Agent and old runtime image are rejected; an enabled trace with an empty URL persists and produces `true`/empty exec variables;
  - an update of `llm_trace` does not erase `gateway.token` mask behavior or unknown root keys;
  - response JSON does not contain a new secret field.

Run:

```bash
cd server
go test ./internal/handler -run 'Agent.*LLMTrace|LLMTrace.*Agent' -count=1
```

### Task 3 — Carry the per-task setting into both cloud launchers

**Files:**

- Modify: `server/internal/service/fc_e2b.go`
- Modify: `server/internal/service/asb_launcher.go`
- Modify: `server/internal/service/fc_e2b_test.go`
- Modify: `server/internal/service/asb_launcher_test.go`

- [ ] In the shared `extraEnvForTaskWithModel` path, call `LLMTraceEnvForTask` after resolving Agent/runtime/model identity.  For a trace-capable image, always add these two values to that task's explicit exec environment:

```text
MULTICA_LLM_TRACE_ENABLED=true
MULTICA_LLM_TRACE_SINK_URL=https://...
```

`MULTICA_TASK_ID` already exists in the runner environment and becomes the JSON payload's `task_id`.  Add the two new names to the FC/E2B runner extra-env allowlist and any equivalent ASB command-env validation, never to the custom user environment.

- [ ] FC/E2B must continue to form its `e2b sandbox exec --background -e ... /usr/local/libexec/multica-fc-runner ...` command with the values in its explicit environment map.  ASB must continue to pass the same environment to `POST {sandbox}/command`; do not modify the ASB platform control API or introduce a persistent per-task daemon.

- [ ] Treat a stale Agent record that names an unsupported/disabled trace configuration as no-trace and write a redacted server warning with Agent/runtime/task identifiers.  This makes deployment rollbacks safe: tasks still run rather than failing because trace is unavailable.

- [ ] Tests must assert the exact variables for both backends: disabled config is `false`/empty, enabled config is `true`/configured URL, and old image capability or non-cloud task has neither variable.  Retain assertions that `OPENAI_BASE_URL` and `OPENAI_API_KEY` still reach the runner unchanged.

Run:

```bash
cd server
go test ./internal/service -run 'FCE2B.*LLMTrace|ASB.*LLMTrace|ExtraEnv.*LLMTrace' -count=1
```

### Task 4 — Add the Agent settings experience without corrupting provider config

**Files:**

- Create: `packages/core/agents/llm-trace-runtime-config.ts`
- Create: `packages/core/agents/llm-trace-runtime-config.test.ts`
- Create: `packages/views/agents/components/tabs/llm-trace-tab.tsx`
- Create: `packages/views/agents/components/tabs/llm-trace-tab.test.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.tsx`
- Modify: `packages/views/agents/components/agent-overview-pane.test.tsx`
- Modify: `packages/views/locales/en/agents.json`
- Modify: `packages/views/locales/zh/agents.json` (use the actual current Chinese locale path if it differs)

- [ ] Implement helper functions that clone a root record and only replace/delete `llm_trace`:

```ts
export type LLMTraceRuntimeConfig = {
  enabled: boolean;
  sinkUrl: string;
};

export function parseLLMTraceRuntimeConfig(raw: unknown): LLMTraceRuntimeConfig;
export function mergeLLMTraceRuntimeConfig(
  raw: unknown,
  next: LLMTraceRuntimeConfig,
): Record<string, unknown>;
```

When disabled, `merge` removes `llm_trace` rather than storing a partially empty object.  The tests must prove that `{ gateway: { token: "***" }, future_provider_key: 1 }` remains intact.

- [ ] Add an `LLM trace` tab only when all are true: Agent `runtime_mode === "cloud"`, the selected Runtime reports `llm_trace_v1`, and the viewer is a workspace owner/admin.  Non-admin users see neither the sink URL nor a disabled-but-editable control.

- [ ] The tab contains: enable switch, receiver URL, a clear warning that raw prompts and tool inputs are sent to the receiver, and a save action that uses the normal Agent PATCH mutation with the merged JSON.  It clearly displays that tracing is inactive until both the switch is enabled and the URL is non-empty.

- [ ] Add translations which clearly say “Requests only; responses are not exported”, “Provider Authorization headers are never forwarded”, and “Delivery failure does not stop the Agent.”

Run:

```bash
pnpm --filter @multica/core test -- llm-trace-runtime-config
pnpm --filter @multica/views test -- llm-trace-tab agent-overview-pane
pnpm typecheck
```

### Task 5 — Extend the existing runtime provider proxy (separate image-repository task)

**Repository:** `/Users/wangxin/Documents/projects/work/multica-fc-hermes-runtime`
**Execution boundary:** create the separate `[HF from dt-fde-multica] LLM trace image proxy` task before changing these files.

**Files:**

- Modify: `scripts/multica-fc-hermes-runner`
- Modify: `scripts/multica-provider-http-proxy`
- Modify: `tests/multica-fc-hermes-runner-test.sh`
- Create: `tests/provider_http_proxy_trace_test.py`

- [ ] Change the immutable generation configuration from schema 1 to schema 2.  Keep every existing field and add exactly these typed fields:

```json
{
  "schema_version": 2,
  "llm_trace": {
    "enabled": true,
    "sink_url": "https://llm-trace.example.internal/v1/ingest"
  }
}
```

The runner receives the fields from Task 3, creates this file under `MULTICA_PROVIDER_PROXY_CONFIG_DIR/$MULTICA_TRACE_ID.json` with `umask 077` and `chmod 600`, and preserves the existing generation health check.  It writes disabled defaults when tracing is off.  Do not put `OPENAI_API_KEY`, `MULTICA_DAEMON_TOKEN`, custom environment values, or receiver credentials in the file.  Existing v1 config must be rejected by the new image so config/schema mismatches fail clearly during rollout rather than silently tracing incorrectly.

- [ ] In `multica-provider-http-proxy`, add a `TraceDelivery` immutable dataclass containing `task_id`, copied request body bytes, and receiver URL.  `ProviderProxyServer.__init__` creates one `queue.Queue(maxsize=256)` and two daemon worker threads.  The worker uses the image's fixed one-second delivery timeout, then logs only:

```json
{"event":"llm_trace_delivery","level":"info|warn","task_id":"...","status":204}
```

Never log raw body, destination query string, original headers, API keys, or a receiver response body.  Start workers with the server, not in a runner process; this keeps warm-sandbox overlap safe.

- [ ] In `ProviderProxyHandler.forward`, immediately after `body = self.request_body()` and **before** `open_upstream`, invoke a non-blocking `enqueue_trace(config, body, self.command, target_path)`.  It must use `put_nowait`; a full queue emits `llm_trace_dropped` and returns.  It must copy all config required by workers at enqueue time, because the runner removes the generation JSON after the task and the worker may run later.

- [ ] Send every delivery to `sink_url` using `POST` with `Content-Type: application/json` and this exact application envelope: `{ "task_id": <MULTICA_TASK_ID>, "body": <original request JSON value> }`.  Construct the envelope by encoding `task_id` as a JSON string and splicing the already-read JSON `body` value into it; do not deserialize/re-serialize the model payload.  Do not copy any client/provider headers.  Do not buffer, parse, redact, or mutate successful upstream responses; retain the existing `response.read1(64 * 1024)` / flush loop exactly.

- [ ] Make all trace failures fail-open.  The following must still reach the upstream and return its bytes unchanged: receiver DNS failure, TLS failure, timeout, 500, full queue, malformed disabled trace block, and worker exception.  A malformed *enabled* v2 config is logged as `llm_trace_config_error` and downgraded to trace-disabled; it must not be converted into `provider_proxy_not_configured`.

- [ ] Add isolated tests with a local upstream and local trace receiver:

  - the receiver gets exactly `{task_id, body}`; `body` preserves the original model request JSON (including tool definitions and whitespace/order);
  - provider `Authorization`, `Cookie`, generation header, query string, and sink response body are absent from delivered/logged data;
  - response SSE chunks are observed before the upstream finishes, unchanged by a slow trace receiver;
  - 500/timeout/full-queue cases produce expected records and the provider response remains successful;
  - generation config is mode `0600`, task-specific, and has no provider credential;
  - two overlapping generation configs cannot send to one another's receiver.

Run in the image repository:

```bash
python3 -m unittest tests/provider_http_proxy_trace_test.py
bash tests/multica-fc-hermes-runner-test.sh
```

### Task 6 — Gate on an image capability and publish compatible artifacts

**Repository:** `/Users/wangxin/Documents/projects/work/multica-fc-hermes-runtime` (same separate task as Task 5)

**Files:**

- Modify: `Dockerfile`
- Modify: `scripts/runtime_manifest_alias.py`
- Modify: `tests/runtime_manifest_alias_test.py`
- Modify: `scripts/build-template-from-image.py`
- Modify: `scripts/runtime-smoke-test`

- [ ] Replace the exact ordered capability list everywhere with:

```text
dws,dws.im_event,mcp,llm_trace_v1
```

This includes the OCI label, `/usr/local/share/multica/runtime-manifest.json`, manifest alias validator, image build verification, smoke assertion, and unit test expected value.  Keep `schema_version: 1` for the image manifest; only the provider generation config changes to v2.

- [ ] Build and smoke-test the image, then publish new FC and ASB templates/artifacts carrying this manifest.  Capture the immutable artifact/template identifiers and the manifest output.  Do not alter or re-label the old stable image as trace-capable.

- [ ] In Multica, extend/update existing runtime-capability tests so a runtime whose metadata lacks `llm_trace_v1` cannot save enabled configuration and cannot receive trace env.  Only change stable runtime metadata after its corresponding image has passed smoke tests.

### Task 7 — Document receiver integration, operations, and rollout

**Files:**

- Create: `docs/llm-trace.md`
- Modify: `docs/product-overview.md`

- [ ] In `docs/llm-trace.md`, document the exact `{task_id, body}` wire contract, 2xx success rule, duplicate semantics, sensitive-data classification, downstream retention/access obligations, and explicit no-response-capture scope.  Include a receiver example that reads the JSON body without showing a bearer token.

- [ ] Update the cloud-task diagram/description in `docs/product-overview.md` to show the existing loopback provider proxy as the LLM egress boundary.  Do not claim that the Multica server itself calls the LLM.

- [ ] Add this audit record at the end of every changed protocol document:

```markdown
## 历史记录

| 日期 | 版本 | 改动 | 原因 |
|---|---|---|---|
| 2026-08-06 | llm-trace-v1 | 新增云沙箱 LLM 请求镜像协议与按 Agent 开关 | 在不改变模型调用和流式响应的前提下提供可关联的调试/审计 trace，同时限制 prompt 外发目的地 |
```

### Task 8 — Integrated acceptance and staged rollout

**Files:** no new production code; use a dedicated non-production Agent, a test runtime image, and a controlled HTTPS receiver.

- [ ] Verify configuration safeguards before enabling any receiver: ordinary Agent owner cannot enable; an enabled switch with an empty URL produces no receiver POST; an old image remains no-trace; and Diamond/runtime configuration is neither read nor required.

- [ ] Deploy the image capability first to one non-production FC template and one non-production ASB runtime.  Verify the manifest has `llm_trace_v1`, then update corresponding Multica runtime metadata.  Only then save the Agent switch and receiver URL; verify that each task's exec environment carries those two values.

- [ ] Create one capable cloud Agent, enable trace, run one task with a streaming model response and one forced provider retry/error.  Assert at the receiver:

  - every model invocation arrives as a JSON payload with the expected `task_id` and `body`;
  - `body` visibly contains the known prompt and tool schema;
  - no provider authorization/cookie reaches the receiver;
  - token streaming timing and task result are unaffected;
  - trace receiver 500/timeout does not alter task completion.

- [ ] Observe `llm_trace_delivery`, `llm_trace_dropped`, and `llm_trace_config_error` records for 24 hours in non-production.  Define alerts on non-zero configuration errors and a sustained delivery-failure/drop rate; do not alert on a single best-effort timeout without rate context.

- [ ] Promote in this order: runtime image artifact → FC template/ASB runtime metadata → Multica server that understands the Agent config/capability → selected Agent switches.  Roll back by turning off the affected Agent switch or clearing its URL; active tasks continue with their existing provider path and new tasks receive no trace env.

Final focused verification commands:

```bash
cd /Users/wangxin/.codex/worktrees/25e9/dt-fde-multica/server
go test ./internal/service ./internal/handler -run 'LLMTrace|Agent.*RuntimeConfig' -count=1

cd /Users/wangxin/.codex/worktrees/25e9/dt-fde-multica
pnpm --filter @multica/core test -- llm-trace-runtime-config
pnpm --filter @multica/views test -- llm-trace-tab agent-overview-pane
pnpm typecheck

cd /Users/wangxin/Documents/projects/work/multica-fc-hermes-runtime
python3 -m unittest tests/provider_http_proxy_trace_test.py tests/runtime_manifest_alias_test.py
bash tests/multica-fc-hermes-runner-test.sh
```

## 4. Delivery checkpoints

1. **Multica contract checkpoint:** Agent-config parser/handler tests are green; no Diamond configuration exists and no image capability claims are made yet.
2. **Image checkpoint:** standalone runtime proxy tests prove fail-open `{task_id, body}` delivery and unchanged streaming; a new immutable image manifest advertises `llm_trace_v1`.
3. **Integration checkpoint:** FC/E2B and ASB launcher tests verify the same task-scoped input reaches the runner and old images are ignored safely.
4. **Product checkpoint:** UI is role/capability gated, preserves unrelated runtime config, and receiver contract/docs have passed review.
5. **Pre-production checkpoint:** controlled end-to-end trace matches task correlation and a failing receiver cannot change model task outcome.

## 5. Historical record

| Date | Version | Change | Reason |
|---|---|---|---|
| 2026-08-06 | plan-v3 | 移除 Diamond 方案；开关和 URL 仅由每次 sandbox exec 的环境参数传给 runner。 | trace 是 Agent 级任务参数，避免增加部署级配置和开关。 |
| 2026-08-06 | plan-v2 | 收敛投递条件与 payload：仅开关开启且地址非空时，POST `{task_id, body}` JSON。 | 需求是直接分析模型实际请求内容，不需要额外的 trace headers 或 response capture。 |
| 2026-08-06 | plan-v1 | Chose the existing sandbox provider reverse proxy over a new mitmproxy sidecar; specified Agent setting, runtime protocol, fail-open behavior, capability rollout, and two-repository boundary. | The warm cloud sandbox already has a generation-isolated loopback interception point, so reuse gives safer tracing with no extra proxy lifecycle or SSE buffering risk. |
