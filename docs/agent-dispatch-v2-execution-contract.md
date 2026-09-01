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

### Inbound short loop (reply vs issue)

Before a sandbox starts, Multica runs a **bounded server-side tool loop**
(`assoc_recall` / `assoc_bind` / `finish`, thinking off, at most eight model
rounds, last round finish-only, `tool_choice=required`) on web Chat and on
DingTalk `channel/message.created` for digital employees and robots. A verdict
is only accepted from the `finish` tool. Graph questions (what a
`conversation_id` is following, which matters exist) must call `assoc_recall`
with that exact id; empty items means unknown. Only work that needs DWS,
search, files, or tracking becomes an Issue and starts a sandbox.

The loop does not expose DWS, search, or news as model-callable tools and is not
behind a feature flag. On robot and digital-employee turns, the server resolves
the inbound `uid/orgId`, mints a **separate** short-lived Agent Identity context,
redeems it in a per-request `DWS_CONFIG_DIR`, and runs `dws chat message list`
for the authoritative current DingTalk conversation before the first model round.
The task/sandbox ContextToken is never consumed. The current message is removed and
the previous latest 10 messages are supplied chronologically. A reply message keeps
its `quotedMessage` sender and content inline with that history entry. Each request owns
and deletes its credential directory, so concurrent users cannot share a DWS
profile. A DWS identity or read failure continues the existing sandbox enqueue;
it never substitutes Multica's chat projection or the Router dispatch window.
Web Chat does not load DingTalk history. It uses `qwen3.7-plus` with thinking off
and a 45s wall clock shared by DWS history and all model rounds. Agent
setting `inbound_coordinator` is on by default for new and existing agents; an
explicit owner off switch skips the loop and enqueues the sandbox.

| Action | User sees | Sandbox |
| --- | --- | --- |
| `reply` | One complete sentence in the current conversation | none |
| `issue` | A living first sentence that names the concrete thing being checked, then an Issue | Issue task |
| `silence` | Nothing. Web Chat never silences. Group chatter that is not for the agent may silence | none |
| internal continue | Existing enqueue path when the decisioner is unavailable | existing |

The routing contract forbids `reply` from terminating with a capability refusal such
as “I cannot access contacts”. Requests that need an unavailable lookup or action must
choose `issue` and use the normal concrete Issue acknowledgement.

Digital-employee DWS outbound delivers `reply` through a completed
`execution-result` row whose `resultMessage` is that sentence. An Issue
acknowledgement is a frozen `execution-update` (`delegated_to_issue` +
`resultMessage`) so the later Issue completion can still close the dispatch
through `execution-result`. A root Issue completion uses the normalized provider
`output`, never the last streamed task-message fragment; comment callbacks keep
their thread-specific Agent reply. Robots post the same sentences through the
Robot SDK replier; Router `resultMessage` does not send a second DWS copy.

The same Chat session is the web transcript. Coordinator replies (web, robot,
or digital employee) persist as `message_kind=coordinator` with the short-loop
reason on the assistant row, so the web Chat can label them and fold the
decision process without a sandbox timeline.

Calendar, approval, emotion-only, and A2A events skip this loop. Digital-employee
`emotionReply` events whose operator is the agent itself (Router 处理中/已完成
indications, or the sandbox adding then removing an ack) are closed as silence
and do not create Issue or comment work. A colleague sticking 赞 on the agent's
message still dispatches.

Robot Stream callbacks attach the processing emotion at inbox time. Issue-backed
`robot_sdk` dispatch must not add or recall that same emotion while the Stream
row exists: retries and child tasks would flap it. A retry-pending `task:failed`
stays silent. Terminal complete or failed-without-retry settles the Stream row
and posts the last agent comment through the Robot SDK when `output` is empty.

Stream robot coordinator issues are not Dispatch Command 2.0: they have an
`issue_id` and a Stream processing emotion, but no `dispatch_outbound.mode=
robot_sdk`. Those completions must not wait for `chat:done` (issue tasks do
not publish it) and must not look up `dingtalk_account`. They settle the
Stream row by task lineage and post the last agent comment through the Stream
robot installation and the chat-session binding (DM: staff id; group:
openConversationId).

