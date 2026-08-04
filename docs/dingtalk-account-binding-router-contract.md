# DingTalk Account Binding Router Contract

Multica and Agent Message Router communicate in both directions, with two
independent address contracts.

## Multica to Router

Multica calls Router through the fixed HTTP base URL configured for the Router
client. The request destination is always the Agent Message Router service; it
is never derived from a binding record or a Multica callback address.

```text
AGENT_MESSAGE_ROUTER_INTERNAL_URL + /api/account-binding-tokens
```

To issue a short-lived DingTalk account-binding credential, Multica sends the
Agent descriptor and the environment-neutral callback path as business data.
This extends the existing endpoint in place; there is no V2 endpoint:

```http
POST /api/account-binding-tokens
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "agentId": "<agent UUID>",
  "name": "<Agent display name>",
  "workspace": {
    "id": "<workspace UUID>",
    "name": "<workspace display name>"
  },
  "dispatchPath": "/api/webhooks/agent-dispatch/<endpointId>"
}
```

The begin handler loads the Agent with `GetAgentInWorkspace` and then loads the
Workspace by that workspace ID. `name`, `workspace.id`, and `workspace.name`
therefore come from the database, never from request-body display fields.
`workspace.id` is serialized as a standard lowercase, hyphenated UUID string.

Before issuing the Router request, Multica creates one normalized descriptor
snapshot. Agent and Workspace names are trimmed, must remain non-empty, may
contain at most 256 Unicode code points, and must not contain C0 or C1 control
characters. Invalid authoritative names fail begin explicitly; Multica neither
truncates nor silently renames them.

Router returns the credential and its expiry. It does not return a callback
origin or a full callback URL:

```json
{
  "bindingToken": "<short-lived token>",
  "expiresAt": "<RFC3339 timestamp>"
}
```

Multica uses that same normalized descriptor snapshot for the Router request and
the QR-code fragment. The fragment retains all existing fields and adds three
display fields:

```text
bindingMode
bindingToken
agentId
agentName
workspaceId
workspaceName
dispatchPath
callbackUrl
callbackToken
expiresAt
```

The fragment is encoded with Go `url.Values`; names containing spaces, Unicode,
`&`, `+`, `/`, `?`, or `#` therefore remain field-safe after browser decoding.
`bindingToken` and `callbackToken` stay in the fragment, are never moved to the
query string, and must not be logged. Multica keeps `dispatchPath` in the
pending binding and does not replace its local dispatch endpoint with a URL
supplied by Router.

The names are immutable display snapshots for the binding page. They do not
participate in authentication, authorization, routing ownership, execution
identity, or sender selection. The existing `message` / `identity` modes,
organization selection, message scope, callback contract, and later binding
behavior are unchanged.

### Binding operation authorization

The binding-list endpoint remains visible to workspace members so an operator
without mutation permission can inspect the current digital-employee and
execution-identity state.

Starting either binding mode, changing the processing surface, and unbinding
require both of the existing Agent permission decisions:

1. The member can manage the Agent: they are the Agent owner or a workspace
   owner/admin.
2. The member can invoke the Agent under `permission_mode` and
   `agent_invocation_target`. Workspace administration does not bypass a
   private Agent's invocation policy.

An operator who fails either decision receives HTTP 403:

```json
{
  "code": "agent_binding_forbidden",
  "error": "无权限操作，请联系此智能体管理员处理"
}
```

The browser applies the same decision before mutation and keeps the binding
card readable. The server check remains authoritative for stale clients and
direct API calls.

### Inspecting and changing the processing surface

For an active message binding, Multica reads the Router subscription with the
same service credential:

```http
GET /api/subscriptions/<sourceId>
Authorization: Bearer <service credential>
```

The successful response includes the current processing surface and outbound
policy. `surface.type` is `issue`, `chat`, or `auto`; the DBase-created message
binding keeps `outbound` fixed to `dws` / `latest_message`. Multica preserves
and displays `auto` as the binding fact. Router materializes inbound `auto`
messages through its `chat` dispatch path, where the Agent decides whether to
answer directly or create an issue for background work.

Multica changes only the processing surface through the owner-checked Router
endpoint:

