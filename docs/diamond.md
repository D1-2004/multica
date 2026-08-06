# Alibaba Diamond configuration

Multica uses two independent Alibaba Diamond documents:

- `dt-fde-multica.json` is the always-active, fail-open prompt/feature-rule source documented on this page. It does not replace process environment variables, the YAML rule file, or caller defaults.
- `dt-fde-multica-runtime.json` is the opt-in, fail-closed managed runtime configuration. When `MULTICA_RUNTIME_CONFIG_SOURCE=diamond`, it is authoritative for the migrated non-secret settings and has no environment-variable fallback. See [Managed runtime configuration](runtime-config.md).

For the prompt/feature-rule document, the effective precedence is:

```text
FF_* environment override > Diamond JSON > YAML file > caller default
```

There is no Diamond enable switch. Every server process attempts the initial read and registers the listener during startup. Connection, read, and listener failures are fail-open and do not prevent the service from starting.

## Fixed connection contract

The server uses `github.com/nacos-group/nacos-sdk-go/v2` v2.3.5 and the current Alibaba unit endpoint contract:

| Setting | Value |
|---|---|
| Endpoint | `jmenv.tbsite.net:8080` |
| Endpoint context path | `diamond-server` |
| Cluster | `diamond` |
| Namespace | empty |
| Application | `dt-fde-multica` |
| Configuration type | `json` |

The configuration coordinates default to:

| Setting | Environment variable | Default |
|---|---|---|
| Data ID | `MULTICA_DIAMOND_DATA_ID` | `dt-fde-multica.json` |
| Group | `MULTICA_DIAMOND_GROUP` | `DEFAULT_GROUP` |

For the pre-release environment, create the JSON configuration under application `dt-fde-multica`, environment `pre`, Data ID `dt-fde-multica.json`, and Group `DEFAULT_GROUP`. Pre-release configuration and deployment are separate operational steps and are not performed by the code change.

## JSON schema

The Diamond document contains one common runtime prompt plus the prompt for each dispatch surface. These prompts are configuration, not source-code constants:

```json
{
  "common": {
    "prompt": "Shared safety, delivery, and response policy"
  },
  "issue": {
    "prompt": "Issue mode runtime policy"
  },
  "chat": {
    "prompt": "Chat mode runtime policy"
  },
  "auto": {
    "prompt": "Auto mode runtime policy"
  }
}
```

The only allowed top-level keys are `common`, `issue`, `chat`, and `auto`; the only allowed field inside each section is the string field `prompt`. Sections may be omitted. `{}` is a valid empty configuration and atomically removes all Diamond prompts. Unknown sections, unknown fields, `null`, non-string prompts, malformed JSON, and trailing JSON values are rejected.

The server maps these sections to internal feature-flag keys. This keeps the existing precedence contract available for emergency overrides and YAML fallback:

| Section | Highest-priority environment override | YAML fallback key |
|---|---|---|
| `common` | `FF_DISPATCH_COMMON_RUNTIME_PROMPT` | `dispatch_common_runtime_prompt` |
| `issue` | `FF_DISPATCH_ISSUE_RUNTIME_PROMPT` | `dispatch_issue_runtime_prompt` |
| `chat` | `FF_DISPATCH_CHAT_RUNTIME_PROMPT` | `dispatch_chat_runtime_prompt` |
| `auto` | `FF_DISPATCH_AUTO_RUNTIME_PROMPT` | `dispatch_auto_runtime_prompt` |

There is no fixed dispatch prompt embedded in the binary. For each daemon claim, Multica assembles the independent task-level `instruction` in this exact order:

```text
common.prompt

<current surface>.prompt

Dispatch Command contextPrompt
```

Blank sections are skipped. If all three inputs are blank, `instruction` is omitted and older tasks preserve their previous behavior. `contextPrompt` is not Diamond configuration: it is dynamic, credential-free execution context supplied by the authenticated Router command and persisted only in private task context. Issue descriptions, comment content, chat messages, and assignment handoff notes are never rewritten with these instructions.

Every continuation task recomposes the instruction at claim time from the latest valid Diamond snapshot. When Auto delegates to an Issue, Multica transfers the dynamic Router context, changes the private dispatch surface to `issue`, and recomposes `common + issue + context`; it does not copy the Auto prompt into the child task.

## Startup, updates, and shutdown

At startup the server:

1. Creates the Diamond client with the fixed unit coordinates.
2. Calls `GetConfig` for the configured Data ID and Group.
3. Validates the complete JSON document and installs it as one immutable snapshot.
4. Registers `ListenConfig` for subsequent changes.

Each listener update is parsed and validated before one atomic pointer replacement. Requests therefore observe either the previous complete snapshot or the new complete snapshot. An invalid update never partially changes rules and never replaces the last valid snapshot.

Diamond is fail-open. Client creation, initial fetch, or listener registration failures do not terminate the server. The remaining provider chain continues to serve `FF_*`, YAML, and caller defaults. An initial fetch failure does not prevent listener registration, so the process can recover when Diamond becomes reachable.

During graceful shutdown the server calls `CancelListenConfig` and then `CloseClient`.

## Logging and security

Application logs for Diamond record only:

- Data ID;
- Group;
- number of rules;
- SHA-256 of the received document.

The configuration body and SDK error text are not written to application logs.

Diamond is not a secret store. Do not place database URLs, JWT secrets, service credentials, private keys, signing keys, callback tokens, ContextTokens, or other credentials in this document. Keep those values in the existing deployment secret/configuration mechanisms.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-04 | Added `common.prompt` and claim-time `common + mode + contextPrompt` composition through the independent task `instruction` field. | Keep fixed policy in Diamond, dynamic delivery facts in Router context, and all private instructions out of user-visible Issue, comment, chat, and handoff content. |
| 2026-08-04 | Removed the Diamond enable switch and made the provider start unconditionally with fail-open behavior. | Avoid silently skipping dynamic prompts when deployment configuration omits a redundant enable variable. |
| 2026-08-01 | Replaced the embedded auto-mode prompt with the strict `issue/chat/auto.prompt` Diamond document and enabled dynamic prompt injection for all three surfaces. | Keep surface behavior policy outside the binary and allow one atomic configuration update to control every dispatch mode. |
| 2026-08-01 | Added the optional Diamond-backed dynamic feature-flag provider. | Allow runtime configuration updates without restarting Multica while preserving fail-open behavior and configuration precedence. |
