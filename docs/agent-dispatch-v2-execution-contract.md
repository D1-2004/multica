# Agent Dispatch V2 Execution Contract

Agent Dispatch V2 is a structured, authenticated command. Multica must execute
its fields as independent policies instead of inferring one policy from another.

## Surface

`surface.type` is the only field that selects the Multica execution mode. Each
mode has an explicit initial persistence materializer:

- `issue` creates an Issue or appends a follow-up to the referenced Issue.
- `chat` creates or reuses a chat session and appends a user message.
- `auto` preserves automatic execution policy as a first-class mode while
  initially materializing the channel message in a chat session. Multica adds
  private foreground-coordination instructions so the Agent can either finish
  lightweight work in Chat or delegate durable work to an Issue.

`auto` currently applies to `channel/message.created`. The
`calendar/calendar.started` and `approval/approval.status_changed` contracts
remain Issue-only because neither has a foreground Chat session to release.

Channel slash commands do not override this choice. In particular, text such as
`/issue`, `/new`, `/reset`, or `/unbind` remains prompt content when delivered by
Agent Dispatch V2.

The response always returns the latest continuation produced by the selected
surface. A recreated missing Issue or chat therefore replaces a stale
continuation for the Router to persist.

Continuation kind identifies the materialized locator, not the execution mode.
Both `chat` and `auto` therefore return a `chat` continuation containing
`chatSessionId`; `issue` returns an Issue continuation.

For DingTalk channel messages, a newly created Chat title includes enough topic
context to distinguish repeated conversations with the same sender. Private
messages use `sender：opening summary`; group messages use
`conversation · sender：opening summary`, with missing group or sender fields
omitted. The summary is NFKC-normalized, strips control and format characters,
collapses whitespace, and the complete title is capped at 160 Unicode runes.
On an existing session, only older machine-derived titles such as `sender` or
`conversation · sender` may be upgraded on the next message; manual and LLM
titles are never overwritten by channel ingestion.

## Identity

The webhook bearer credential authenticates an Agent Dispatch endpoint and
resolves its endpoint actor, workspace, and Agent. That endpoint actor is the
Multica principal used to create sessions, persist messages, and authorize task
execution.

`externalIdentity` carries optional execution-identity inputs. The existing
`contextToken` and `expiresAt` pair remains supported: when `contextToken` is
present, `expiresAt` is required and is Unix epoch milliseconds. The same object
may also contain `dws={uid,orgId}`. DWS `uid` and `orgId` must be supplied
together as decimal identifiers, and `dws` may be supplied without a legacy
ContextToken.

Multica stores the legacy token in the dedicated private
`agent_identity_context_token` field with
`agent_identity_context_token_expires_at` and
`agent_identity_context_token_source=external`. The stable DWS descriptor is
stored separately as private `external_identity.dws`. Chat, Issue, Chat-to-Issue
delegation, and Issue follow-up preserve that private descriptor; ordinary task
responses, Issue/comment content, metadata, broadcasts, and logs do not expose
it.

Cloud-sandbox launch uses one identity resolver after queue-serialization
blocking, sandbox resolution, and runner probing, but before `execRunOnce`. For
a DWS-capable runtime its priority is:

1. the Multica Agent's local DWS binding;
2. private `external_identity.dws` from the dispatch;
3. the legacy task ContextToken.

GitHub identity is orthogonal to that ordering. A stable DWS identity creates
one Agent Identity context with DWS and optional GitHub identities at TTL 900.
Without stable DWS, a legacy ContextToken is validated and reused; optional
GitHub identity is appended with `ExtendContext`. With neither stable DWS nor a
legacy token, an available GitHub binding creates a GitHub-only context. An
expired external token, a token inside the one-minute execution safety window,
or an incomplete token/expiry pair fails runtime startup. This resolver is not
used by the standalone daemon.

The local-clock check applies only when reusing a token read from existing task
context. Once Multica calls the server-side Agent Identity HSF interface, its
returned ContextToken is authoritative for the current launch and is not
checked again against the local clock. Task-context preparation carries the
HSF-returned ContextToken and `expiresAt` together so later reuse can apply the
stored-token policy.

DingTalk sender identifiers remain message attribution and reply-target data.
They do not become the Multica principal and do not trigger ordinary channel
sender binding on this authenticated path.

Direct DingTalk Stream or callback ingestion outside Agent Dispatch V2 retains
the existing sender-binding policy.

## Prompt

Multica builds prompt material from the structured source event:

