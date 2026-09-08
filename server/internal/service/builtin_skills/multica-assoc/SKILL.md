---
name: multica-assoc
description: "Use when an Issue must contact someone on DingTalk, or a reply arrives in a new private chat. Recall and bind openConversationId to the current Issue. Digital-employee inbound has complete cid/uid; robot/web inbound often does not."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Scene graph: Issue ↔ DingTalk conversation

The platform graph tags each send/reply with a DingTalk `openConversationId`
(scene). Query it. Do not reconstruct the relationship from chat history.

Outreach to another person is not the reply the platform delivers back to the
waiting sender.

The inbound coordinator is a read-only decision loop: `assoc_recall`,
`context_read` for bounded conversation history, and recalled-issue readers.
It submits a complete plan through `finish.items`: new items omit `issue_id`;
continuations carry a recalled `issue_id` plus the current source references.
Host commits the normal Issue/member-comment operations after validation and
keeps retries idempotent. Coordinator does not expose write tools. Business
DWS operations and full skills still run in the sandbox.

## Identity

The trusted DingTalk dispatch event is authoritative for business identity:

- On a new Issue, the current DingTalk sender is the task delegator/requester.
- On an inbound reply projected through a continuation plan, the current sender
  is the actual DingTalk speaker for that message. Find the original delegator
  from the Issue's original DingTalk task scene and association graph.
- The Multica Issue creator or member-comment author only identifies the
  workspace principal that executed the Issue tool. That person is an
  executor/assistant and must not be treated as the delegator, DingTalk
  speaker, or contacted recipient merely because their name appears on the
  Issue timeline.
- This applies to both routes. Digital-employee events carry complete
  conversation/user identity. Robot events may omit the sender uid; use only
  the sender name, conversation, and message facts actually present, and never
  replace the missing identity with the Multica Issue author.

| Inbound source | conversation_id | uid |
|---|---|---|
| Digital employee | complete | complete |
| Robot | may exist | often missing |
| Web chat | none | none |

Outbound `dws chat message send` receipts always include `openConversationId`.
Bind those. Never invent a cid for web inbound.

## After outbound send — bind

```bash
multica assoc bind --conversation <openConversationId> [--evidence <openMsgId>] [--person <uid>] --output json
```

Or use the sandbox MCP `assoc_bind` for the current Issue. Never invent an
Issue or a conversation ID. Coordinator itself does not call this write tool:
Host binds a newly committed Issue to its source scene. An existing matter is
continued only when the current message adds substantive input to that same
deliverable, not merely because a candidate matched.

## Recall before treating a chat as a new matter

Current Issue (who did we already contact):

```bash
multica assoc recall --current-issue --since 48h --output json
```

This conversation (which Issue caused it):

```bash
multica assoc recall --conversation <openConversationId> --since 48h --output json
```

Scene-tagged events (inbound/outbound evidence):

```bash
multica assoc events --conversation <openConversationId> --since 48h --output json
```

`--since` is required. Inspect `purpose`; if several items match, ask. HTTP is
`GET /api/assoc/recall` and `GET /api/assoc/events`. Coordinator `assoc_recall`
always keeps the inbound `openConversationId`; `q` filters that scene and must
not drop the cid. Coordinator recall JSON is a short card (`read_this` /
`issue_id` / `purpose` / `why` / `on_this_scene`); continue only after
comparing purpose to the current message.

An inbound `/reset-memory` (first token, optional leading @mention) closes this
conversation's Issue associations and does not start a sandbox. It is not
`/reset`. After that, recall for the cid should be empty of items.

## Purpose

Name 委托人, 事件, and 目的: `冬翔委托：向辰驷确认明天几点打球`.
Place is optional. Not the inbound envelope. Not `帮我看看`. One recalled card
is not a verdict; continue only when purpose matches the current message.
