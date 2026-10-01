# Managed runtime configuration

Alibaba-managed Multica deployments can move non-secret, operational settings from Aone environment traits into one Diamond document that updates without an application release.

This source is explicitly enabled with:

```text
MULTICA_RUNTIME_CONFIG_SOURCE=diamond
```

The coordinates are fixed so an application cannot accidentally point at another team's documents:

| Setting | Runtime settings | Runtime provider catalog | Model pricing |
|---|---|---|---|
| Application | `dt-fde-multica` | `dt-fde-multica` | `dt-fde-multica` |
| Data ID | `dt-fde-multica-runtime.json` | `dt-fde-multica-runtime-manifest-fingerprints.json` | `dt-fde-multica-model-pricing.json` |
| Group | `DEFAULT_GROUP` | `DEFAULT_GROUP` | `DEFAULT_GROUP` |
| Type | `json` | `json` | `json` |

This document is separate from `dt-fde-multica.json`, whose strict schema contains dispatch prompts and uses fail-open feature-rule semantics.

## Source contract

There are two explicit deployment modes:

- An empty `MULTICA_RUNTIME_CONFIG_SOURCE` keeps the existing environment-only/self-hosted path.
- The exact value `diamond` makes the runtime settings and model-pricing documents authoritative. The provider catalog is loaded and watched independently; an unavailable or invalid provider catalog does not block application startup.

The document is decoded with unknown-field rejection and validated as a complete snapshot. A valid listener update atomically replaces the previous snapshot. An invalid later update is rejected and the last valid snapshot remains active. Logs contain only the Data ID, group, schema version, generation, model count, and SHA-256 digest; the document body is not logged.

## Environment values that remain

Diamond is not a secret store. Keep credentials, private keys, encryption/signing keys, and startup topology in Aone environment traits. In particular:

- `DATABASE_URL`, `JWT_SECRET`, `APP_ENV`, and `MULTICA_RUNTIME_CONFIG_SOURCE`;
- `MULTICA_RUNTIME_LLM_API_KEY`, shared by FC/E2B and ASB;
- `MULTICA_FC_E2B_API_KEY`, `MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET`, and `MULTICA_ASB_WG_CLIENT_CREDENTIALS`;
- `MULTICA_BUC_CLIENT_ID`, `MULTICA_BUC_CLIENT_SECRET`, and `MULTICA_BUC_AGENT_ID`;
- dispatch, sandbox-relay, channel, OAuth, storage, Redis, email, GitHub App, CloudFront, and other service credentials/keys;
- static process topology such as ports, local paths, repository synchronization, connection ownership, and rate-limit settings.

The first managed rollout deliberately keeps the old migrated environment values in place for release rollback. In Diamond mode the new binary ignores those values. Remove them only in a later, separately reviewed environment cleanup after pre-release and production acceptance.

## Migrated settings

The following legacy environment settings are represented by the runtime document:

| JSON section | Replaces |
|---|---|
| `web` | `ATTACHMENT_DOWNLOAD_MODE`, `CORS_ALLOWED_ORIGINS`, `FRONTEND_ORIGIN`, `LOGIN_PROVIDERS`, `MULTICA_APP_URL`, `MULTICA_PUBLIC_URL`, `LOCAL_UPLOAD_BASE_URL` |
| `integrations` | `AGENT_MESSAGE_ROUTER_INTERNAL_URL`, `DINGTALK_DBASE_BINDING_ORIGIN`, `DINGTALK_DBASE_BINDING_PAGE_URL`, `GITHUB_API_BASE_URL`, `MULTICA_DINGTALK_REGISTRATION_BASE_URL`, `MULTICA_DINGTALK_REGISTRATION_OUTGOING_URL` |
| `features.workspace_access_tokens` | `FF_WORKSPACE_ACCESS_TOKENS` |
| `runtime.fc_e2b` | non-secret `MULTICA_FC_E2B_*` enablement, template, endpoint, domain, publisher allowlist, and timeout settings |
| `runtime.asb` | non-secret `MULTICA_ASB_*` enablement, endpoints, timeouts, CPU, and memory settings |
| `runtime.llm` | the former FC/E2B and ASB OpenAI-compatible base URLs and model lists, now one shared catalog with an explicit default model |
| `agent_identity` | non-secret `MULTICA_AGENT_IDENTITY_*` endpoints, timeout, and pre-release debug controls |
| `enterprise_identity` | non-secret `MULTICA_ENTERPRISE_IDENTITY_*`, `MULTICA_BUC_*`, `MULTICA_AUTHX_*`, and `MULTICA_IDEM_*` settings |

