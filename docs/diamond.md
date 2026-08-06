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

The Diamond document contains the runtime prompt for each dispatch surface. These prompts are configuration, not source-code constants:

```json
{
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

The only allowed top-level keys are `issue`, `chat`, and `auto`; the only allowed field inside each section is the string field `prompt`. Sections may be omitted. `{}` is a valid empty configuration and atomically removes all Diamond prompts. Unknown sections, unknown fields, `null`, non-string prompts, malformed JSON, and trailing JSON values are rejected.

The server maps these sections to the internal feature-flag keys `dispatch_issue_runtime_prompt`, `dispatch_chat_runtime_prompt`, and `dispatch_auto_runtime_prompt`. This keeps the existing precedence contract available for emergency overrides and YAML fallback:

| Surface | Highest-priority environment override | YAML fallback key |
|---|---|---|
| `issue` | `FF_DISPATCH_ISSUE_RUNTIME_PROMPT` | `dispatch_issue_runtime_prompt` |
| `chat` | `FF_DISPATCH_CHAT_RUNTIME_PROMPT` | `dispatch_chat_runtime_prompt` |
| `auto` | `FF_DISPATCH_AUTO_RUNTIME_PROMPT` | `dispatch_auto_runtime_prompt` |

There is no mode-specific prompt embedded in the binary. When Diamond is unavailable or omits a section, and neither the corresponding `FF_*` override nor YAML rule supplies a value, Multica injects no mode-specific prompt. The common external-input safety policy and trusted DWS outbound workflow remain server-owned because they are security and protocol constraints rather than surface behavior configuration.

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
| 2026-08-04 | Removed the Diamond enable switch and made the provider start unconditionally with fail-open behavior. | Avoid silently skipping dynamic prompts when deployment configuration omits a redundant enable variable. |
| 2026-08-01 | Replaced the embedded auto-mode prompt with the strict `issue/chat/auto.prompt` Diamond document and enabled dynamic prompt injection for all three surfaces. | Keep surface behavior policy outside the binary and allow one atomic configuration update to control every dispatch mode. |
| 2026-08-01 | Added the optional Diamond-backed dynamic feature-flag provider. | Allow runtime configuration updates without restarting Multica while preserving fail-open behavior and configuration precedence. |
