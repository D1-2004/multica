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

## Managed model pricing

The model-pricing document is a strict, dynamically watched USD catalog. Every model listed in `runtime.llm.models` must have an exact price entry; the pricing document may be a superset so operators can publish a new price before adding the model to the Runtime catalog. This price-first order keeps every live generation valid.

Each model declares input, output, cache-read, and cache-write USD rates per million tokens. `base_tier_max_input_tokens` marks providers whose public list price changes with request size; aggregate task usage cannot recover each request's prompt size, so Multica uses the documented base tier as an estimate and labels it as a starting price in the model picker. Provider-reported cost remains authoritative when present.

At read time Multica applies the managed catalog to usage without provider cost. The same calculation feeds Runtime usage, workspace statistics, task execution costs, and label summaries/detail sorting. Hermes' transport-only `custom:` model prefix resolves to the exact managed model ID; unfamiliar variants are not priced by fuzzy prefix matching.

See [the complete pricing example](runtime-model-pricing.example.json) and [the source ledger](runtime-model-pricing.md). The checked-in rates use public list prices as of 2026-08-24; CNY prices use the official USD/CNY central parity snapshot recorded in the source ledger. Promotional discounts are not embedded.

## Release procedure

1. Build the runtime-settings JSON from the current environment snapshot without placing secrets in it, add a fingerprint entry only when a new provider combination is introduced, and build the model-pricing JSON from authoritative provider price sheets.
2. Validate the runtime-settings document with the same strict parser used by the server: `cd server && go run ./cmd/runtimeconfig -file /path/to/runtime.json` (add `-production` for the production document). Run `go test ./pkg/runtimeconfig ./pkg/modelpricing` to validate both managed catalogs.
3. Publish the model-pricing Data ID first, then the runtime settings and provider catalog, before releasing the binary.
4. Add `MULTICA_RUNTIME_CONFIG_SOURCE=diamond` and `MULTICA_RUNTIME_LLM_API_KEY` to the target Aone environment trait while preserving the complete old trait snapshot.
5. Release the binary to that environment.
6. Verify every replica loaded the same Diamond digest and registered a listener.
7. Verify health, public config/model order, one real FC/E2B task, and one real ASB task.
8. Publish one harmless, reversible runtime update and verify both replicas switch generation without a release, then restore it.

Never reuse a pre-release document in production. Publish and verify each unit independently.

## Change history

| Date | Change | Reason |
|---|---|---|
| 2026-08-30 | Reused `web.site_connect_src` as the hosted-site fetch proxy server-side origin allowlist while retaining the connector default and CSP behavior. | Client exact-URL declarations are untrusted; a live Diamond origin boundary lets the server authorize destinations without adding a second configuration contract. |
| 2026-08-30 | Added non-removable `'self'` to the hosted-site CSP `connect-src` defaults while retaining the connector domain and Diamond HTTPS origin additions. | Allow hosted feedback pages to use the Multica same-origin proxy without letting Diamond remove either default source. |
| 2026-08-30 | Added `web.site_connect_src` and the non-removable `https://connector.dingtalk.com` hosted-site CSP default. | Allow hosted feedback pages to call DingTalk AI Table webhooks without relaxing other CSP directives. |
