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
passed through private task context. DingTalk sender identifiers remain message
attribution and reply-target data. They do not become the Multica principal and
do not trigger ordinary channel sender binding on this authenticated path.

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

## Reason

The previous chat path reused ordinary DingTalk robot ingestion end to end. It
therefore attempted sender binding before creating a chat and coupled DWS versus
robot behavior to hard-coded source/surface pairs. This caused authenticated
digital-employee dispatches to be acknowledged without creating a conversation.
Independent policies make the explicit dispatch fields authoritative and keep
login identity, Agent execution identity, and external message sender distinct.
