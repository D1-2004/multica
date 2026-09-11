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

### Message scope contract versions

The completion callback may report the message-listening scope in two contract
versions under `message_binding`:

```json
{
  "message_scope": "custom",
  "message_scope_version": 2,
  "message_scope_detail": {
    "direct_cids": ["101:202"],
    "group_cids": ["grp-1"]
  }
}
```

A missing `message_scope_version` means v1: `message_scope` + `conversations`
are the only scope payload and `message_scope_detail` must be absent. Version 2
carries the dual-dimension detail: each bucket accepts `["*"]` (all), a cid
list (specified), or `[]` (muted); both buckets must be present, may not both
be empty, `"*"` must be the only entry of its bucket, and cids are non-empty,
whitespace-free, and at most 512 bytes. A successful v2 callback must include
the detail; a failed v2 callback may omit it and is then persisted as v1. The
legacy `message_scope` is still reported as a degraded fallback for old
renderers, so v2 relaxes the "custom requires conversations" rule (a
wildcard-or-muted combination selects no conversations).

The binding-list API mirrors the stored scope per record under `message_route`:

```json
{
  "message_scope": "custom",
  "message_scope_version": 2,
  "subscription": {"direct_cids": ["101:202"], "group_cids": ["grp-1"]},
  "legacy_view": {"message_scope": "custom", "cids": ["101:202", "grp-1"]}
}
```

`subscription` is the dual-dimension view: the stored detail for v2 records, or
an upgraded projection for v1 records (`all` → double `["*"]`; `direct_only` →
both buckets empty, because the underlying `uid:uid` self-chat rule is not
deliverable by the event center; `custom` conversations split by the cid
colon). `legacy_view` keeps old renderers working: v1 records restate their
stored scope, while v2 records degrade (`["*"]`+`["*"]` → `all`/`["*"]`;
all-direct plus muted-group → `direct_only`/`["uid:uid"]` synthesized from the
bound account id; specified-only → `custom`/merged cids; any other wildcard
combination → `custom`/`null`). Existing rows default to version 1 without
migration.

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
rendered by the UI. Reconciliation remains a display-only read: it can show
`active`, `unbound`, `bound_to_other_agent`, `inconsistent`, or
`router_unavailable`, but it is not a precondition for user unbind.

On DELETE, Multica reloads the local projection and builds the exact binding key
from persisted `agentId + platform + tenantId + accountId`. Historical active
projections that have `router_source_id` but lack the account key first call
the source-identity lookup and write the recovered key through a full-snapshot
CAS. This enrichment accepts disabled historical sources and does not require a
current active subscription, current Agent owner, dispatch target match, or full
expected-domain match before Router unbind. The final ownership decision belongs
to Router for the submitted `agentId + platform + tenantId + accountId`.

The conditional unbind request is always sent for an active projection with a
complete or recovered key:

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
all Gateway-backed domains only when all of them still belong to the requested
Agent, then returns to Multica after that upstream teardown or ownership check
has completed. It returns `unbound` when the relationship was removed or was
already absent, `ownership_changed` when the account now belongs to another
Agent, and `inconsistent` for mixed or partial ownership. The latter two
outcomes never remove another Agent's Router binding.

Multica cleans its current-environment local projection only after Router
returns `unbound` or `ownership_changed`. The cleanup is still guarded by an
account-key compare-and-swap, so a concurrent replacement local projection is
not cleared. Router timeout, transport failure, authentication failure, Router
5xx, malformed response, or `inconsistent` all preserve the local projection and
return a retryable or conflict error to the user.

The existing Agent-wide digital-employee delete is not used for a user's local
unbind. It would be too broad when a stale environment still shows one account
after that Agent has subsequently bound another account.

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
does not require the source to remain owned by the local Agent, does not require
an active subscription, and may resolve a disabled historical source, because
Router is the authority for the subsequent conditional unbind. It does require
the source to be a DingTalk digital-employee channel source. Rows with a missing source,
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
`expectedDomains` during display reconciliation. User unbind does not send
`expectedDomains`; Router evaluates the submitted account binding key. Adding a
new Router domain therefore does not require another Multica code change. The
top-level `source_id` must still match the `channel` subscription because the
existing detail and surface APIs use that locator.

Every active binding config must include `enabled_domains` with `channel`.
Multica does not reconstruct domains from `calendar_start_enabled`; an empty,
duplicate, inactive, malformed, or channel-mismatched subscription collection
is rejected as an invalid callback result. `message_scope` and `conversations`
remain the channel-domain filter and are not members of `enabled_domains`.

`previous_agent_id` is omitted when no takeover occurred. On takeover, Multica
matches the previous Agent plus `platform`, `tenant_id`, and `account_id`; the
new installation is excluded and a row absent from this environment is a no-op.
This cleanup never calls Router, so a callback handled against an isolated
pre-release Multica database cannot change the authoritative Router owner or a
production-only Multica projection.

