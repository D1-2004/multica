# Managed runtime configuration

Alibaba-managed Multica deployments can move non-secret, operational settings from Aone environment traits into one Diamond document that updates without an application release.

This source is explicitly enabled with:

```text
MULTICA_RUNTIME_CONFIG_SOURCE=diamond
```

The coordinates are fixed so an application cannot accidentally point at another team's documents:

| Setting | Runtime settings | Runtime manifest fingerprints |
|---|---|---|
| Application | `dt-fde-multica` | `dt-fde-multica` |
| Data ID | `dt-fde-multica-runtime.json` | `dt-fde-multica-runtime-manifest-fingerprints.json` |
| Group | `DEFAULT_GROUP` | `DEFAULT_GROUP` |
| Type | `json` | `json` |

This document is separate from `dt-fde-multica.json`, whose strict schema contains dispatch prompts and uses fail-open feature-rule semantics.

## Source contract

There are two explicit deployment modes:

- An empty `MULTICA_RUNTIME_CONFIG_SOURCE` keeps the existing environment-only/self-hosted path.
- The exact value `diamond` makes the runtime settings and manifest-fingerprint documents authoritative. The server requires the initial fetch and listener registration for both Data IDs. It does not read a moved legacy environment value as a second source.

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

See [the complete example](runtime-config.example.json) for schema version 1.

## Runtime manifest fingerprint catalog

An m7 Runtime template alias contains a 16-character fingerprint instead of embedding every component version. The second managed document maps that fingerprint to the exact six-component contract used by the Runtime image builder. This keeps the existing alias and verification mechanism while allowing a newly built candidate image to become recognizable through a Diamond update rather than a Multica code change.

The document has one strict schema:

- `version` must be `1`;
- `fingerprints` must be non-empty;
- every key must be exactly 16 lowercase hexadecimal characters;
- every value must contain exactly `hermes`, `opencode`, `opencode-v2`, `dsh`, `pi`, and `dws`;
- the server independently recomputes `sha256(canonical m7 contract)[:16]` and rejects a key that does not match its component versions.

See [the complete fingerprint example](runtime-manifest-fingerprints.example.json). A valid listener update atomically replaces the entire catalog. An invalid update keeps the previous generation. Application logs contain only the Data ID, generation, entry count, and document SHA-256.

The environment-only/self-hosted path can still parse the explicit m1-m6 aliases. With no managed fingerprint catalog, unknown compact m7 aliases are deliberately ignored rather than guessed.

## Release procedure

1. Build the runtime-settings JSON from the current environment snapshot without placing secrets in it, and build the manifest-fingerprint JSON from the verified Runtime image contracts.
2. Validate the runtime-settings document with the same strict parser used by the server: `cd server && go run ./cmd/runtimeconfig -file /path/to/runtime.json` (add `-production` for the production document). Run `go test ./pkg/runtimeconfig` to validate the checked-in fingerprint example and canonical fingerprint contract.
3. Publish both Data IDs to the target Diamond unit before releasing the binary.
4. Add `MULTICA_RUNTIME_CONFIG_SOURCE=diamond` and `MULTICA_RUNTIME_LLM_API_KEY` to the target Aone environment trait while preserving the complete old trait snapshot.
5. Release the binary to that environment.
6. Verify every replica loaded the same Diamond digest and registered a listener.
7. Verify health, public config/model order, one real FC/E2B task, and one real ASB task.
8. Publish one harmless, reversible runtime update and verify both replicas switch generation without a release, then restore it.

Never reuse a pre-release document in production. Publish and verify each unit independently.
