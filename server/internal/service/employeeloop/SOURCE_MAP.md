# EmployeeLoop source map

This directory contains a modified, direct copy of the Go BotLoop sources from
`/Users/mac-m3/github/gawkbot`, fixed commit
`71e82a1809565281cbd0bf8185d3c125b715d934` (Nex / Wuphf).
The original files were retrieved with `git show <fixed-sha>:<path>`, copied into
this package, and adapted in place. The code is an active, callable kernel, not
an unused reference snapshot. All modifications below are by the Multica project.

The original [LICENSE](LICENSE) is included verbatim. It is the Sustainable Use
License, not an MIT/Apache grant; the original copyright and use/distribution
terms remain applicable. See [NOTICE](NOTICE).

| Fixed source | Active local code | Retained and adapted | Deliberately removed |
| --- | --- | --- | --- |
| `internal/bot/types.go` | `types.go` | Phase lifecycle, config/state, named tool, tool call and session entry contracts; adapted to typed Model/Host and trusted admission Identity | Local bot budget/cron state, provider stream chunk transport, credibility/escalation types |
| `internal/bot/loop.go` | `loop.go` | `Tick` switch is now `tick`; idle → build context → model → tool batch → model/done; `GetState`, `Interrupt`, `buildContext`, tool validation and result recording, `handleDone` | UI events, filesystem execution logs, compaction, indefinite service ticks, implicit EOF completion, finish/composer passes |
| `internal/bot/queues.go` | `queues.go` | Message queues copied with package/header changes; the active `Run`/`buildContext` path enqueues and drains human input and follow-ups | No durable queue ownership is claimed; queues are scoped to one wake |
| `internal/bot/tools.go` | `tools.go` | `ToolRegistry`, register/get/list/has/unregister, required/unknown argument checks; list/errors are sorted and required accepts typed string slices | Shell, filesystem, message sending and all built-in execution tools; Host owns capabilities |
| `internal/bot/session.go`, `internal/bot/loop.go::entriesToMessages` | `session.go` | Append/read session history and role switch; tool batches/results use native OpenAI messages and exact native IDs | JSONL paths, local file persistence, branch/list/delete sessions; durable history belongs to Host |
| `internal/bot/service.go` | `service.go` | Constructor dependency assembly, per-loop ownership and cancellation; service tick driver becomes bounded `Run(ctx, input)` | Singleton bot map, global timers, bot CRUD, local task workers, shell/provider resolution |
| `internal/team/prompt_builder.go::Build` direct-session branch | `prompt_builder.go::BuildPrompt` | Pure stable string builder, copied persona/expertise/voice headings, direct reply and concise conversational voice; stable sorted expertise snapshot | Office roles, mandatory Issue creation, wiki/HTML policies, team broadcast/poll, forced interviews |

## Behavioral corrections during adaptation

- The upstream single `pendingToolCall` and first-tool `goto done` become a full
  native call batch. Malformed/duplicate native call identities reject the whole
  batch before any Host execution. Every executed tool result retains its ID.
- Upstream streaming channel closure is not proof of success. This Chat-based
  adapter requires `finish_reason=stop`, nonempty text and no pending tool call
  for a natural Reply. Missing/length/content-filter completions never become a
  Reply. `tool_calls` completion requires a complete valid native batch.
- `Run` permits at most three calls to Model.Chat, counting request failures and
  invalid-format repair together. There is no hidden completion/check/composer
  or GenerateJSON helper. Injected `pkg/llm.Client` MUST be constructed with
  `llm.Config{MaxRetries: -1}`; the HTTP test proves three failures produce three
  actual requests. Durable restart wrappers must retain the same wake budget.
- One wake context reaches both Model.Chat and Host.Execute. State/cancellation
  locks are never held around either slow call. An interrupted loop can accept
  the next wake after the canceled run exits.
- Host tools may declare a terminal disposition. The kernel rejects a batch
  combining any effect with a no-effect terminal before executing any Host call.
  The invalid batch uses the existing three-call format-repair budget; valid
  reads and multiple dispatches keep their original semantics. The scene Host
  exposes `reply` as an explicit no-task terminal alongside normal text replies;
  `dispatch_task.reply` is only the acknowledgement of real background work.
  PostgreSQL regressions observed both call orders creating a task before this
  guard; they now verify no EmployeeTask, run, or new queue row is created.