`runtime.llm.default_model` must name an entry in `runtime.llm.models`. The server always exposes and injects the default model first, so FC/E2B and ASB use the same default and catalog.

`runtime.fc_e2b.stable_publisher_user_ids` is also live: both stable-release authorization and developer-first rollout classification read the current Diamond snapshot, so list changes do not require an application release.

`web.site_connect_src` is an optional array of additional HTTPS origins for both the hosted-site CSP `connect-src` directive and the server-side hosted-site fetch proxy origin allowlist. The server always includes `'self'` in CSP and `https://connector.dingtalk.com` in both controls; Diamond can add HTTPS origins but cannot remove either default. At response or proxy-request time values are trimmed and normalized, duplicates are removed, and entries with a non-HTTPS scheme, user info, path, query, fragment, wildcard, or invalid CSP host characters are ignored. A missing, empty, or entirely invalid addition therefore leaves the safe same-origin and DingTalk connector defaults and never produces `*`. An invalid Diamond update retains the previous snapshot. Every hosted-site response and proxy request reads the current runtime snapshot, so accepted listener updates apply without a restart. This setting authorizes origins only: the proxy separately validates the exact HTTPS target, resolved addresses, method, body and response limits, timeout, redirects, and safe headers.

```json
"site_connect_src": [
  "https://feedback-api.example.com"
]
```

See [the complete example](runtime-config.example.json) for schema version 1.

## AgenticFS quota defaults

`runtime.agentic_fs` holds non-secret defaults for new employee filesystems:

```json
"agentic_fs": {
  "size_limit": 107374182400,
  "file_count_limit": 1000000000
}
```

`size_limit` is bytes (100 GiB above). `file_count_limit` is a count (one billion above). Missing sections use these defaults. Every new provisioning request reads the current Diamond snapshot; accepted changes need no application restart. An existing provisioning intent retains its persisted quota across retries. Existing cloud spaces are unchanged; use the NAS quota API to resize those explicitly.

`runtime.agentic_fs.placement` contains `account_id`, `region`, `zone`, `team_id`, `file_system_id`, `vpc_id`, `security_group_id`, and `vswitch_ids`. `runtime.agentic_fs.credential_resource` contains the Normandy access-package resource reference, not key material. Actual credentials continue to resolve through the managed provider. When placement is present, the entire legacy `MULTICA_DSH_STORAGE_CONFIG` value is ignored, even if malformed. Logs explicitly record `source=diamond` and effective quotas.

During initial migration, a missing placement permits the existing deployment configuration so rolling instances keep provisioning. After publishing complete placement and verifying `source=diamond`, remove `MULTICA_DSH_STORAGE_CONFIG` from both Aone environment traits. Subsequent starts and provisioning use Diamond without that environment variable. Environment-only/self-hosted deployments retain the existing explicit environment mode.

For the initial schema rollout, deploy this binary against the existing document first, then add `runtime.agentic_fs` separately in `pre` and `sh`. Older binaries reject unknown fields, so do not publish the new section while older replicas might restart. Subsequent quota changes only require a Diamond configuration publication.

## Runtime provider catalog

The second managed document maps one opaque 16-character fingerprint to one provider combination. FC reads the fingerprint from the display alias; ASB release inputs carry the same fingerprint explicitly. The key is never recomputed from component versions and is not an image-integrity check. Releasing another image with the same provider combination does not require a Diamond update. Adding a provider creates one new fingerprint entry while retaining old combinations.

The document has one strict schema:

- `version` must be `1`;
- `fingerprints` is an object and may be empty;
- every key is exactly 16 lowercase hexadecimal characters;
- every value is a duplicate-free provider list with no exact-count constraint;
- template IDs, image references, commits, component versions, and capabilities are not stored in this document.

See [the complete provider example](runtime-manifest-fingerprints.example.json). A valid listener update atomically replaces the entire catalog. An invalid update keeps the previous generation. Application logs contain only the Data ID, generation, entry count, and document SHA-256. The existing Data ID name is retained for deployment compatibility; its content is no longer a fingerprint or component-version document.

A compact m7 alias is display text and carries the opaque provider-combination fingerprint. It is not the execution identity and does not encode component versions. FC execution identity remains the template ID. Templates whose fingerprint is not in Diamond stay visible with no selectable providers, so they cannot be used for new Runtime creation or stable publication.

## FC/E2B SDK rollout

`runtime.fc_e2b_sdk_rollout` in `dt-fde-multica-runtime.json` is the single switch of the FC/E2B SDK change. For a selected scope it