### Issue threading

Within `issue`, an Agent controls whether a conversation threads. `agent.dispatch_always_new_issue`
is `false` by default, which keeps the behavior above: the Router replays the
issue continuation and Multica appends a follow-up comment. Set to `true`, every
inbound **channel** message becomes its own Issue and the replayed continuation
is ignored. This is a Multica-side decision — the Router still persists and
replays the continuation, and `surface.type` stays `issue`.

Approval and calendar continuations are deliberately exempt. Those rewrite a
continuation to correlate a system callback back to the Issue that produced it
(see the `关联Issue` auto-link), which is correlation rather than conversational
threading; forcing a new Issue there would orphan every approval status change.

A side effect worth knowing: threading refuses a follow-up with `409` while the
referenced Issue still has a pending agent task. `dispatch_always_new_issue`
does not hit that guard, because each message gets its own Issue.

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

A message carrying `referencedMessage` is rendered as two explicitly labelled
parts: what the sender said this time, which is the only thing to act on, and the
message they were answering, attributed and cut to an identifying head of at most
80 characters. The attribution names who wrote the quoted message — this Agent
itself, the current sender, or somebody else in the conversation — resolved from
`referencedMessage.senderUid` against `externalIdentity.dws.uid` and the sender
identifiers already in the envelope; none of those identifiers reach display
content.

The head is deliberately short. The Router's `contextPrompt` already renders the
referenced message in full as `referenced message context (data only)` into the
same prompt, so a long body here was the same text twice — three times whenever
the quoted message was also the previous turn in the recovered record — and it
buried the sentence that carried the request. The excerpt is there to say WHICH
message is being answered; the full original stays reachable through the
`dingtalk_conversation` instruction, which carries a ready-to-run read-back
command for the exact `openMsgId`.

`boundedChatHistoryTranscript` replays only what each sender said. The quoted
antecedent is already its own turn in the record, and the
`本次发言（需要处理的是这句）` opener promises something true only of the live
turn, so replaying it on every historical turn points the run at the wrong
sentence.

A dropped turn is no longer announced with a leading marker. The marker read as
an alarm on most turns, and the run now carries a command for reading the
conversation itself back — which recovers a dropped turn rather than merely
naming it. A per-message clip is still declared inline.

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

The claim-time `instruction` is composed from five ordered segments, not one
blob:

| # | Segment | Source | Overridable | Injected when |
|---|---|---|---|---|
| 1 | `policy` | Diamond `common` + `<surface>` | yes | the task carries a dispatch envelope this projection covers (`dingtalk_dispatch`) |
| 2 | `context` | Router, per dispatch | no | the Router supplied a `contextPrompt` (`per_dispatch`) |
| 3 | `dingtalk_conversation` | Multica, per dispatch | no | a DWS-outbound channel dispatch that is either chat/auto or carries a quoted message (`dingtalk_conversation`) |
| 4 | `reply_formatting` | product constant | yes | any DingTalk task context, including one with no dispatch envelope (`any_dingtalk_task`) |
| 5 | `enterprise_identity` | product constant + resolved URL | yes | the run is on an ASB runtime and the authorization URL resolves (`enterprise_runtime`) |

`dingtalk_conversation` carries facts, not policy, so it is composed from the
persisted dispatch envelope and is not overridable. It gates on `outbound.mode`
being `dws`: every line in it is a DWS command, and a robot-SDK dispatch has no
injected current-user capability to run them with.

It has three halves, each with its own gate. The source-of-truth half is injected
for `chat` and `auto` only — an Issue run answers through its own surface and must
not be told to read the room first. It states that the DingTalk conversation, not Multica's record of
it, is authoritative, and that an assistant turn in that record is text written
back to the platform rather than proof a DingTalk message exists or a reply style
to copy. That is the direct fix for runs that read their own "已通过 DWS 回复" out
of a recovered transcript and treated it as delivery. The daemon-side
`<interaction-record>` block states the same caveat next to the record itself
whenever the session is backed by an IM channel.

