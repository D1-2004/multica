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

### Inspecting and changing the processing surface

For an active message binding, Multica reads the Router subscription with the
same service credential:

```http
GET /api/subscriptions/<sourceId>
Authorization: Bearer <service credential>
```

The successful response includes the current processing surface and outbound
policy. `surface.type` is either `issue` or `chat`; the DBase-created message
binding keeps `outbound` fixed to `dws` / `latest_message`.

Multica changes only the processing surface through the owner-checked Router
endpoint:

```http
PATCH /api/subscriptions/<sourceId>/surface
Authorization: Bearer <service credential>
Content-Type: application/json

{
  "agentId": "<agent UUID>",
  "surface": { "type": "chat" }
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

## Router to Multica

`dispatchPath` identifies the Multica webhook but deliberately contains no
environment origin. Router persists the canonical path in its shared database.
When Router actually dispatches a task, the running Router environment combines
that path with its own configured Multica callback origin:

```text
MESSAGE_ROUTER_MULTICA_DISPATCH_ORIGIN + dispatchPath
```

Pre-release and production Router deployments can therefore share binding data
without persisting a pre-release callback origin into a record later consumed
by production. The Router HTTP API base URL and the Multica callback origin are
separate configuration values with opposite communication directions.

## History

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
operators switch between `issue` and `chat` without deleting and recreating the
subscription, while the Agent ownership and outbound-policy checks prevent the
update from widening into a different binding change.

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
