# Alibaba Diamond configuration

Multica uses four independent Alibaba Diamond documents:

- `dt-fde-multica.json` is the always-active, fail-open prompt/feature-rule source documented on this page. It does not replace process environment variables, the YAML rule file, or caller defaults.
- `dt-fde-multica-runtime.json` is the opt-in, fail-closed managed runtime configuration. When `MULTICA_RUNTIME_CONFIG_SOURCE=diamond`, it is authoritative for the migrated non-secret settings and has no environment-variable fallback. See [Managed runtime configuration](runtime-config.md).
- `dt-fde-multica-runtime-manifest-fingerprints.json` keeps its historical Data ID but now contains only one deployment-wide provider list. It changes only when providers change, not when images or templates are released. It is not an image-integrity check, and read/listener failures do not block application startup. See [Runtime provider catalog](runtime-config.md#runtime-provider-catalog).
- `dt-fde-multica-model-pricing.json` is the fail-closed USD-per-million-token catalog for every deployment-managed model. It prices usage centrally and supplies rates to the model picker, so users do not configure each Runtime or browser. See [Managed model pricing](runtime-config.md#managed-model-pricing).

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

### Agent-authored override

An Agent may replace this document's contribution. `agent.dispatch_prompt_overrides`
is empty by default, which keeps the Diamond composition described below. Its
`policy` key replaces **both** `common.prompt` and the current `<surface>.prompt`
for every dispatch that Agent claims — the author owns the complete fixed policy,
including the safety, delivery, and truthfulness clauses that `common` would
otherwise contribute. The settings editor prefills with the managed text so that
dropping one of those clauses is a deliberate edit rather than the consequence of
starting from an empty field.

The override is resolved at claim time from the current database row, exactly
like the Diamond snapshot it replaces, so an edit reaches continuation tasks and
delegated Issue follow-ups without re-dispatching. Whitespace-only is treated as
empty. The Router-supplied `contextPrompt` is never replaceable: it is not
authored policy but this run's resolved delivery facts.

| | no `policy` override (default) | `policy` override set |
|---|---|---|
| Composition | `common.prompt` + `<surface>.prompt` + `contextPrompt` | override + `contextPrompt` |
| Reacts to a Diamond update | yes | no — the Agent is opted out of this document |

Because the override drops `common`, an operator changing a fleet-wide safety
rule in Diamond does not reach any Agent that has authored its own prompt. Audit
`agent.dispatch_prompt_overrides` alongside this document when rolling out a
policy change.

## Claim-time composition

There is no fixed dispatch prompt embedded in the binary. For each daemon claim, Multica assembles the independent task-level `instruction` in this exact order:

```text
common.prompt

<current surface>.prompt

Dispatch Command contextPrompt

quoted-message facts (only when the window contains a quoted reply)
```

Blank sections are skipped. If every input is blank, `instruction` is omitted for an instruction-capable daemon. The quoted-message section is composed by Multica from the dispatch envelope rather than configured here; see [Agent Dispatch V2 execution contract](agent-dispatch-v2-execution-contract.md). `contextPrompt` is not Diamond configuration: it is dynamic, credential-free execution context supplied by the authenticated Router command and persisted only in private task context. Persisted Issue descriptions, comment content, chat messages, and assignment handoff notes are never rewritten with these instructions. At claim time, daemons advertising `task-instruction-v1` use this new composition through `instruction`; older daemon images bypass it, rebuild the previous structured DingTalk prompt, and receive that prompt as a temporary prefix in the existing task-content field they already consume.

Every continuation task recomposes the instruction at claim time from the latest valid Diamond snapshot. When Auto delegates to an Issue, Multica transfers the dynamic Router context, changes the private dispatch surface to `issue`, and recomposes `common + issue + context`; it does not copy the Auto prompt into the child task.

## Startup, updates, and shutdown

This section describes the always-active prompt/feature-rule document. The three managed Runtime documents use the separate fail-closed lifecycle described in [Managed runtime configuration](runtime-config.md): all initial reads and listeners are required when Diamond Runtime configuration is enabled.

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
| 2026-08-25 | Generalized the Agent override to per-segment `dispatch_prompt_overrides` and added a preview endpoint. | One override could only replace the managed policy; the reply-formatting and BUC segments appended after it were invisible and uncustomizable. |
| 2026-08-24 | Added the Agent-level `dispatch_prompt` override, which replaces `common` and the surface section for that Agent. | Let an Agent owner author the complete dispatch policy when the fleet-wide Diamond document does not fit that Agent's job, without forking the deployment configuration. |
| 2026-08-06 | Selected the new or legacy prompt builder directly from daemon capability. | Preserve complete old-image behavior while keeping legacy hard-coded policy out of instruction-capable runtime tasks. |
| 2026-08-06 | Added capability-gated selection between the new instruction path and the legacy claim path. | Bind prompt construction to the actual daemon consumer during rolling upgrades instead of guessing from runtime metadata. |
| 2026-08-04 | Added `common.prompt` and claim-time `common + mode + contextPrompt` composition through the independent task `instruction` field. | Keep fixed policy in Diamond, dynamic delivery facts in Router context, and all private instructions out of user-visible Issue, comment, chat, and handoff content. |
| 2026-08-04 | Removed the Diamond enable switch and made the provider start unconditionally with fail-open behavior. | Avoid silently skipping dynamic prompts when deployment configuration omits a redundant enable variable. |
| 2026-08-01 | Replaced the embedded auto-mode prompt with the strict `issue/chat/auto.prompt` Diamond document and enabled dynamic prompt injection for all three surfaces. | Keep surface behavior policy outside the binary and allow one atomic configuration update to control every dispatch mode. |
| 2026-08-01 | Added the optional Diamond-backed dynamic feature-flag provider. | Allow runtime configuration updates without restarting Multica while preserving fail-open behavior and configuration precedence. |