- sends FC/E2B commands (sandbox create, exec, template lookup) through the in-process Go SDK instead of the `e2b` CLI subprocess;
- injects `FC_E2B_TASK_ID` into the runner, and
- ends a cancelled or failed task's processes in the sandboxes it used (see `docs/fc-sandbox-lifecycle.md`).

An unselected scope keeps the behavior from before the change: the CLI, no marker, and an aborted task's processes are left to the sandbox release.

```json
"fc_e2b_sdk_rollout": {
  "enabled": true,
  "workspace_ids": ["<workspace uuid>"],
  "agent_ids": ["<agent uuid>"],
  "runtime_ids": ["<runtime uuid>"],
  "percent": 0
}
```

- `enabled` is the master switch. `false`, or the whole key absent, selects nothing whatever the lists say. Switch-off value: `{"enabled": false}`.
- An operation is selected when its agent, runtime, or workspace is listed, or when its agent (else runtime, else workspace) falls in the `percent` bucket. `percent: 100` also covers operations without a scope, such as the stable-channel template scan.
- Identifiers must be canonical lowercase UUIDs without duplicates; `percent` must be 0–100. Like any invalid runtime document, a rejected publication keeps the previous generation.
- Operations that freeze one runtime snapshot route every command they send by it: a launch freezes one when it starts, and a stop of a cancelled or failed task freezes one when it is scheduled and uses it for both of its passes. A publication applies to such operations that start after it, without a restart; operations already running finish under their snapshot. A selected stop therefore still runs its second pass after a switch-off, and a stop that was not selected is not started after a switch-on. Commands sent outside such an operation, such as the template list, read the live document each time.
- Each operation routed to the SDK logs `FC/E2B SDK transport` with `rollout_source=snapshot|live`, the outcome and, for a failure, `error_kind` (`exit`, `output_limit`, `deadline`, `canceled`, `failed`) with the exit code or the transport cause. Commands, environment values, and command output are never logged there. A stop the rollout cannot select is not scheduled at all; one refused because 512 stops are already pending logs `event=fc_e2b_task_processes_stopped outcome=backlog_full`, and a scheduled stop whose sandbox scope turns out unselected logs `outcome=disabled`.

This key replaces the separate Data ID `dt-fde-multica-fc-e2b-sdk-rollout.json` and the environment fallback `MULTICA_FC_E2B_SDK_ROLLOUT`; neither is read any more.

Rollout order: binaries older than this key reject a runtime document that carries it. Follow "Adding a runtime-document key" under the release procedure: every replica runs the supporting binary before the key is published, and the key is removed before an older binary is released.

## DingTalk calls through the DWS SDK

`runtime.use_dws_for_tag` (boolean, default false) sends the server's DingTalk calls through the in-process DWS gateway SDK (`server/pkg/dws`, synced from dws-for-tag by `scripts/sync-dws.sh`) instead of spawning the `dws` CLI:

```json
"use_dws_for_tag": true
```

