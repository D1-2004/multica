---
name: multica-delegating-to-issues
description: "Use in a Chat task when long-running, side-effectful, or domain-specific work should continue as an Issue. Covers routing to an existing Issue, creating a stable Issue, and using the delegation command while preserving task identity and completion responsibility."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Delegate Chat work to an Issue

Use Issue delegation to release the current Chat session while the requested
work continues in Multica's Issue queue.

## Decide while working, not with a slow preflight

Start by understanding the request and take a cheap first step when that helps.
Delegate as soon as the work clearly requires sustained execution, specialist
domain work, code or repository changes, several dependent tool calls, waiting
on an external system, or coordinated side effects across products.

Do not keep work in Chat merely because it starts from public information alone.
For example, collecting today's news, turning it into a card, sending it to
multiple people, and creating a todo is a multi-step delivery job even though
the source material is public.

Keep genuinely quick explanations, small lookups, and short answers in Chat.
Delegation is not needed only because a task sounds technical; it is needed when
continuing it would occupy the Chat turn or benefits from an Issue's durable
queue and specialist assignee.

## Route semantically before creating

The current Chat session is available as `MULTICA_CHAT_SESSION_ID`. Before
creating a new Issue, list the Issues already delegated by this Chat:

```bash
multica issue list \
  --metadata "multica.chat_session_id=$MULTICA_CHAT_SESSION_ID" \
  --limit 100 \
  --output json
```

Compare the new request with each candidate's stable title, description,
status, and latest relevant comments:

- Continue the existing Issue when the subject and intended deliverable are the
  same, including short contextual follow-ups such as "continue", "that is
  wrong", or "do not send it anymore".
- Create a new Issue when no candidate represents the same work.
- Do not route only by word overlap. A broad topic can contain several distinct
  deliverables, while differently worded messages can still continue one job.

When creating, use a stable semantic title:

```text
subject or object + intended deliverable + necessary scope
```

Good: `采集 2026-07-29 科技新闻并生成消息卡片`

Bad: `帮我看一下` or `用户的新任务`

Choose an Agent whose domain matches the work. Use `multica agent list --output
json` when the current Agent is not the right specialist.

## Use the delegation command

Write long content to a UTF-8 file inside the current working directory.

Create a new delegated Issue:

```bash
multica issue delegate \
  --title "采集 2026-07-29 科技新闻并生成消息卡片" \
  --description-file ./delegation.md \
  --assignee-id <agent-id> \
  --output json
```

Continue an existing Issue:

```bash
multica issue delegate \
  --issue <issue-id> \
  --content-file ./follow-up.md \
  --output json
```

The command reads `MULTICA_TASK_ID` automatically. Do not copy or print
ContextToken, callback details, or other task-private identity data. They are
looked up and transferred by the server, and are never command arguments.

Do not use ordinary Issue creation or a plain comment as a substitute for this
command: those operations do not transfer the source task's completion
responsibility.

The server returns `release_parent: true` only after the Issue or comment,
background task, task lineage, and reliable handoff update have committed
together. After that response, briefly tell the user that the work has moved to
the background through the task's normal outbound path, then stop the current Chat task.
For DWS outbound, this acknowledgement must be a successful
`dws chat message reply`; a terminal stdout-only sentence is not a delivered
user reply. The server records that successful DWS reply as the user-visible
handoff result without exposing callback details to the command. Do not
continue executing the delegated business work in Chat.

If the command fails or does not return `release_parent: true`, control has not
transferred. Keep the current Chat task responsible for reporting the failure;
never claim that background execution started.