- display content contains user-visible message and attachment descriptions;
- `contextPrompt` contains credential-free dynamic execution facts rendered by
  Router for this delivery, including the outbound mode and any trusted DWS
  reply target;
- fixed safety, delivery, response, and routing policy comes from Diamond
  `common.prompt` plus the prompt for the current `surface.type`.

The daemon claim task accepts an optional `instruction` string. When it is
non-blank, the daemon prepends it to the generated per-task prompt for every
task kind. It does not write the value into the built-in runtime brief. A
missing, empty, or whitespace-only value is a byte-for-byte no-op.

An instruction-capable daemon advertises `task-instruction-v1` through
`X-Client-Capabilities` on both HTTP claims and the WebSocket control
connection. Multica selects the delivery projection from the capability on the
actual claim request, not from persisted runtime metadata or the runtime
version string. An instruction-capable daemon receives the new Diamond plus
Router composition through `instruction`. A daemon without that capability
uses the previous prompt builder and receives its output through the existing
`handoff_note`, `trigger_comment_content`, or `chat_message` claim field it
already consumes. The two builders and transports are mutually exclusive.

At claim time Multica composes that field as `common.prompt + current mode.prompt
+ contextPrompt`, skipping blank sections and separating non-blank sections with
two newlines. The composition is claim-scoped, so Diamond updates apply to tasks
that have not yet been claimed. Persisted Issue descriptions, trigger-comment
content, chat messages, and assignment handoff notes remain unchanged. Only the
claim response for a legacy daemon temporarily prefixes the matching task field
as a compatibility transport.

If all three new composition inputs are blank, an instruction-capable daemon
receives no task instruction. This does not switch it onto the legacy builder.
Conversely, a daemon without `task-instruction-v1` always uses the legacy
builder, even when Diamond common or Router context sections are available.

Event projection in the prompt builder is independent of `surface.type`. The
same structured event can therefore run as an Issue, Chat, or Auto mode without
moving prompt assembly back into the Router.

The mode remains `auto` in persisted dispatch context and audit data even though
its initial materializer is Chat. A successful Issue delegation transfers the
dynamic Router context, changes the target task's private dispatch surface to
`issue`, and recomposes `common + issue + context` when the child is claimed.
The child therefore does not receive the automatic front-stage policy.

## Outbound

`outbound.mode` is the only field that selects outbound ownership:

- `dws` delegates acknowledgement and final DingTalk delivery to the Agent's
  DWS capability according to the configured fixed prompt and Router's dynamic
  context. Multica suppresses server-side typing and robot replies for that
  dispatch.
- `robot_sdk` keeps Multica's channel typing and robot reply lifecycle enabled.

Outbound selection is independent of source type and surface. Issue plus DWS,
chat plus DWS, Issue plus robot SDK, and chat plus robot SDK remain valid
compositions subject to the command's ordinary validation.

## LLM telemetry and terminal summary

`completionCallback` may carry a task-scoped telemetry capability in addition
to its existing terminal and update callback paths:

```json
{
  "url": "/api/v1/dispatch-tasks/dispatch-1/execution-result",
  "updateUrl": "/api/v1/dispatch-tasks/dispatch-1/execution-update",
  "telemetryUrl": "/api/v1/dispatch-tasks/dispatch-1/llm-traces",
  "telemetryToken": "opaque-task-write-capability",
  "telemetryExpiresAt": 1786377600000
}
```

The telemetry fields are optional as a group. When present, `telemetryUrl` is
a trusted relative Router path without query or fragment; all three callback
paths must identify the same Router dispatch task. The token is an opaque
Bearer value and `telemetryExpiresAt` is Unix epoch milliseconds. Multica
stores the Router path and capability only in private task context. The cloud
sandbox receives an absolute HTTPS Multica task-relay URL plus its existing
short-lived daemon token, so it reuses the same reachable control-plane origin
as task messages, usage, and completion. Multica authenticates the daemon,
checks task/Runtime ownership, resolves the stored relative Router path against
its existing Router Internal Base URL, and forwards the payload over the
internal network. None of the Router telemetry values is written to task
content, sandbox environment, response payloads, command arguments, or logs.

In deployments using the existing Sandbox Relay, the runtime maps that
same-origin Multica endpoint through its per-task loopback egress relay. The
edge verifies the sandbox assertion and daemon-token digest before Multica
applies ordinary `DaemonAuth` and task ownership checks. The sandbox assertion
remains outside provider proxy configuration.

Agent `runtime_config.llm_trace.enabled` remains the delivery switch. A complete
Router telemetry capability and the Agent's configured static sink are
independent destinations. Multica fans out one sandbox submission to both when
both are present. A static sink receives the unchanged trace JSON without a
Router or daemon token. Missing or disabled configuration produces no trace
delivery.