Agent archive and hard runtime/profile cleanup are synchronous and fail closed.
The local transaction locks the Agent and its single account projection, safely
enriches a legacy row from source identity when necessary, calls conditional
account-level unbind, and only then removes the exact local projection and
continues Agent deletion. Multica therefore cleans from upstream to downstream:
Router/Gateway ownership first, then Multica's projection and Agent state. Only
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
5. Enable batch reconciliation, Router-first conditional user unbind, and synchronous
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
- The superseded design revoked a local active projection reported as
  `bound_to_other_agent` or `unbound` by exact AccountKey compare-and-swap
  without calling Router unbind.
- Router-unavailable, malformed, mismatched, or `inconsistent` check results
  preserved the local projection for retry. A `valid` check used conditional
  Router unbind before local cleanup. This DELETE precheck branch was
  superseded by the 2026-08-04 Router-first unbind contract below.

## 2026-08-03 User-unbind Reconciliation Change Reason

The UI can display a fresh reconciliation result while the local projection
still names an older Agent. Calling Router unbind directly from that stale
projection creates an avoidable ownership conflict and must never risk the
current winner. Rechecking from persisted AccountKey data makes the server the
decision authority; skipping Router mutation for a confirmed stale projection
removes only Multica's obsolete state, while the existing conditional unbind
continues to protect ownership changes. This reason explains the superseded
2026-08-03 precheck design; the current write contract is Router-first.

## 2026-08-03 Generic Subscription-domain Persistence Change History

- Added `channel_installation.config.enabled_domains` as the durable ordered
  snapshot of every active domain returned by the successful binding callback.
- Removed Multica's `channel`/`calendar` allowlist from callback and Router-check
  validation; domain collections now use generic bounded identifier validation.
- Reconciliation now sends the persisted domain set instead of reconstructing it
  from `calendar_start_enabled`.
- Retained read compatibility for active rows created before `enabled_domains`
  by deriving the previous `channel` plus optional `calendar` representation.

## 2026-08-03 Generic Subscription-domain Persistence Change Reason

Reducing a multi-domain callback to one calendar boolean loses approval and any
future Router domain, causing a valid binding to be reported as `inconsistent`
and blocking user unbind. Persisting the Router callback's complete domain set
makes that set authoritative throughout Multica and removes the need to update
Multica whenever Router adds another supported business domain.

## 2026-08-04 Router-first Unbind Change History

- Removed the fresh batch-check gate from user-initiated Multica DELETE; the
  list reconciliation result remains display-only and cannot block a retry.
- User unbind and Agent removal now call Router conditional unbind first for any
  active projection with a complete or recovered account key.
- Multica clears the current local projection only for Router `unbound` or
  `ownership_changed`; Router failures and `inconsistent` preserve local state.
- Historical rows missing account key are enriched from source identity,
  including disabled historical sources, without requiring active subscription,
  current Agent ownership, dispatch target match, or expected-domain parity.

## 2026-08-04 Router-first Unbind Change Reason

Router is the authority for account ownership and Gateway listener teardown.
Multica's local row is only a projection, so it must not use stale display
reconciliation or incomplete subscription-domain snapshots as a write gate.
Calling Router first lets the upstream chain decide whether the submitted
`agentId + platform + tenantId + accountId` is safe to unbind; only after
Router/Gateway success does Multica remove its current-environment projection.
This keeps retries lossless when Router is unavailable and prevents
`ownership_changed` from disturbing another Agent's new binding.

## 2026-08-04 Public Subscription-domain Projection Change History

- Added `message_route.enabled_domains` to the Multica account-binding list
  response and its frontend schema.
- The Agent integrations page now displays the approval listener when the
  returned domain set includes `approval`.
- Active stored bindings no longer fall back from `calendar_start_enabled` to
  synthesize `enabled_domains`; the corrected persisted domain set is required.

## 2026-08-04 Public Subscription-domain Projection Change Reason

The integrations page previously received only the derived
`calendar_start_enabled` value, so it could not display an active approval
subscription even though Multica had persisted and reconciled it. Exposing the
authoritative domain set preserves the distinction between business event
domains and the channel-domain conversation filter.

## Native MCP Direct Binding Contract

The server-hosted Streamable HTTP endpoint at `POST /api/mcp` exposes three
digital-employee actions:

- `get_digital_employee_binding`
- `bind_digital_employee_to_multica_agent`
- `unbind_digital_employee`

The MCP client never supplies `workspace_id`, a Router binding token, or a
dispatch target. With a `mul_` Personal Access Token, the caller must supply
`agent_id`; Multica uses the authenticated PAT user as initiator and verifies
the Agent's persisted Workspace membership without accepting a Workspace
header. With a `mat_` Task Token, `agent_id` is omitted and Multica fixes the
target to the persisted Agent of the server-authenticated active task; an
explicitly different Agent is rejected. Both modes retain the same Agent
manage-plus-invoke permission rule as the browser flow. The PAT user or Task's
persisted human originator is the authorization principal, never a
caller-supplied user identifier.

The bind tool accepts the `tenant_id` and `digital_employee_id` returned by the
DWS digital-employee creation flow, plus optional processing surface, message
scope, conversation filters, and enabled business domains. Defaults are
`surface_type=auto`, `message_scope=direct_only`, and
`enabled_domains=[channel]`; every submitted domain set must contain `channel`.
Multica creates the local pending projection before any Router mutation, then
issues and consumes the one-time Router credential entirely in server memory.
The credential is never returned, persisted, or logged.