- It is a server switch. Sandboxes are unaffected: the daemon and the dws shim keep the dws CLI, and sandbox-side shortcuts ship as the `dws-shortcuts` skill (dws-for-tag `skill/publish.py`), iterated by publishing a new skill version rather than through the server or the image.
- It covers everything `server/internal/dwsclient` does: the AuthCode exchange, Coordinator and Scene Memory history reads, cross-org read renewal, message sends and quote replies, send-status queries, sender lookups, A2UI decision cards and the card-action event stream.
- Callers are unchanged. `server/pkg/dws/clicompat` builds the tool arguments dws v1.0.62-beta.8 sends and renders what it prints, including its success rules and error JSON, so the existing parsers read the same bytes.
- The switch is read live, once per operation: the session an operation opens picks the transport, and every later call of that operation follows it. A publication therefore applies to operations that start after it, and a running operation finishes on the transport it started with; the exception is a decision session's card consumer, which ends within seconds of a switch-off so the decision service reopens it on the dws CLI.
- Credentials are shared, not per call. An identity (agent, DWS user, organization) is exchanged once; every operation and every replica then uses its token, and an Agent Identity context is minted only when no usable token exists. Tokens are stored in the store Redis, sealed with `MULTICA_DINGTALK_SECRET_KEY` under a per-deployment prefix, and refreshed by one replica at a time under a Redis lock, because the refresh token rotates. Without Redis or the key, tokens stay per process. Agent Identity therefore records the context of the first operation that minted, not one per call.
- Event streams are the only state a replica holds. `server/internal/dwseventsource` is the server's DWS WebSocket event source: one DWS personal event stream per identity that some consumer needs (today: senders with open user decisions, for `user_card_action_triggered`), however many identities the deployment hosts. Where each stream runs is `server/internal/connmgr`'s business, a business-free connection manager built on the robot connector's proven design (the robot connector itself is unchanged): a Redis coordinator with one READY member per identity places each stream, a dropped stream is released and claimed again with backoff, a crashed holder is replaced after the lease TTL, and on shutdown the stream keeps consuming until another replica's replacement is READY, so rolling deploys hand streams over. A replica already holding more streams than the least-loaded replica waits longer before claiming, and replicas heartbeat their load into Redis so one holding more than its fair share hands one stream per poll interval to a less-loaded replica through the same drain handoff; streams therefore spread again after a rolling deploy. Each stream pings the gateway every minute and treats the pong as proof of life, because an idle gateway sends nothing for longer than the three-minute read deadline. Event dedupe, subscription records and a short-lived per-identity ready marker live in Redis; delivery is at least once (a handoff overlaps two streams), and the decision service dedupes card actions by event id; any replica persists a card action through the decision service, and a decision session waits for its identity's stream instead of opening one. Switching on ends a card consumer still running on the dws CLI within a second, and switching off ends a session waiting on a stream at once; either way the decision service reopens the identity on the selected transport.
- Known difference: dws release binaries decrypt SafeChat (encrypted-group) messages with a native library. The SDK cannot; such messages keep their ciphertext in `content`, as dws does when its own decryption fails.
- The same key gates the native subscription event source (`h.DWSNativeEvents`): a second `dwseventsource` source, with its own Redis prefix, that streams `user_im_message_receive_at` and `user_im_message_receive_o2o_all` for every execution identity with native subscription on. It always dials the production DWS gateway (`mcp.dingtalk.com`), whatever this deployment is, and its replies are pinned to production through `dws_environment` on the managed response action. Switched off, the source holds no streams; native callbacks already queued still drain through the native completion worker. The per-agent switch (`PUT …/dingtalk/account-bindings/{agentId}/native-subscription`) selects identities; `GET` on the same path reports the identity's stream state for the identity card's indicator (`unavailable` while this key is off). See "Native subscription ingress" in `docs/inbound-coordinator-loop.md`.
- Behavior rollback: publish `false` or remove the key; no release is needed.

Rollout order: as for any new key, release the binary that knows it to every replica first, then publish it (see "Adding a runtime-document key").

## Managed model pricing

The model-pricing document is a strict, dynamically watched USD catalog. Every model listed in `runtime.llm.models` must have an exact price entry; the pricing document may be a superset so operators can publish a new price before adding the model to the Runtime catalog. This price-first order keeps every live generation valid.

Each model declares input, output, cache-read, and cache-write USD rates per million tokens. `base_tier_max_input_tokens` marks providers whose public list price changes with request size; aggregate task usage cannot recover each request's prompt size, so Multica uses the documented base tier as an estimate and labels it as a starting price in the model picker. Provider-reported cost remains authoritative when present.

At read time Multica applies the managed catalog to usage without provider cost. The same calculation feeds Runtime usage, workspace statistics, task execution costs, and label summaries/detail sorting. Hermes' transport-only `custom:` model prefix resolves to the exact managed model ID; unfamiliar variants are not priced by fuzzy prefix matching.

See [the complete pricing example](runtime-model-pricing.example.json) and [the source ledger](runtime-model-pricing.md). The checked-in rates use public list prices as of 2026-08-24; CNY prices use the official USD/CNY central parity snapshot recorded in the source ledger. Promotional discounts are not embedded.

## Release procedure

Older binaries reject runtime-document keys they do not know: a starting replica refuses to start, and a running replica keeps its previous snapshot. Every step below therefore keeps the document readable by every binary that may run against it.

### First adoption of the Diamond source

Use this once, when an environment moves from environment traits to Diamond. Its binaries do not read Diamond before the release, so the document can be published first, but it may only contain keys the binary being released understands.

