# Managed runtime configuration

Alibaba-managed Multica deployments can move non-secret, operational settings from Aone environment traits into one Diamond document that updates without an application release.

This source is explicitly enabled with:

```text
MULTICA_RUNTIME_CONFIG_SOURCE=diamond
```

The coordinates are fixed so an application cannot accidentally point at another team's documents:

| Setting | Runtime settings | Runtime manifest fingerprints | Model pricing |
|---|---|---|---|
| Application | `dt-fde-multica` | `dt-fde-multica` | `dt-fde-multica` |
| Data ID | `dt-fde-multica-runtime.json` | `dt-fde-multica-runtime-manifest-fingerprints.json` | `dt-fde-multica-model-pricing.json` |
| Group | `DEFAULT_GROUP` | `DEFAULT_GROUP` | `DEFAULT_GROUP` |
| Type | `json` | `json` | `json` |

This document is separate from `dt-fde-multica.json`, whose strict schema contains dispatch prompts and uses fail-open feature-rule semantics.

## Source contract

There are two explicit deployment modes:

- An empty `MULTICA_RUNTIME_CONFIG_SOURCE` keeps the existing environment-only/self-hosted path.
- The exact value `diamond` makes the runtime settings, manifest-fingerprint, and model-pricing documents authoritative. The server requires the initial fetch and listener registration for all three Data IDs. It does not read a moved legacy environment value as a second source.

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

`web.site_connect_src` is an optional array of additional HTTPS origins for the hosted-site CSP `connect-src` directive. The server always includes `'self'` and `https://connector.dingtalk.com`; Diamond can add origins but cannot remove either default. At response time values are trimmed and normalized, duplicates are removed, and entries with a non-HTTPS scheme, user info, path, query, fragment, wildcard, or invalid CSP host characters are ignored. A missing, empty, or entirely invalid addition therefore leaves the safe same-origin and DingTalk connector defaults and never produces `*`. An invalid Diamond update retains the previous snapshot. Every hosted-site request reads the current runtime snapshot, so accepted listener updates apply without a restart.

```json
"site_connect_src": [
  "https://feedback-api.example.com"
]
```

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

## Managed model pricing

The model-pricing document is a strict, dynamically watched USD catalog. Every model listed in `runtime.llm.models` must have an exact price entry; the pricing document may be a superset so operators can publish a new price before adding the model to the Runtime catalog. This price-first order keeps every live generation valid.

Each model declares input, output, cache-read, and cache-write USD rates per million tokens. `base_tier_max_input_tokens` marks providers whose public list price changes with request size; aggregate task usage cannot recover each request's prompt size, so Multica uses the documented base tier as an estimate and labels it as a starting price in the model picker. Provider-reported cost remains authoritative when present.

At read time Multica applies the managed catalog to usage without provider cost. The same calculation feeds Runtime usage, workspace statistics, task execution costs, and label summaries/detail sorting. Hermes' transport-only `custom:` model prefix resolves to the exact managed model ID; unfamiliar variants are not priced by fuzzy prefix matching.

See [the complete pricing example](runtime-model-pricing.example.json) and [the source ledger](runtime-model-pricing.md). The checked-in rates use public list prices as of 2026-08-24; CNY prices use the official USD/CNY central parity snapshot recorded in the source ledger. Promotional discounts are not embedded.

## Release procedure

1. Build the runtime-settings JSON from the current environment snapshot without placing secrets in it, build the manifest-fingerprint JSON from verified Runtime image contracts, and build the model-pricing JSON from authoritative provider price sheets.
2. Validate the runtime-settings document with the same strict parser used by the server: `cd server && go run ./cmd/runtimeconfig -file /path/to/runtime.json` (add `-production` for the production document). Run `go test ./pkg/runtimeconfig ./pkg/modelpricing` to validate both managed catalogs.
3. Publish the model-pricing Data ID first, then the runtime settings and manifest fingerprints, before releasing the binary.
4. Add `MULTICA_RUNTIME_CONFIG_SOURCE=diamond` and `MULTICA_RUNTIME_LLM_API_KEY` to the target Aone environment trait while preserving the complete old trait snapshot.
5. Release the binary to that environment.
6. Verify every replica loaded the same Diamond digest and registered a listener.
7. Verify health, public config/model order, one real FC/E2B task, and one real ASB task.
8. Publish one harmless, reversible runtime update and verify both replicas switch generation without a release, then restore it.

Never reuse a pre-release document in production. Publish and verify each unit independently.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-30 | Added non-removable `'self'` to the hosted-site CSP `connect-src` defaults while retaining the connector domain and Diamond HTTPS origin additions. | Allow hosted feedback pages to use the Multica same-origin proxy without letting Diamond remove either default source. |
| 2026-08-30 | Added `web.site_connect_src` and the non-removable `https://connector.dingtalk.com` hosted-site CSP default. | Allow hosted feedback pages to call DingTalk AI Table webhooks without relaxing other CSP directives. |
