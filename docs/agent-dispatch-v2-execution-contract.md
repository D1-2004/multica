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
`calendar/calendar.started` contract remains Issue-only because it has no
foreground Chat session to release.

Channel slash commands do not override this choice. In particular, text such as
`/issue`, `/new`, `/reset`, or `/unbind` remains prompt content when delivered by
Agent Dispatch V2.

The response always returns the latest continuation produced by the selected
surface. A recreated missing Issue or chat therefore replaces a stale
continuation for the Router to persist.

Continuation kind identifies the materialized locator, not the execution mode.
Both `chat` and `auto` therefore return a `chat` continuation containing
`chatSessionId`; `issue` returns an Issue continuation.

## Identity

The webhook bearer credential authenticates an Agent Dispatch endpoint and
resolves its endpoint actor, workspace, and Agent. That endpoint actor is the
Multica principal used to create sessions, persist messages, and authorize task
execution.

`externalIdentity.contextToken` remains the separate Agent execution identity
passed through private task context. When `contextToken` is present,
`externalIdentity.expiresAt` is also required and carries the token expiry as
Unix epoch milliseconds. Multica stores the pair as
`agent_identity_context_token` and
`agent_identity_context_token_expires_at`, and marks request-supplied tokens
with `agent_identity_context_token_source=external`; none of these fields is
exposed through ordinary task responses.

Immediately before starting the task runner, a DWS runtime first resolves the
Agent's local DingTalk identity binding. When a binding exists, it has highest
priority regardless of whether task context contains an external or cached
token, and Multica creates a new Agent Identity context from that binding. Only
when no Agent binding exists does Multica inspect task context: an external
token is never replaced by a cached identity, while an expired external token,
a token inside the one-minute execution safety window, or an incomplete
external token/expiry pair fails runtime startup. A cached token with more than
one minute remaining is reused; an absent cache runs without Agent Identity,
while an expired or near-expiry cache fails because no Agent binding is
available to refresh it. A malformed cached token/expiry pair still fails
closed.

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
missing, empty, or whitespace-only value is a byte-for-byte no-op, allowing the
daemon consumer to roll out before any server starts producing the field.

At claim time Multica composes that field as `common.prompt + current mode.prompt
+ contextPrompt`, skipping blank sections and separating non-blank sections with
two newlines. The composition is claim-scoped, so Diamond updates apply to tasks
that have not yet been claimed. Issue descriptions, trigger-comment content,
chat messages, and assignment handoff notes remain unchanged user-visible data.

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