Multica binds the account through the current Router subscription endpoint:

```http
POST /api/subscriptions
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "source": {
    "platform": "dingtalk",
    "domain": "channel",
    "tenantId": "<organization ID>",
    "accountId": "<digital employee UID>",
    "subscriptionConfig": {"upstreamMode": "HTTP_CALLBACK"}
  },
  "agent": {
    "agentId": "<authorized Agent UUID>",
    "dispatchUrl": "<current Multica origin>/api/webhooks/agent-dispatch/<endpointId>"
  },
  "surface": {"type": "auto"},
  "outbound": {"mode": "dws", "replyTo": "latest_message"},
  "bindingToken": "<one-time token>",
  "enabledDomains": ["channel"],
  "replaceExistingBinding": false
}
```

The token descriptor continues to carry the environment-neutral
`dispatchPath`; the subscription request carries the current environment's
absolute `dispatchUrl`. Multica validates the returned Agent, source, dispatch
target, status, surface, and outbound policy before activating its projection.
If a previously unbound account becomes owned by the Agent but Router returns
an error, an invalid response, or local activation fails, Multica performs an
exact conditional account-level unbind as compensation. A compensation failure
keeps the pending projection for retry. Before compensating, Multica rechecks
the pending attempt credential hash and exact account key; a superseded attempt
must not unbind ownership established by a newer concurrent attempt.

A pending direct projection already contains the complete account key. The
unbind tool therefore calls Router's conditional account-level unbind before
clearing either an active projection or such a pending projection. This covers
the recoverable window where Router accepted the direct bind but Multica could
not activate its local row. Pending browser attempts without a complete
account key remain local-only and can still be revoked without a Router call.

## 2026-08-07 Native MCP Direct Binding Change History

- Removed the PAT client's Workspace header; the selected Agent row now owns
  Workspace resolution before member and manage-plus-invoke authorization.
- Added `mul_` PAT support. PAT callers select `agent_id` within the
  authenticated Workspace; Task Token callers remain pinned to the task Agent.
- Added task-scoped query, bind, and unbind tools to the existing `/api/mcp`
  endpoint without adding caller-controlled workspace identifiers.
- Added direct Router subscription creation with server-only one-time binding
  credentials and persisted the requested generic enabled-domain set.
- Separated core digital-employee binding availability from optional DBase
  page configuration; the browser flow still requires a valid DBase origin.
- Added exact Router compensation for partial direct binds and Router-first
  cleanup for pending direct projections with a complete account key.

## 2026-08-07 Native MCP Direct Binding Change Reason

Local Qoder and Claude Code clients already use Multica Personal Access Tokens
as durable user credentials. Allowing that existing credential on MCP avoids
manufacturing a long-lived Task Token, while explicit Agent selection plus the
existing manage-plus-invoke check preserves the browser flow's authorization
boundary. Running Agents keep the narrower Task Token behavior, so they cannot
switch the binding target by supplying another `agent_id`.

Agents can create a DingTalk digital employee through DWS while running a
Multica task, so requiring a browser QR handoff would reintroduce client-image
release coupling and prevent the Agent from completing the workflow itself.
Keeping identity derivation, one-time credentials, dispatch targeting, and
compensation inside the Multica service makes the MCP call safe across old and
new runtime images. Extending Router-first cleanup to complete pending direct
projections prevents a failed local activation from leaving an upstream
subscription that the user cannot subsequently remove.

## Reuse an existing execution identity

The digital employee configuration's execution-identity card can use an identity
already bound in the same workspace without another QR-code flow. Message
subscriptions and enterprise employee/BUC authorizations are independent.

- `GET /api/workspaces/{id}/dingtalk/execution-identities?agent_id={target}`
  lists `identities`, deduplicated by DingTalk UID and organization. Each item
  exposes only `source_agent_id`, `source_agent_name`, `account_display_name`,
  and `organization_name`.
- `POST /api/workspaces/{id}/dingtalk/execution-identities/reuse` accepts
  `agent_id` and `source_agent_id`; successful completion returns HTTP 204.
  The caller cannot supply account coordinates or token material.

Both endpoints require a human actor who owns the active target Agent. Sources
must be active Agents in the same workspace, owned by that caller, with an
identity personally bound by that caller. Workspace administration alone does
not grant reuse of another owner's identity. Transferred Agent ownership does
not transfer the original binder's right to reuse their identity.

The write locks the relevant Agent ownership rows and source identity, checks
all source/target restrictions again in SQL, and records
`agent_dingtalk_identity_reused` in `activity_log` in the same atomic statement.
Successful reuse also deletes pending QR-binding attempts for the target so an old QR callback cannot replace the selected identity. The audit contains source and target Agent IDs, not account coordinates. A
source that was revoked or became ineligible returns 409; an existing different
target identity also returns 409. Repeating the same bind succeeds without
changing the original binding timestamp. Each resulting binding is independent;
unbinding a source later does not revoke previously authorized copies.

No schema migration or Router/DWS credential-copy operation is required.
