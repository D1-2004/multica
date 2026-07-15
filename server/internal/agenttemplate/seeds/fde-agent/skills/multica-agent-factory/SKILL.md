---
name: multica-agent-factory
description: Discover workspace Agent templates and runtimes, create one independent Multica Agent from a persisted snapshot, and optionally connect it to DingTalk through a task-authenticated API helper.
---

# Multica Agent Factory

Use this skill when a user wants to create, configure, or connect a Multica Agent. The bundled `scripts/multica_factory_api.py` helper and its JSON output are the only operational interface. It uses task-scoped authentication and reads complete workspace template snapshots from Multica. It never compiles a checked-out repository or falls back to generic Agent/Skill creation commands.

## Helper location and authentication

Locate the helper without downloading or generating another client. Try these paths in order and use the first existing file:

1. `skills/multica-agent-factory/scripts/multica_factory_api.py` in the Agent repository;
2. `$HOME/.hermes/skills/multica-agent-factory/scripts/multica_factory_api.py`;
3. `$HOME/.agents/skills/multica-agent-factory/scripts/multica_factory_api.py`.

Run it with `python3`. It reads `MULTICA_SERVER_URL`, `MULTICA_WORKSPACE_ID`, and the task-scoped `MULTICA_TOKEN` from the sandbox environment. Never print these variables, pass the token as an argument, copy it into a file, or replace it with a user or daemon token.

The current server requires the triggering user's workspace membership to be `owner` or `admin` for Agent creation and DingTalk installation. If the helper returns HTTP 403, report that permission requirement; do not attempt to bypass it or use another credential.

## Preconditions

1. The normal user input is only an Agent name and a natural-language description of its capabilities. Do not ask for fields that can be derived or discovered.
2. Run `python3 <helper> template-list` and `python3 <helper> runtime-list` before creating the Agent.
3. If either command fails because task authentication or server configuration is missing, or Multica is unreachable, stop and report the exact blocker. The helper owns the only approved CLI compatibility path; do not improvise CLI commands, arbitrary HTTP calls, or database access.
4. Run `python3 <helper> agent-list` and check for an existing non-archived Agent with the requested name. Reuse it when its purpose matches the request. Ask only when the same name refers to a materially different Agent.

## User interaction boundary

The creation interface has exactly two human-owned inputs:

- Agent name;
- Agent capability or purpose, used as the instance description.

Template discovery is the welcome step, not another form. For a generic creation request, first run `template-list`, briefly tell the user which templates are available, recommend the best match, and say that using one requires only the Agent name and capability description. Ask only for whichever of those two fields is missing. If both are already present, state the selected template briefly and begin creation immediately. Never enumerate additional fields, optional settings, or a configuration checklist.

The following are not questions for the user:

- `instructions`, skills, and supporting files: copied from the selected workspace template snapshot;
- template slug: discovered and selected from the workspace template list;
- runtime: automatically select an online Claude runtime by default;
- model and thinking level: owned by runtime configuration;
- project association, visibility, and privacy: not part of this MVP creation flow;
- DingTalk installation: inferred from the user's expressed intent.

Do not offer these as optional choices, even with wording such as “你可以先告诉我名称和用途”. Mention an internal choice only after creation as supporting information, or when a real blocker prevents progress.

### Format finite choices for DingTalk

When a question genuinely has a short, finite set of answers, present every answer as a DingTalk quick-reply Markdown link:

```markdown
[A. 创建一个新的 Agent](dtmd://dingtalkclient/sendMessage?content=A)

[B. 查看现有 Agent 列表](dtmd://dingtalkclient/sendMessage?content=B)

[C. 连接 Agent 到钉钉](dtmd://dingtalkclient/sendMessage?content=C)

[D. 查看工作区信息](dtmd://dingtalkclient/sendMessage?content=D)
```

Assign sequential uppercase letters starting at `A`, use only as many as needed up to `D`, and include both the letter and description in each link label. Set the link's `content` to that letter only. Separate every two links with a full blank line—exactly two newline characters rather than a single line break—because DingTalk Markdown can merge or swallow adjacent links. Never place the choices in one paragraph, side by side, or in a table. End with a fallback such as “点击选项，或直接回复 A/B/C/D”。

Interpret a bare `A`, `B`, `C`, or `D` case-insensitively against the most recent unresolved choice in the conversation. A clicked link, an uppercase typed letter, and its lowercase equivalent select the same option. Once handled, do not apply later letters to that resolved prompt.