```http
PATCH /api/subscriptions/<sourceId>/surface
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "agentId": "<agent UUID>",
  "surface": { "type": "auto" }
}
```

Router returns the complete active subscription. Multica verifies that the
source, Agent, dispatch target, status, requested surface, and existing
outbound policy are unchanged before persisting the local surface snapshot.
This operation updates the existing subscription and does not issue a new
binding credential.

### Message-account display snapshot

The DBase completion callback may include these optional fields under
`message_binding`:

```json
{
  "account_display_name": "<DingTalk account nickname>",
  "account_avatar_url": "https://..."
}
```

They describe the DingTalk account listening for messages. Multica stores them
with the message route for display only; they do not populate
`identity_binding` and do not become the Agent execution identity.

### Binding failure details

When the DBase completion callback reports a failed message binding, it includes
a safe, user-facing error:

```json
{
  "message_binding": {
    "status": "failed",
    "error": {
      "code": "source_already_bound",
      "message": "消息源已绑定给其他 Agent，请解绑后重试",
      "retryable": false
    }
  }
}
```

Multica persists this error with the pending binding and exposes it as
`message_route.error` from the binding-list API. The UI renders `message`
instead of collapsing every terminal result into a generic failed status.
`code` remains the stable diagnostic and automation key; `message` must already
be safe to display, and callback validation rejects invalid codes, empty or
oversized messages, and control characters.

### Account key and current binding ownership

A digital-employee binding is a control-plane ownership fact, not a separate
Router entity. The account key and binding key are:

```text
accountKey = platform + tenantId + accountId
bindingKey = accountKey + agentId
```

The dedicated API fixes `sourceType=digital_employee`. If a future shared API
also manages robots, `sourceType` must become an explicit part of both keys.
`domain` is deliberately absent: Router owns one `agent_data_source` row per
business domain, while users bind and unbind the selected domain set as one
digital-employee operation.

This control-plane key does not redefine inbound message identity. Inbound
routing continues to resolve a source by its established platform, domain, and
account fields; an event-supplied tenant is not a trusted routing discriminator.
For binding control-plane calls, however, Router requires the trusted
`tenantId` and compares it exactly with the persisted source tenant before it
reads or mutates ownership.

`sourceId` remains a Router-owned per-domain locator. Multica may retain the
channel `router_source_id` for subscription detail and surface updates, but it
must not use one domain source as the cross-domain binding identity.

Router also enforces the current product constraint that one Agent owns at most
one digital-employee account. Binding and takeover lock the target Agent before
checking its active account groups; a different existing account returns
`agent_already_bound` and is never silently replaced.

### Batch reconciliation

Multica checks active local projections in one authenticated batch:

```http
POST /api/digital-employee-bindings/check
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "bindings": [
    {
      "agentId": "<agent UUID>",
      "platform": "dingtalk",
      "tenantId": "<organization ID>",
      "accountId": "<digital employee UID>",
      "expectedDomains": ["channel", "calendar", "approval"]
    }
  ]
}
```

Router resolves every active domain source for the account key and returns one
result per request in the same order. Result status is one of:

- `valid`: every expected domain exists and every active domain belongs to the
  requested Agent;
- `unbound`: the account has no active binding;
- `bound_to_other_agent`: every active domain belongs to one different Agent;
- `inconsistent`: an expected domain is missing, an unexpected partial state
  exists, or active domains have different owners.

A `bound_to_other_agent` result includes `currentAgentId`. An `inconsistent`
result does not nominate an owner. Router unavailability is an HTTP failure;
Multica derives `router_unavailable` locally and must not rewrite or remove the
stored projection. The UI keeps the unbind action and displays:

- `bound_to_other_agent`: `该数字员工已经绑定到其他智能体，消息订阅已失效`
- `inconsistent`: `该数字员工已经绑定到其他智能体，消息订阅已失效`
- `router_unavailable`: `暂时无法核验消息订阅状态`

Reconciliation never mutates Router or Multica state.

### Conditional account-level unbind

User-initiated Multica DELETE never trusts a reconciliation status previously
rendered by the UI. It reloads the active local projection, builds the complete
binding key and expected domain set from the persisted config, and performs a
fresh batch-check request for that one key.