The terminal `execution-result` request now also accepts an optional
`executionSummary`. Multica freezes the same task timing, usage, message/tool
counts, and runtime/sandbox shape exposed by its task summary endpoint into the
completion outbox in the terminal transaction. Every retry sends that immutable
snapshot, allowing Router to persist Agent environment data without making a
post-terminal summary/messages request back to Multica.

## History

- 2026-07-22: Separated surface, authenticated principal, prompt projection,
  and outbound ownership for Agent Dispatch V2. Chat dispatch no longer reuses
  ordinary channel sender binding, and channel commands can no longer override
  the requested surface.
- 2026-07-27: Added the required `externalIdentity.expiresAt` field and the
  private task-context expiry field. Runtime startup now rejects incomplete,
  expired, or one-minute-to-expiry ContextTokens instead of reusing them.
- 2026-07-27: Distinguished request-supplied external tokens from refreshable
  task-context cache entries. Expired or near-expiry cache entries now refresh
  from the Multica Agent binding; external identity never silently changes.
- 2026-07-27: Limited local expiry checks to task-context reuse. Tokens returned
  by a new server-side HSF call are trusted for that launch, while the returned
  expiry continues downstream with task-context-prepared tokens.
- 2026-07-27: Raised the Multica Agent DingTalk binding above external and
  cached task-context tokens in the runtime identity decision. Task context is
  now consulted only when the Agent has no local identity binding.
- 2026-07-30: Added `auto` as a first-class Agent Dispatch mode. Channel
  messages retain `surface.type=auto`, initially materialize as Chat, and
  receive private foreground-coordination and Issue-delegation instructions.
- 2026-08-04: Added backward-compatible daemon consumption of the optional
  task-level `instruction` field. Non-blank values are prepended to the task
  prompt; absent or blank values preserve the existing prompt exactly.
- 2026-08-04: Added top-level `contextPrompt`, Diamond `common.prompt`, and
  claim-time `common + mode + context` composition into task `instruction`.
  Removed Multica's hard-coded DingTalk safety/delivery prompt generation and
  stopped rewriting user-visible task fields with private instructions.
- 2026-08-06: Added the `task-instruction-v1` daemon capability and a
  capability-gated claim projection so prompt construction can follow the
  actual daemon consumer instead of a runtime version guess.
- 2026-08-06: Split prompt construction by daemon capability. New daemons use
  only `common + mode + contextPrompt`; older images always use the previous
  structured DingTalk prompt builder and legacy task-content transport.
- 2026-08-07: Added the opening-message summary to DingTalk Chat titles while
  retaining CAS protection for manual and LLM titles.

## Reason

The previous chat path reused ordinary DingTalk robot ingestion end to end. It
therefore attempted sender binding before creating a chat and coupled DWS versus
robot behavior to hard-coded source/surface pairs. This caused authenticated
digital-employee dispatches to be acknowledged without creating a conversation.
Independent policies make the explicit dispatch fields authoritative and keep
login identity, Agent execution identity, and external message sender distinct.

The expiry addition closes a lifecycle gap where a ContextToken could be copied
into task context without its termination time. A delayed or resumed task could
therefore prefer an opaque stale token over acquiring a usable execution
identity. Keeping the token and expiry together makes reuse explicit and
bounded.

The source marker makes the fallback boundary auditable. Without it, a token
copied from an external dispatch and a token cached by Multica were
indistinguishable after persistence, so an expiry policy could either fail
refreshable tasks unnecessarily or replace an external execution identity with
the Agent's local binding.

Treating HSF as the authority for newly issued tokens avoids rejecting a
successful server response because of local clock or safety-window comparisons.
The returned expiry remains lifecycle metadata for a later cache-reuse
decision, rather than a second acceptance gate on the same HSF call.

Giving the explicit per-Agent binding highest priority makes the execution
identity configured in Multica authoritative. External and cached tokens remain
fallback inputs for unbound Agents, so this change only reorders identity
selection and does not alter the task-context protocol.

Keeping `auto` distinct from `chat` preserves the binding decision in task
context, logs, and future policy evolution. Separating mode from materializer
allows the current implementation to reuse durable Chat sessions and Chat
continuations without erasing the fact that automatic delegation policy applies.

The task-level `instruction` field separates runtime-delivery policy from
user-visible issue, comment, and chat content. Rolling out its daemon reader
first is safe because existing claim responses omit the field and therefore
retain the previous prompt without modification.