1. Build the runtime-settings JSON from the current environment snapshot without placing secrets in it, add a fingerprint entry only when a new provider combination is introduced, and build the model-pricing JSON from authoritative provider price sheets.
2. Validate the runtime-settings document with the same strict parser used by the server: `cd server && go run ./cmd/runtimeconfig -file /path/to/runtime.json` (add `-production` for the production document). Run `go test ./pkg/runtimeconfig ./pkg/modelpricing` to validate both managed catalogs.
3. Publish the model-pricing Data ID first, then the runtime settings and provider catalog, before releasing the binary.
4. Add `MULTICA_RUNTIME_CONFIG_SOURCE=diamond` and `MULTICA_RUNTIME_LLM_API_KEY` to the target Aone environment trait while preserving the complete old trait snapshot.
5. Release the binary to that environment.
6. Verify every replica loaded the same Diamond digest and registered a listener.
7. Verify health, public config/model order, one real FC/E2B task, and one real ASB task.
8. Publish one harmless, reversible runtime update and verify both replicas switch generation without a release, then restore it.

### Adding a runtime-document key

Use this for every new key, such as `fc_e2b_sdk_rollout` or `use_dws_for_tag`.

1. Release the binary that knows the key to every replica, with the document unchanged. Verify every replica runs the new build and logs `runtime Diamond config loaded` with the same generation and SHA-256, and that `mw diamond listener` shows each replica listening on the current MD5.
2. Validate the new document with the released commit: `cd server && go run ./cmd/runtimeconfig -file /path/to/runtime.json` (add `-production` for production). The same command at the previous release commit must reject it with `unknown field`, which confirms the older binary cannot read it.
3. Publish the document with the key at its off or narrowest value. Verify every replica logs `runtime Diamond config updated` with the new generation and SHA-256.
4. Widen or switch the key in later single publications, verifying each on every replica.

### Hot updates and rollback

- A publication applies to operations that start after every replica logs its generation. Operations that froze a snapshot finish under it, as each key describes. An invalid publication is rejected by every replica, which keeps its previous generation and logs `runtime Diamond update rejected; retaining previous snapshot`.
- Behavior rollback: publish the key switched off (for example `{"enabled": false}`); no release is needed.
- Binary rollback: first publish the document without every key the older binary does not know (for this batch `fc_e2b_sdk_rollout` and `use_dws_for_tag`), verify every replica logged the new generation, then release the older binary. An older binary released while the document still carries such a key cannot start.

Never reuse a pre-release document in production. Publish and verify each unit independently.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-10-01 | `runtime.use_dws_for_tag` also gates the native subscription event source. | Native subscription reuses the DWS SDK event streams; no separate Diamond key or environment variable. |
| 2026-09-30 | Added `runtime.use_dws_for_tag`. | Move the server's DingTalk calls from the dws subprocess to the in-process SDK behind a live switch. |
| 2026-09-30 | The process stop also covers failed tasks; its second pass runs 10 seconds after the task ended; moved the references to `runtime.performance_optimization` out of this document. | A failed task leaves the same orphans as a cancelled one; that key is implemented and documented by the PRI-47 change, which is not on this branch yet. |
| 2026-09-28 | Split the release procedure into first adoption, adding a key, and hot updates and rollback; a cancelled-task stop now freezes one snapshot for both passes. | The general steps published the document before the binary, which older replicas reject once it carries a new key; each stop pass reread the switch (PRI-67). |
| 2026-09-28 | Moved the FC/E2B SDK rollout into `runtime.fc_e2b_sdk_rollout`, which also gates the cancelled-task stop and its runner marker; dropped the separate Data ID and `MULTICA_FC_E2B_SDK_ROLLOUT`. | One runtime document carries every rollout switch; the SDK change and the performance batch keep separate, independent switches (PRI-47, option B). |
| 2026-08-30 | Reused `web.site_connect_src` as the hosted-site fetch proxy server-side origin allowlist while retaining the connector default and CSP behavior. | Client exact-URL declarations are untrusted; a live Diamond origin boundary lets the server authorize destinations without adding a second configuration contract. |
| 2026-08-30 | Added non-removable `'self'` to the hosted-site CSP `connect-src` defaults while retaining the connector domain and Diamond HTTPS origin additions. | Allow hosted feedback pages to use the Multica same-origin proxy without letting Diamond remove either default source. |
| 2026-08-30 | Added `web.site_connect_src` and the non-removable `https://connector.dingtalk.com` hosted-site CSP default. | Allow hosted feedback pages to call DingTalk AI Table webhooks without relaxing other CSP directives. |

### Coordinator model

`runtime.llm.coordinator_model` selects the Coordinator main-loop and finish-review model independently of `default_model` and sandbox model selection. Set it to `qwen3.8-max`. Each decision snapshots the current value; Diamond updates affect subsequent decisions without a restart. Thinking remains disabled (`enable_thinking=false`, `reasoning_effort=none`). Omission or blank retains the prior `qwen3.7-plus` during migration. Deploy supporting binaries on every replica before adding this field, because older binaries reject unknown fields.