- `valid`: send the exact binding key to Router's conditional unbind, then
  revoke the exact local projection by account-key compare-and-swap;
- `unbound` or `bound_to_other_agent`: do not call Router unbind; revoke only
  the exact stale local projection by account-key compare-and-swap;
- `inconsistent`: preserve the local projection and return a conflict;
- transport, timeout, authentication, Router 5xx, result-count mismatch, or
  malformed/mismatched response: preserve the local projection and return a
  retryable verification failure.

The conditional unbind request for the `valid` branch is:

```http
POST /api/digital-employee-bindings/unbind
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "agentId": "<agent UUID>",
  "platform": "dingtalk",
  "tenantId": "<organization ID>",
  "accountId": "<digital employee UID>"
}
```

Router locks the account's active domain bindings before acting. It unsubscribes
all domains only when all of them still belong to the requested Agent. It
returns `unbound` when the relationship was removed or was already absent,
`ownership_changed` when the account now belongs to another Agent, and
`inconsistent` for mixed or partial ownership. The latter two outcomes never
remove a Router binding. Multica may revoke only the local projection whose
complete stored binding key equals the request; a row absent from the current
environment is an idempotent no-op.

The existing Agent-wide digital-employee delete is not used for a user's local
unbind. It would be too broad when a stale environment still shows one account
after that Agent has subsequently bound another account.

The check and mutation are deliberately separate network calls. If ownership
changes after a `valid` check, Router's conditional unbind returns
`ownership_changed` without deleting the new winner; Multica may then revoke
only its exact old local projection. The local compare-and-swap also prevents a
concurrent replacement projection from being cleared.

### Conflict takeover

The first binding request always uses `replaceExistingBinding=false`. When the
account is already owned by another Agent, Router returns `source_already_bound`
with safe details `{ "boundAgentId": "<current Agent>" }`; the authenticated
request already carries the account key. The binding page shows a dedicated
error step with `解除原绑定并继续` and `退出`.

Confirmation repeats the authenticated binding request with:

```json
{
  "replaceExistingBinding": true,
  "expectedCurrentAgentId": "<Agent reported by the conflict>"
}
```

Router treats all domain rows for the account as one compare-and-swap. It
switches them only if they still belong to `expectedCurrentAgentId`; an absent
binding may be created and an already-current `newAgentId` is idempotent. A new
owner produces a refreshed conflict, and mixed ownership produces
`inconsistent`. Retargeting does not unsubscribe and recreate unchanged
DingTalk listeners.

The success callback includes the canonical account key and, only after a
takeover, `previous_agent_id`. Multica uses that tuple only as a local cleanup
hint in the database receiving the callback. It never calls Router while
cleaning the old local projection.

### Historical account-key enrichment

New successful bindings persist `router_platform`, `router_tenant_id`, and
`router_account_id` beside the existing Agent and channel `router_source_id`.
Historical active projections missing those fields are enriched from the
Router source named by their stored `router_source_id`. Router must return the
persisted trusted account identity for that service-authenticated lookup.

```http
POST /api/digital-employee-bindings/source-identities
Authorization: Bearer <service credential>
Content-Type: application/json

{ "sourceIds": ["source-channel"] }
```

Each found result contains `sourceId`, `platform`, `tenantId`, `accountId`,
`sourceType`, and `domain`; missing IDs are returned separately. This read does
not require a current `agent_binding` row and never mutates source ownership.

The enrichment is dry-run by default and writes through a full-snapshot CAS. It
does not require the source to remain owned by the local Agent, because that is
the state reconciliation is intended to discover; it does require the source
to be a DingTalk digital-employee channel source. Rows with a missing source,
missing trusted tenant, malformed local config, or a concurrent change remain
unchanged for audit. No binding ID or Router relation table is introduced.

## Router to Multica

`dispatchPath` identifies the Multica webhook but deliberately contains no
environment origin. Router persists the canonical path in its shared database.
When Router actually dispatches a task, the running Router environment combines
that path with its own configured Multica callback origin:

```text
MESSAGE_ROUTER_MULTICA_DISPATCH_ORIGIN + dispatchPath
```

### Dispatch rejection details

Multica non-2xx dispatch responses include a JSON error body. For example, the
runtime invocation gate currently returns HTTP 403 with:

```json
{
  "error": "dispatch member cannot invoke agent"
}
```

The current Router dispatch client reads this response body and persists it in
`dispatch_task.response_body`; its terminal task log also includes a bounded
`responseSummary`. Router separately records the normalized
`failureCode=http_error` and an HTTP-status-based `failureReason`.

Dispatch is asynchronous: the original Router receive call has already returned
`accepted` before the worker calls Multica. Therefore the detailed Multica body
is retained on the Router dispatch task and in its terminal log, but is not
synchronously returned to the original event producer.

Pre-release and production Router deployments can therefore share binding data
without persisting a pre-release callback origin into a record later consumed
by production. The Router HTTP API base URL and the Multica callback origin are
separate configuration values with opposite communication directions.

The successful message callback carries the canonical account identity in
snake_case. `source_id` remains the channel-domain locator used by existing
subscription detail and surface APIs:

```json
{
  "message_binding": {
    "status": "success",
    "source_id": "source-channel",
    "platform": "dingtalk",
    "tenant_id": "corp-a",
    "account_id": "employee-uid",
    "previous_agent_id": "agent-a",
    "subscriptions": [
      {"domain": "channel", "source_id": "source-channel", "status": "active"},
      {"domain": "calendar", "source_id": "source-calendar", "status": "active"},
      {"domain": "approval", "source_id": "source-approval", "status": "active"}
    ]
  }
}
```

For every successful message callback, `subscriptions` is required and is the
authoritative set of active business domains. Multica validates the collection
without maintaining a local domain allowlist, persists its ordered domain set
as `channel_installation.config.enabled_domains`, and sends that exact set as
`expectedDomains` during reconciliation and user-unbind prechecks. Adding a new
Router domain therefore does not require another Multica code change. The
top-level `source_id` must still match the `channel` subscription because the
existing detail and surface APIs use that locator.

Rows written before `enabled_domains` existed remain readable. For such an
active row only, Multica derives `channel` plus optional `calendar` from the
legacy `calendar_start_enabled` field. New callbacks never use this fallback;
an empty, duplicate, inactive, malformed, or channel-mismatched subscription
collection is rejected as an invalid callback result.

`previous_agent_id` is omitted when no takeover occurred. On takeover, Multica
matches the previous Agent plus `platform`, `tenant_id`, and `account_id`; the
new installation is excluded and a row absent from this environment is a no-op.
This cleanup never calls Router, so a callback handled against an isolated
pre-release Multica database cannot change the authoritative Router owner or a
production-only Multica projection.

Agent archive and hard runtime/profile cleanup are synchronous and fail closed.
The local transaction locks the Agent and its single account projection, safely
enriches a legacy row when necessary, and calls conditional account-level
unbind before removing the exact local projection and Agent state. Only
`unbound` and `ownership_changed` may continue. `inconsistent`, malformed
responses, authentication/transport failures, and Router 5xx preserve the
local transaction for retry. Binding begin takes a conflicting lock on the same
Agent row, so a new binding cannot cross deletion. If Router succeeds but the
local transaction later fails, the Agent and projection remain locally while
Router is already unbound; this deliberately accepted reverse window is
resolved by an idempotent user retry and does not introduce an asynchronous
compensation table. There is no Agent-wide or source-only fallback for an
unresolved account key.

## Rollout order

1. Deploy Router's additive check, source-identity, conditional-unbind, and
   compare-and-swap takeover contract while retaining unrelated old callers.
2. Deploy the binding page so every new successful callback carries the
   canonical account key and conflict confirmation uses the expected owner.
3. Deploy Multica's strict account-key callback, reconciliation, conditional
   unbind, and synchronous fail-closed Agent deletion. Do not keep a binding-ID or legacy
   callback identity track in parallel.
4. Run account-key enrichment in dry-run, review unresolved rows, apply it to
   the intended Multica database, and repeat until every actionable active row
   is complete or explicitly classified.
5. Enable batch reconciliation, conditional user unbind, and synchronous
   account-key Agent removal. Remove the Agent-wide user-unbind path only
   after this gate passes.

## History

