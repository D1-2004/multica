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

The inbound coordinator (web Chat and DingTalk `message.created`) is a short
tool loop with `assoc_recall`, `assoc_bind`, `issue_get`, `issue_comment_list`,
`issue_comment_add`, and `finish`. It may bind this scene to an existing Issue
and leave a reception note before opening a sandbox. DWS still belongs to the
sandbox, not the coordinator. Sandbox CLI `multica issue comment *` remains
for Issue tasks.

## Identity

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

Or MCP / coordinator tool `assoc_bind`. The coordinator must inject `purpose`
(deliverable phrase) and `intent` (`ask` / `confirm` / `notify` / `lookup` /
`wait` / `other`). Omit `issue_id` to declare a new matter.

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
`GET /api/assoc/recall` and `GET /api/assoc/events`.

An inbound `/reset-memory` (first token, optional leading @mention) closes this
conversation's Issue associations and does not start a sandbox. It is not
`/reset`. After that, recall for the cid should be empty of items.

## Purpose

Name the deliverable: `向冬翔确认今天吃什么`. Not `帮我看看`.