The Issue-delivery half is injected for `issue` when the dispatch carries a
completion callback. It states that the platform delivers the run's final
assistant output back into the DingTalk conversation as the reply the person is
waiting for, and that the Issue comment is the Multica-side record they do not
see. Without it the only statement an Issue run gets about delivery is the runtime
brief's `## Output` line — "the user does NOT see your terminal output or run
logs — only comments on the issue" — which is exactly backwards for this case. The
sentence that used to carry the obligation required a `dws chat message reply`
tool call and went away with the reply tracker; Router/ServerPush owns the
delivery now, so the instruction says not to send it a second time.

The locator half prints one runnable command per target, with the real ids
substituted in:

```text
- conversation: `dws chat message search-advanced --conversation-ids cidXXX --limit 50 --format json`
- quoted msgYYY (1820 chars, TRUNCATED, by you): `dws chat message list-by-ids --msg-ids msgYYY --format json`
```

`by you` marks a quote this Agent wrote itself; otherwise the line carries the
quoted sender's uid, or `sender unknown`. A quote the display truncated is marked
`TRUNCATED` and must be read back before the Agent relies on anything the excerpt
does not show; when the dispatch supplied no quoted-message id the line says so
and the conversation command remains the way in.

The commands are spelled out rather than left as placeholders, because an Agent
that has to assemble one from a data blob is an Agent that guesses. The segment
carries no structured duplicate of those lines and no quoted text: the locator
half stays no longer than the excerpt it points past.

Every identifier appears once. The `openMsgId` is printed only inside the command
that uses it, never also as a label for the line, and the block states nothing the
daemon's own chat frame already states.

Each segment reports its gate as a stable `condition` key, present whether or
not the segment is active in the previewed scenario. A preview that only
explained exclusions would answer "why is this missing" but not "when will this
reach the agent", which is the question an owner editing a prompt actually has.

None of these segments reach work created inside Multica — a web-authored Issue
or a Multica chat is not a dispatch. Daemons without `task-instruction-v1` get
no `instruction` at all; the policy is prepended into the task content instead.

`agent.dispatch_prompt_overrides` replaces segments by id; an absent key keeps
the managed text, and a blank value means "restore managed" rather than "make
this empty". Segment 2 is not overridable because it carries this run's resolved
delivery locators rather than authored policy — replacing it would leave the
agent with no reply target. On the legacy claim path the resolved DWS workflow
block is exempt for the same reason.