- 2026-07-31: Bound message/identity mutations to the intersection of Agent
  management and invocation permission, while preserving member-visible
  binding status. Documented how Router retains Multica non-2xx response bodies
  for asynchronous dispatch diagnosis.
- 2026-07-29: Added `auto` as a processing-surface binding value. Multica now
  preserves it in the local binding snapshot, exposes it through the binding
  API, and lets operators display or select it without rewriting it to `chat`.
- 2026-07-27: Extended the existing binding-token request with the
  database-authoritative Agent name and Workspace `{id,name}` snapshot. Added
  `agentName`, `workspaceId`, and `workspaceName` to the QR fragment, with one
  shared normalization/validation step and `url.Values` encoding.
- 2026-07-23: Preserved validated DBase task errors on failed message bindings
  and exposed `message_route.error = {code,message,retryable}` so Multica can
  display the actual failure instead of only `status=failed`.
- 2026-07-23: Stopped treating the legacy account-binding `dispatch_url`
  snapshot as authoritative. Subscription verification now derives the
  expected target from `dispatch_endpoint_id`, accepting the canonical path or
  the full URL built from the current Multica origin. Compatibility writes
  remain during the rolling rollout; physical storage cleanup is a separate
  post-rollout change.
- 2026-07-23: Added message-account nickname/avatar snapshots, exposed the
  active `issue`/`chat` surface, and added an owner-checked surface update that
  reuses the existing Router subscription.
- 2026-07-22: Multica now persists canonical dispatch paths for new Agent
  endpoint mappings and ignores the origin in historical full-URL rows. Legacy
  callers that still require a full URL receive one rebuilt from the current
  Multica runtime origin.
- 2026-07-22: Removed the incorrect `dispatchUrl` field from the Router token
  response contract and changed the QR binding payload to carry
  `dispatchPath`. Router now resolves the full callback URL only when it
  dispatches a task.
- 2026-07-21: Changed binding-token requests from a full `dispatchUrl` to
  `dispatchPath` so callers no longer provide an environment origin.

## Reason

The previous implementation confused the fixed Multica-to-Router HTTP request
address with the environment-specific Router-to-Multica callback address. Since
pre-release and production Router deployments share a database, persisting a
full callback URL can route production traffic to an isolated pre-release host.
Persisting only the path and resolving the current environment origin at
dispatch time prevents that cross-environment leak. Treating historical
Multica endpoint origins as authoritative also blocked QR generation before the
Router token request, so endpoint mappings now use the endpoint ID and canonical
path as their environment-neutral identity. The account-binding list, callback,
and surface-update flows therefore validate the Router target against that
endpoint identity instead of comparing it with a redundant database URL
snapshot.

The binding page also needs to show which DingTalk account owns the message
listener and which processing surface is active. Carrying a display-only
account snapshot fixes that presentation without confusing the listener with
the Agent execution identity. Updating only the Router binding's surface lets
operators switch among `issue`, `chat`, and `auto` without deleting and
recreating the subscription, while the Agent ownership and outbound-policy
checks prevent the update from widening into a different binding change.
Keeping `auto` intact in Multica makes the selected policy observable even
though Router deliberately uses the existing `chat` materialization path.

The downstream binding page also needs to identify which Multica Agent and
Workspace the QR code represents. Reading those names from Multica's database
prevents a client from spoofing display metadata, while a single validated
snapshot prevents the Router request and QR fragment from drifting. The
256-code-point and control-character rules keep the cross-repository protocol
bounded without truncating authoritative names, and treating the values as
display-only preserves the separation between login identity, Agent execution
identity, and message sender.

Failed callbacks previously validated a structured task error and then
discarded it, leaving only `message_route_status=failed` in the stored config.
That made distinct causes such as an existing source binding or an incompatible
client environment indistinguishable in Multica. Persisting the already
validated error preserves the failure boundary without exposing callback
credentials or raw upstream exceptions.

Binding mutators previously required only workspace membership, while task
dispatch later enforced the Agent invocation policy against the member captured
by the dispatch endpoint. That mismatch allowed an operator to create a binding
that could never execute. Requiring both management and invocation permission
aligns configuration authority with runtime authority without granting workspace
admins an invocation bypass. Keeping the list endpoint readable preserves the
view-only experience.