This formatting rule applies to real decisions such as equally plausible templates or an Agent-name conflict. It does not turn free-form inputs into choices and does not override automatic decisions elsewhere in this skill. Ask for a missing name or capability description in plain text, and never add a confirmation step solely to show quick replies.

## Choose the inputs

### Template

Use the template list returned by the helper. It reflects templates owned by the current workspace. Present the available template names and short descriptions in concise user language. When exactly one template is available, recommend it automatically. When several are available, compare their descriptions with the requested capability and inspect the most plausible templates:

```bash
python3 <helper> template-get <template-key>
```

Select the clearest match without asking the user to understand internal slugs. Ask a template question only when multiple materially different templates remain equally plausible or no template supports the request.

### Runtime

Use only runtime IDs returned by:

```bash
python3 <helper> runtime-list
```

Select an online Claude runtime by default. Match runtime name/provider metadata case-insensitively for `claude`, and choose a healthy online match without asking the user or listing alternatives. If several healthy Claude runtimes exist, choose the first stable result returned by Multica.

Use another provider only when the user explicitly requests it. Never silently fall back to Codex. If no online Claude runtime exists and the user did not request another provider, report that Claude is unavailable and stop; do not turn runtime selection into a questionnaire.

Do not accept or pass model names or thinking levels; those are runtime-owned configuration.

### Instance metadata

Use the name supplied by the user. Generate a concise one-sentence description in the user's language from the requested capabilities, unless the user already supplied suitable wording. Template instructions and skills are not customizable through this creation command.

Do not present template, runtime, description, or provider fields for confirmation. Once the requested name and capability are clear, proceed and communicate progress in plain user language. Internal choices belong in the completion report or in a blocker explanation, not in a preflight form.

## Create exactly one Agent

Once the user's name and capability are clear and no real conflict remains, run:

```bash
python3 <helper> agent-create <template-slug> \
  --runtime-id <runtime-id> \
  --name <agent-name> \
  --description <agent-description>
```

Normally pass the user-supplied name and the derived description. Read the new Agent ID from the JSON result and immediately verify it:

```bash
python3 <helper> agent-get <agent-id>
```

If creation returns an error or an ambiguous response, do not rerun it automatically. First use `python3 <helper> agent-list` to determine whether the Agent was created.

## Connect the Agent to DingTalk

Infer this step from the user's request. Phrases such as “创建机器人”, “钉钉机器人”, “让用户在钉钉里使用”, or equivalent intent request DingTalk installation. A request only to create an Agent does not. Do not ask the user to toggle DingTalk installation when the wording is already clear. Reuse the verified Agent ID:

```bash
python3 <helper> dingtalk-begin --agent-id <agent-id>
```

Add `--allow-unbound` when the user explicitly wants external users or customers to use the Agent without binding a Multica account.

Read `qr_code_url`, `session_id`, and `expires_in_seconds` from the result. Return the QR URL, its expiry, and a reminder to authorize the intended DingTalk organization as the final response of the current chat task. Keep the `session_id` in that final response as a supporting installation-session detail so it remains recoverable from conversation history.

**Stop the task immediately after returning the QR URL.** DingTalk channel users receive the Agent's response only after the chat task completes. Never poll, wait, sleep, or run another installation command in the same task that called `begin`.

### Check after the user scans

When the user later says “已扫码”, “已授权”, or equivalent, recover the most recent unexpired installation `session_id` from the conversation and run exactly one status check:

```bash
python3 <helper> dingtalk-status <session-id>
```

Handle states as follows:

- `pending`: tell the user authorization has not completed yet and ask them to finish scanning, then end the task. Check again only after another user message.
- `success`: capture `installation_id`, report completion, and end the task.
- `error`: report the returned reason, preserve the Agent for a DingTalk-only retry, and end the task.
- expired or missing session: start a new DingTalk installation session for the same existing Agent, return the new QR URL, and end the task.

Never implement status checking with a shell loop or a variable named `status`. Never make repeated status calls in one task, even when the server returns `poll_interval_seconds`.

Do not invoke the installed `multica` CLI as a fallback. If a workspace template API is unavailable, report the API error and stop without trying to reconstruct the template from local files.

Never ask the user for a DingTalk Client Secret during the QR flow. Never display credentials returned by any lower-level system.

## Completion report

Report only verified fields:

- Agent name and ID;
- template name and slug;
- runtime name and ID;
- DingTalk installation ID and status when requested;
- any action still required from the user.

Do not claim that the Agent can execute tasks merely because creation succeeded. Runtime reachability and task execution are separate checks.

Do not report a template source link for the created Agent. Creation copies the snapshot and deliberately records no template or Git provenance on the Agent.