`GET /api/agents/{id}/dispatch-prompt-preview` renders this composition through
the same function the claim path uses, so the settings UI cannot drift from what
the agent receives. It also returns a structural index of the sections the daemon
assembles on the runtime (the brief and the per-turn body); the server does not
own that text and deliberately does not reproduce it.
See [Diamond configuration](diamond.md#agent-authored-override).

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

- `dws` selects the current-user DWS capability for acknowledgement work. When
  Router terminal delivery is configured, Router owns lifecycle status and
  final DingTalk delivery through ServerPush, using Multica's terminal callback
  body; the Agent must not send a second direct DWS reply. Multica suppresses
  robot-SDK typing and replies for this mode. Compatibility inputs that omit a
  completion callback still instruct the Agent to finish with ordinary provider
  output; they do not restore direct DWS final-reply ownership.
- `robot_sdk` keeps Multica's channel typing and robot reply lifecycle enabled.

Outbound selection is independent of source type and surface. Issue plus DWS,
chat plus DWS, Issue plus robot SDK, and chat plus robot SDK remain valid
compositions subject to the command's ordinary validation.

For a successful Chat-to-Issue handoff, Multica sends the non-terminal
`delegated_to_issue` execution update only after the source Chat turn reaches a
terminal transaction. The request accepts an optional top-level
`resultMessage`: it is the normalized and redacted provider-selected final
output frozen from the source turn's immutable `/complete` payload. The same
`output` is the ordinary terminal `execution_result` body when no comment-scoped
persisted reply applies. Issue content, task-message text, tool receipts, and
logs are not substitutes for this value. Empty values are omitted.

The handoff row is committed atomically with the Issue and target task as
`waiting_result`, so neither a new worker (`queued` plus frozen) nor an old
worker (`queued` only) can claim it during a rolling binary deployment. The
source terminal transaction freezes `resultMessage` and atomically changes the
row to `queued`. A database trigger holds an early terminal completion at
`available_at = infinity`, a condition understood by old completion workers;
the same freeze transaction releases it. Delivery retries reuse the frozen
outbox value. Rows created by older binaries remain `queued` with the default
frozen value and send the old payload without `resultMessage`.

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

Task-scoped Router telemetry is enabled by default whenever a complete Router
capability is present. Agent `runtime_config.llm_trace.enabled` controls only
delivery to the configured static sink. The two destinations are independent:
Multica fans out one sandbox submission to both when the static sink is enabled,
and continues to forward to Router when it is disabled or absent. A static sink
receives the unchanged trace JSON without a Router or daemon token. With neither
a complete Router capability nor an enabled non-empty static sink, the runtime
does not capture or deliver traces.

The terminal `execution-result` request now also accepts an optional
`executionSummary`. Multica freezes the same task timing, usage, message/tool
counts, and runtime/sandbox shape exposed by its task summary endpoint into the
completion outbox in the terminal transaction. Every retry sends that immutable
snapshot, allowing Router to persist Agent environment data without making a
post-terminal summary/messages request back to Multica.

The summary also carries the first effective Agent activity time:

```json
{
  "executionSummary": {
    "first_effective_reply_at": "2026-08-13T02:03:04.567Z"
  }
}
```

`first_effective_reply_at` is a nullable JSON string formatted as RFC3339 or
RFC3339Nano. Its non-null value is the `created_at` of the first item by `seq`
in this task's persisted Agent message stream, including `thinking`, `text`,
`tool_use`, `tool_result`, and `error` activity. It is `null` when the task has
no persisted Agent messages; Multica does not substitute task creation, start,
or completion time. Like the rest of `executionSummary`, the value is frozen at
the terminal transaction and is unchanged across delivery retries.

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

## Change record: 2026-08-25 Segmented instruction composition and preview

- History: `agent.dispatch_prompt` (TEXT) became
  `agent.dispatch_prompt_overrides` (JSONB, keyed by segment id). Reply
  formatting and BUC authorization, previously appended by two separate calls
  after the dispatch projection ran, are now segments of one composition. Added
  `GET /api/agents/{id}/dispatch-prompt-preview`. The assembled text an agent
  receives is unchanged.
- Reason: a single override could only ever replace the first segment, which
  left the other two invisible and uncustomizable, and made a faithful preview
  impossible. Composing once means the preview is the claim path rather than a
  reconstruction of it — a second implementation would drift and start lying
  about what the agent was told.

## Change record: 2026-08-25 Agent OKR tagging

- History: Added `agent_okr`. Each objective and key result materializes as an
  `issue`-namespace workspace label, and the catalog is appended to the agent's
  instructions with the exact label names and the exact `multica issue label
  add` invocation. A rewrite replaces the set and leaves orphaned labels in the
  catalog.
- Reason: tagging is only useful if it is consistent. An agent told to "tag the
  issue" invents a label per run and the catalog fragments; naming the fixed set
  in the prompt makes the tags aggregate. Labels land in the `issue` namespace
  because the `agent` namespace cannot be attached to an issue. Orphaned labels
  survive a rewrite because issues may already carry them.

## Change record: 2026-08-24 Agent-level Issue threading control

- History: Added `agent.dispatch_always_new_issue`, `false` by default. When
  `true`, an `issue`-surface `channel/message.created` dispatch that carries an
  issue continuation creates a new Issue instead of a follow-up comment. The
  approval auto-link and the calendar contract keep their continuations. Surface
  selection, the Router contract, and the persisted dispatch context are
  unchanged; the Router still replays the continuation and Multica ignores it.
- Reason: One Issue per conversation is wrong for Agents whose inbound messages
  are independent work items — the thread grows without bound and a second
  message is refused with `409` while the first task is still running. Keeping
  this on the Multica side rather than adding a fourth `surface.type` avoids a
  lockstep Router release for what is purely a persistence decision.

## Change record: 2026-08-24 Agent-authored dispatch prompt

- History: Added `agent.dispatch_prompt`, empty by default. A non-empty value
  replaces the Diamond `common` + `<surface>` composition for that Agent at
  claim time on both the instruction-capable and legacy claim paths.
  `contextPrompt` and the legacy DWS workflow block are unaffected. Dispatch
  mode selection, materializer choice, outbound ownership, and persisted
  dispatch context are unchanged.
- Reason: One fleet-wide Diamond document cannot fit every Agent's job. Giving
  the Agent owner a complete replacement keeps per-Agent policy out of the
  deployment configuration, at the cost of opting that Agent out of later
  Diamond policy updates — which is why the override is per-Agent and explicit
  rather than a silent merge.

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
## Change record: 2026-08-13 First effective reply timestamp

- History: Added nullable `executionSummary.first_effective_reply_at` to the
  terminal execution-result callback as the first persisted Agent activity
  timestamp by message-stream order for the task. Existing outbox rows are not
  backfilled.
- Reason: Router now consumes the immutable terminal summary without querying
  the post-terminal transcript, so the summary must carry the original first
  activity signal used by first-reply latency metrics.

## Change record: 2026-08-20 Default Router LLM trace delivery

- History: Router task telemetry is now default-on whenever its private
  callback is complete. Agent `runtime_config.llm_trace.enabled` now controls
  only fan-out to the configured static trace sink.
- Reason: Platform observability must not depend on an Agent's optional external
  trace destination, while the existing sandbox-to-Multica relay remains the
  single capture and delivery path.

## Change record: 2026-08-25 Delegation handoff result message

- History: Added optional top-level `resultMessage` to `delegated_to_issue`
  execution updates. New handoff rows use `waiting_result` until the source Chat
  terminal transaction atomically freezes the daemon-confirmed DWS reply and
  moves them to `queued`; terminal callbacks are held through the legacy
  `available_at` gate; old-binary rows and empty results remain consumable.
- Reason: Router needs the actual user-visible handoff acknowledgement, but it
  does not exist when the Issue/outbox control boundary first commits. Encoding
  the wait in status hides it from old `status = 'queued'` workers during a
  rolling deployment, while the terminal hold preserves update-before-terminal
  ordering and freezing once preserves immutable retry payloads.

## Change record: 2026-08-26 Delegation handoff final output source

- History: Changed `delegated_to_issue.resultMessage` to freeze the provider's
  final `output` from the immutable source Chat terminal payload. The legacy DWS
  reply tracker remains available to terminal and failure compatibility paths
  but no longer chooses the execution-update body.
- Reason: A successful delegation now asks the Agent to acknowledge the handoff
  through its ordinary final assistant reply, without requiring a DWS reply tool
  call. Reading the old receipt therefore dropped valid acknowledgements, while
  freezing terminal `output` preserves retry immutability, rolling-worker
  visibility, and update-before-terminal ordering.

## Change record: 2026-08-26 Remove legacy DWS reply receipt source

- History: Removed the daemon DWS reply tracker and its private
  `ResultMessage`/`result_message` transport through provider results, pending
  terminal reports, daemon callbacks, handler DTOs, and failed-task JSON. A
  completed task now derives both Router terminal delivery and delegated update
  delivery from the provider-selected final `output`; failed callbacks retain
  the explicit `error` and `failureReason` contract. Router outbox
  `result_message` snapshots and comment-scoped persisted replies remain part of
  the current callback protocol.
- Reason: Router/ServerPush is the durable final-reply owner whenever a
  completion callback exists, so a successful `dws chat message reply` tool
  receipt is no longer an AI-output authority. Removing the duplicate source
  prevents ordinary final replies from being replaced or suppressed while
  keeping outbox immutability, rolling-worker visibility, update-before-terminal
  ordering, and one provider-output contract across compatibility inputs.

## Change record: 2026-08-26 Attributed and re-readable quoted messages

- History: Display content for a quoted DingTalk reply now leads with the
  current message, names the quoted message's author relative to the dispatch,
  and inlines at most 800 characters of the original. A new non-overridable
  `dingtalk_conversation` instruction segment carries the reading rule plus one
  compact line per quote — message id, length, truncation state, whether this
  Agent wrote it — each ending in a runnable read-back command, and a
  conversation-scoped read-back command for chat/auto runs;
  the legacy prompt builder emits the same block.
  `persistedDispatchContext` now reads the private `external_identity.dws`
  descriptor already stored beside the envelope.
- Reason: The previous rendering opened with the untruncated original under a
  bare `引用消息：` label and gave the reply a bare `当前回复：` label, so an
  Agent quoting its own completion report saw an unattributed wall of text ahead
  of a one-line acknowledgement and could not tell whose message it was, nor
  recover anything the Router had already trimmed. Attribution and the re-read
  locator make the relationship explicit and the original recoverable without
  putting message identifiers into user-visible content.

## Change record: 2026-08-26 DingTalk conversation as the source of truth

- History: Merged the quoted-message instruction into a `dingtalk_conversation`
  segment that a DWS-outbound chat or auto dispatch always receives, whether or
  not anything was quoted. It names the DingTalk conversation as authoritative,
  prints the real `openConversationId` in a runnable
  `dws chat message search-advanced` command, and states that an assistant turn
  in Multica's recovered record is text written back to the platform rather than
  a delivered message. The daemon's `<interaction-record>` block carries the same
  caveat for any IM-channel session.
- Reason: A pre-release trace showed a chat run whose recovered history held
  undecryptable inbound payloads and four assistant turns reading
  "已通过 DWS 回复", with the frame telling it no command could fetch more of the
  conversation. The run had no way to recover the real exchange and learned to
  answer in its own delivery-report voice. The conversation is readable through
  the injected DWS capability, so the frame was wrong in a way that made the
  session read as non-native.

## Change record: 2026-08-26 Stop reproducing the quoted message

- History: Display content for a quoted DingTalk reply no longer inlines the
  quoted original. It carries what the sender said plus one attribution line, and
  `boundedChatHistoryTranscript` drops even that line when replaying the turn as
  history. The display-side excerpt cap, blockquote rendering and truncation
  notice are gone with it, and the `dingtalk_conversation` hint reports a quote's
  length without claiming any of it is visible.
- Reason: The Router's `contextPrompt` already renders `referenced message
  context (data only)` with the quoted text in full into the same prompt. Multica's
  copy was a second one, and a third appeared whenever the quoted message was also
  the previous turn in the recovered record — a pre-release trace showed one short
  reply present three times. Multica keeps only what the Router cannot resolve:
  whether this Agent wrote the quoted message itself.

## Change record: 2026-08-26 Labelled request/quote split, no trimmed marker

- History: Restored the two labelled parts of a quoted reply — the request and
  the attributed antecedent — with the antecedent cut to an 80-character head
  instead of reproduced. Removed `chatHistoryOmittedMarker`: a bounded transcript
  no longer heads itself with `[older turns were trimmed from this transcript]`.
  De-duplicated the instruction block against itself and against the daemon chat
  frame.
- Reason: Owner review of pre-release traces. Dropping the quote entirely lost the
  split that made the turn legible, while reproducing it in full duplicated the
  Router's copy; a head does both jobs. The trimmed marker fired on almost every
  turn and pushed runs to announce missing context instead of reading the
  conversation back, which they can now do with a printed command.

## Change record: 2026-08-26 Issue runs are told where their reply goes

- History: `dingtalk_conversation` now carries an Issue-delivery paragraph for a
  `surface=issue` dispatch that has a completion callback, naming the final
  assistant output as the reply Router delivers into the conversation and the
  Issue comment as the Multica-side record.
- Reason: Removing the DWS reply tracker also removed the only sentence that told
  an Issue run its result had two destinations, because that sentence required
  the tool call being removed. What remained was the runtime brief's `## Output`
  line, which tells an Issue run the user sees only issue comments — true for a
  web-created Issue, backwards for a DingTalk-dispatched one whose final output
  is what the person receives.