Dispatch failures occur after Router has asynchronously accepted the inbound
event, so the original producer cannot receive Multica's later HTTP body in the
same response. Recording the body on the dispatch task and emitting a bounded
terminal summary keeps the platform detail available for diagnosis while the
stable failure code and reason remain safe aggregation fields.

## 2026-08-02 Account-key Binding Change History

- Defined the digital-employee account key as `platform + tenantId + accountId`
  and the current binding key as that account key plus `agentId`.
- Added account-level batch reconciliation, conditional unbind, and
  compare-and-swap takeover semantics across every selected business domain.
- Kept `sourceId` as a per-domain Router locator and removed the proposed
  cross-service binding ID, relation lifecycle, and binding-ID backfill.
- Added canonical account identity to the successful callback, local
  projection, historical enrichment, and synchronous Agent-removal check.

## 2026-08-02 Account-key Binding Change Reason

The product currently permits at most one digital employee per Agent and treats
all selected business-domain listeners as one account-level operation.
The authoritative fact is therefore which Agent currently owns the trusted
DingTalk account key; a separate historical relation entity is unnecessary.
Including the expected Agent in every reconciliation, unbind, and takeover
mutation makes stale cross-environment requests conditional, while grouping by
the account key prevents a domain `sourceId` from being mistaken for the whole
binding. Agent-wide deletion remains too broad for user-initiated unbind because
the same Agent may bind a different account later, so every local cleanup uses
the complete account binding key.

## 2026-08-02 Synchronous Agent-removal Change History

- Removed the proposed asynchronous unbind table, migration, worker, retry
  configuration, and runtime registration from Multica.
- Agent archive and hard cleanup now lock the Agent and local projection, run
  exact account-key enrichment when needed, conditionally unbind the expected
  Agent in Router, and commit local cleanup only for safe Router outcomes.
- Binding begin now participates in the same Agent-row lock fence, preventing a
  new pending binding from crossing a concurrent delete.

## 2026-08-02 Synchronous Agent-removal Change Reason

The product explicitly chose not to add a new database table for deletion
compensation. A synchronous fail-closed state machine keeps unsafe or
unverifiable ownership intact and makes the small Router-success/local-rollback
window visible as a retryable failure. Exact BindingKey conditions and shared
row locks preserve a newer winner without introducing a second lifecycle or
schema track.

## 2026-08-03 User-unbind Reconciliation Change History

- Added a fresh Router ownership check inside the Multica DELETE service path;
  previously only the list path reconciled ownership.
- A local active projection reported as `bound_to_other_agent` or `unbound` is
  now revoked locally by exact AccountKey compare-and-swap without calling
  Router unbind.
- Router-unavailable, malformed, mismatched, or `inconsistent` check results
  now preserve the local projection for retry. A `valid` check still uses the
  existing conditional Router unbind before local cleanup.

## 2026-08-03 User-unbind Reconciliation Change Reason

The UI can display a fresh reconciliation result while the local projection
still names an older Agent. Calling Router unbind directly from that stale
projection creates an avoidable ownership conflict and must never risk the
current winner. Rechecking from persisted AccountKey data makes the server the
decision authority; skipping Router mutation for a confirmed stale projection
removes only Multica's obsolete state, while the existing conditional unbind
continues to protect ownership changes that occur after a `valid` check.

## 2026-08-03 Generic Subscription-domain Persistence Change History

- Added `channel_installation.config.enabled_domains` as the durable ordered
  snapshot of every active domain returned by the successful binding callback.
- Removed Multica's `channel`/`calendar` allowlist from callback and Router-check
  validation; domain collections now use generic bounded identifier validation.
- Reconciliation and user-unbind prechecks now send the persisted domain set
  instead of reconstructing it from `calendar_start_enabled`.
- Retained read compatibility for active rows created before `enabled_domains`
  by deriving the previous `channel` plus optional `calendar` representation.

## 2026-08-03 Generic Subscription-domain Persistence Change Reason

Reducing a multi-domain callback to one calendar boolean loses approval and any
future Router domain, causing a valid binding to be reported as `inconsistent`
and blocking user unbind. Persisting the Router callback's complete domain set
makes that set authoritative throughout Multica and removes the need to update
Multica whenever Router adds another supported business domain.