- Only Host receipts prove irreversible effects. An effect tool without a
  receipt fails. An accepted dispatch may return its terminal disposition and
  human reply together; no second model call is needed. Full tool batches still
  finish recording their other results. Compatible terminal results are aggregated
  only after every tool in the batch is recorded: multiple accepted dispatches
  retain each receipt and produce one reply from their distinct replies in order.
  `Outcome.ToolOutcomes` preserves every Host result, including terminal data,
  content and errors, before checking cancellation or terminal compatibility.
  Incompatible terminal kinds return `ErrTerminalConflict` with an empty final
  Decision; their already committed facts remain in the returned Outcome. Any
  effect error, even without a receipt or from parameter validation, fails the
  overall batch explicitly while preserving accepted and rejected tool outcomes.
  A previously accepted dispatch cannot turn a later rejected effect into success.
- Identity is a separate typed parameter with a `scene.Ref`; model text never
  supplies tenant/scene permissions. Memory, task briefs and conversation windows
  are user-message data. Dispatch `source_ref` must come from frozen Host-provided
  per-utterance references, and is a locator rather than authority.
- The Host must validate business arguments, enforce current tenant permission,
  deduplicate durable effects, and persist dispatch receipts/outcomes. The kernel
  has no database, inboundcoord dependency, dispatch executor or fallback path.

## Migrated tests and verification

- `internal/bot/queues_test.go`: copied FIFO, empty queues, queue presence, human
  queue isolation, concurrent enqueue/drain and separate-agent tests verbatim
  except package/header; active queue usage is also exercised by Run tests.
- `internal/bot/tools_test.go`: copied the ten ToolRegistry tests, with package
  and `BotTool` → `Tool` substitutions; shell/local execution tests were omitted
  because those capabilities were removed.
- `internal/bot/loop_test.go::TestFullTickCycle`: adapted to public Run and its
  explicit completion result. `TestStreamLLMReceiveStopsOnCancel` was adapted to
  context-aware Model.Chat and extended to slow Host tools.
- `internal/bot/session_test.go::TestSessionCreateAppendRead`: adapted to the
  per-wake append/read store. Filesystem/branch tests no longer apply.
- New fake-model tests cover one-call reply, one-call accepted dispatch+reply,
  two-call read+reply, full native tool ID pairing, no fourth model call,
  EOF/truncation rejection, missing effect receipt, and prompt data separation.
- The P1 batch regression was observed failing with two accepted dispatches and
  an unexecuted trailing read. New regressions cover multiple terminal dispatches,
  reply aggregation/deduplication, incompatible terminal kinds without losing
  committed facts, and Host commits returned alongside cancellation or errors.
- New `httptest` test uses actual `pkg/llm.Client`, with no account or live network.

The initial behavior tests were observed failing against an empty `Run` scaffold
before implementing the adapted flow. Typed-required validation and the source
reference prompt contract were separately observed failing before their fixes.
Run the current evidence with:

```sh
cd server
go test -race ./internal/service/employeeloop -count=1
go vet ./internal/service/employeeloop
```

## Optional real-model smoke

`model_smoke_test.go` is excluded from default tests by the `employeeintegration`
build tag. The real-model test additionally requires
`MULTICA_RUN_EMPLOYEE_MODEL_SMOKE=1`; it reads only the dedicated
`EMPLOYEE_MODEL_BASE_URL`, `EMPLOYEE_MODEL_API_KEY`, and `EMPLOYEE_MODEL_MODEL`
environment variables. It does not discover an account or deployment config.

The two cases require a first-call greeting Reply and a first-call
`dispatch_task` acceptance with a Host-provided `source_ref`. The Host is fake:
no real task or external effect is created. The configured client uses
`MaxRetries: -1`, and a shared HTTP transport enforces at most three actual
requests across both cases combined. Output contains only counters/dispositions,
never credentials, raw headers, provider errors or model content.

After separately providing the three dedicated model variables, an authorized
operator can run:

```sh
cd server
MULTICA_RUN_EMPLOYEE_MODEL_SMOKE=1 go test -tags employeeintegration \
  ./internal/service/employeeloop -run '^TestEmployeeModelSmoke$' -count=1 -v
```

The optional tagged local fixtures can be verified without a live model account:

```sh
go test -tags employeeintegration ./internal/service/employeeloop \
  -run '^TestEmployeeModelSmoke(WithLocalProvider|HTTPBudget)$' -count=1
```

## Host observability and scene capabilities (2026-10-03)

`Config.OnBatchRejected` is an optional observer called after the existing pure
batch validation rejects a request. It does not change validation, execute a
Host tool, add a model call, or persist domain effects. The Host owns Langfuse
integration in `handler/employee_scene_trace.go`; real model and tool I/O is
observed at the durable journal boundaries, not on cached replay.