Separating the three composition inputs keeps fixed policy dynamically
configurable in Diamond and leaves event-specific delivery facts with Router.
Claim-time assembly ensures Issue comments and continued Chat tasks see the
current policy, while an Auto-to-Issue handoff selects the Issue policy without
parsing or rewriting Router's context string.

## Change record: 2026-08-04

- History: Added optional paired `externalIdentity.dws.uid/orgId`, private stable
  DWS propagation, and a single cloud-sandbox pre-start identity resolver with
  local DWS binding > external DWS > legacy ContextToken priority. GitHub remains
  an orthogonal identity and is combined through one create or extend operation.
- Reason: A stable upstream DWS identity must survive delayed, delegated, and
  coalesced execution without exposing credentials, while the final short-lived
  ContextToken must still be minted against the sandbox that will actually run.

## Change record: 2026-08-06

- History: Added the digital-employee `approval/approval.status_changed` Issue
  dispatch. Approval callbacks validate their form and approver data, use a
  stable approval status idempotency key, skip `auto_approve` nodes without an
  Agent task, and recover the `关联Issue` identifier to continue the original
  Issue when its Agent matches. `dws` and `robot_sdk` outbound modes reuse the
  existing trusted reply workflow; `none` keeps approval data outbound-free.
- Reason: Approval lifecycle callbacks must preserve the originating Issue and
  sender policy without exposing raw approval content in observability logs or
  allowing a delayed task to lose its external DWS identity.

## Change record: 2026-08-06 task instruction composition

- History: Router `contextPrompt`, Diamond `common.prompt`, and the persisted
  task surface are composed at claim time into the private task `instruction`.
  Issue, comment, and Chat display fields remain unchanged; delegated Issue
  tasks carry both the Router context and `externalIdentity.dws` privately.
- Reason: Delivery policy must remain current at task start without exposing
  trusted DWS routing data in user-visible content or dropping the stable DWS
  identity during a surface handoff.

## Change record: 2026-08-06 runtime compatibility

- History: Capability-gated the claim response. Daemons advertising
  `task-instruction-v1` receive the composed private `instruction`; older
  daemons receive the legacy prompt through the claim-only Issue, comment, or
  Chat input field. Persisted user-visible content remains unchanged.
- Reason: Rolling deployments must preserve complete prompt behavior while old
  runtime images still ignore `instruction`, without mixing legacy policy into
  new images or duplicating instructions.

## Change record: 2026-08-07 DingTalk Chat title projection

- History: DingTalk private Chat titles now use `sender：opening summary`; group
  Chat titles use `conversation · sender：opening summary`. Older sender-only or
  group/sender machine titles are eligible for one safe in-place upgrade.
- Reason: Sender identity alone produces multiple indistinguishable Chat rows
  for repeated digital-employee conversations. Adding a normalized opening
  summary preserves the channel counterpart while making the conversation topic
  visible, without overwriting user-managed titles.

## Change record: 2026-08-07 LLM trace observability

- History: Added the optional task-scoped LLM telemetry URL/token/expiry
  callback and the optional immutable `executionSummary` terminal payload.
- Reason: The sandbox needs a narrow, expiring write target for paired model
  traffic, while Router needs Agent environment data without a post-terminal
  pull whose runtime snapshot may already have changed or expired.

## Change record: 2026-08-08 Multica LLM trace relay

- History: Changed `completionCallback.telemetryUrl` from a separately
  configured absolute Router URL to a trusted relative path. The sandbox now
  posts paired trace payloads to an absolute HTTPS Multica task endpoint;
  Multica validates the stored task capability and forwards the unchanged body
  to Router through the existing internal Router client.
- Reason: Cloud sandboxes can already reach the Multica control plane but may
  not reach a private Router staging or production ingress. Reusing the
  existing callback origin removes the unnecessary telemetry-Origin setting
  and keeps raw model traffic off an additional public network path. The
  per-task Sandbox Relay assertion is also preserved as the first-hop network
  boundary rather than exposing it to the provider proxy.

## Change record: 2026-08-08 Multica trace fan-out

- History: Replaced Router-capability authentication at the sandbox boundary
  with the existing short-lived daemon token. The Multica task endpoint now
  authenticates through `DaemonAuth`, verifies task/Runtime ownership, and
  independently forwards the same pair to Router telemetry and the Agent's
  configured static sink.
- Reason: A Router callback must not override the Agent's original trace
  destination, and private Router capabilities should remain server-side while
  sandbox traffic reuses the proven task lifecycle control-plane channel.
