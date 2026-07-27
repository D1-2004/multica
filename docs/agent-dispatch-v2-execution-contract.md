# Agent Dispatch V2 Execution Contract

Agent Dispatch V2 is a structured, authenticated command. Multica must execute
its fields as independent policies instead of inferring one policy from another.

## Surface

`surface.type` is the only field that selects the Multica persistence surface:

- `issue` creates an Issue or appends a follow-up to the referenced Issue.
- `chat` creates or reuses a chat session and appends a user message.

Channel slash commands do not override this choice. In particular, text such as
`/issue`, `/new`, `/reset`, or `/unbind` remains prompt content when delivered by
Agent Dispatch V2.

The response always returns the latest continuation produced by the selected
surface. A recreated missing Issue or chat therefore replaces a stale
continuation for the Router to persist.

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
- runtime instructions contain private input-safety policy;
- workflow instructions are added when the selected outbound mode requires the
  Agent to deliver through DWS.

The prompt builder is independent of `surface.type`. The same structured event
can therefore run as either an Issue or a chat without moving prompt assembly
back into the Router.

## Outbound

`outbound.mode` is the only field that selects outbound ownership:

- `dws` delegates acknowledgement and final DingTalk delivery to the Agent's
  injected DWS capability. Multica suppresses server-side typing and robot
  replies for that dispatch.
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