The scene Host reuses the application's `contextcap` merger and configuration
link issuer. `describe_capabilities` remains a no-task terminal and keeps bearer
links in a private delivery record outside model-facing tool results.
`scene_config_get` takes its identity from the admitted job, never model scope
parameters. Existing Direct scene-management MCP handles actual configuration
changes. These are application adapters; the copied GawkBot kernel gains no
storage, permission, or provider-specific implementation.

The Host adapts the deterministic completion hook with an explicit, source-bound
file-delivery notice policy and provider-verified receipts. This delivery policy
is not kernel inference, does not inspect final prose, and adds no model request.


## Requester-private memory Host tools

The Multica scene Host now registers memory_capture and memory_forget as
nonterminal effects, plus read-only memory_lookup. This reuses the existing
native-tool/result loop and three-call cap; it introduces no model pass, Task,
scheduler or generic kernel memory authority. Host evidence/scope is resolved
from frozen sources and current authorization. The journal can project cached
memory results through current state without repeating effects. Old Config.Tools
snapshots remain frozen during rolling upgrades (Employee replica marker 5).


New scene input snapshots also freeze narrow memory-reply guidance: obey explicit
output constraints, answer only the requested fact, avoid unrelated memory lists
or offers to read another scene's private memory, and confirm forgetting without
repeating forgotten content or internal state fields unless audit details were
requested. This is Host Persona/tool-description content, not a change to the
shared BuildPrompt renderer or the copied kernel. Existing snapshots and model
journals retain their original bytes; authority, tool results and call limits are
unchanged (replica marker remains 5).

## Terminal execution facts outside the kernel

The fixed source's `internal/bot/loop.go:handleDone` emits completion without
creating a new follow-up; `internal/team/headless_event.go` separates terminal
facts from subsequent work, and `internal/bot/queues.go:FollowUp` is explicit.
Multica adapts that separation in `handler/employee_execution_event.go` and
`employeeentry/execution_fact.go`: committed Direct Run facts get a durable
admission/consumption, with no model job, generation, or additional delivery.
Original receipt, run ledger, and committed dispatch checkpoint establish the
source. Existing PostgreSQL reconciliation and notice ownership remain in the
Host; no scheduler or event sink is added to the copied kernel. The additive
no-job records are readable during marker-5 rolling upgrades.

## Bounded recent conversation snapshots

`internal/bot/session.go:SessionStore.GetHistory` at the fixed source preserves
ordered recent dialogue; `internal/team/notification_context.go` builds bounded
per-recipient context from broker reads. Multica keeps that separation with a
read-only Host projection in `employeeentry/recent_history.go` and
`handler/employee_recent_context.go`. New wakes may carry `Input.RecentConversation`
as user-role data before the current window; empty historical snapshots keep
their original prompt bytes. This is neither a summary-model call nor a write to
long-term memory. User text must have admitted scene/principal provenance;
assistant text additionally requires a provider-confirmed Host response tied to
that principal's source. The projection states its fixed watermark, 24-hour
window, message/byte bounds, and truncation. It does not claim complete provider
history, authorize new work, or re-read history during a journal replay.

New Host Persona snapshots additionally freeze chronological interpretation:
the latest explicit facts/reset replace older assignments for the same objects,
partial updates retain unaffected facts, and references use the nearest relevant
exchange. Older assistant output cannot override newer user statements, and past
requests are not new execution commands. This does not rewrite stored dialogue,
change the shared BuildPrompt or request profile, or upgrade existing snapshots.
Assembly/replay tests verify those boundaries; model semantics require real IM
evidence rather than a canned model answer.

Retired private memory suppresses only its exact scoped message evidence and
the original job's associated replies in new projections. Audit rows stay intact;
other messages, including ordinary temporary corrections, remain dialogue.
The snapshot marks omitted withdrawn evidence. Callback correlation uses both
the frozen response URL and exact synchronous request ID; independent Run
notices keep their own source proof. No insight-value search or global erasure
is performed: later restatements without structured backreferences are outside
this filter's coverage.


## Configured model selection at the Host boundary

The fixed source's model interface remains one call per loop turn. Multica's
`handler/employee_model_route.go`, durable `employeeentry` model journal, and
`modelregistry/employee.go` adapt that boundary to the existing Coordinator
configuration. New wakes freeze only candidate references and configuration
revision. Each uncached request rechecks current provider authorization and
credentials, and the journal saves the fallback cursor before subsequent turns.
The single-request adapter never invokes Coordinator's internally retrying route,
so the kernel's three-turn budget bounds actual HTTP calls as well. Failed
preparation consumes a reservation but creates no generation; cached responses
create neither new requests nor effects. Historical snapshots keep their original
request bytes. This model plan and the bounded recent-history snapshot share
marker 6; no copied-kernel scheduler, model router, or follow-up pass is added.
