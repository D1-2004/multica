# Event admission and scene routing (PRI-78)

## Scope and seams

`Provider -> Event -> Router -> SceneRef -> scene entry` runs before business
handling. The first adapters are authenticated MessageRouter Dispatch 2.0
(messages, scene observations, calendar and approval facts; statistics-only
telemetry retains its existing admission) and owned DWS native IM
(`im_at`, single-chat messages). The current scene entry continues into the
existing dispatch/Coordinator; this change adds no EmployeeLoop, Task executor,
broker, scheduler, permission binding or scene identity. See `agent-scene.md`.

`internal/eventrouter` owns the protocol and durable routing, with no dependency
on handlers or model services. Adapters in `handler/event_admission.go` supply
Host facts and a typed scene locator. They do not turn every fact into a user
request. MessageRouter is one adapter, not the Router defined here.

## Event v1

An Event carries `version=1`, source-scoped `id`, `source`, original `type`,
`category` (user_message / observation / control / run_callback / wake),
`occurred_at`, and a native business `payload` with its `payload_schema`.
Source is the verified transport/endpoint namespace; credential rotations and
replica ids are excluded. Source + id is unique within workspace and agent.
Native and MessageRouter ownership guards remain authoritative: this is not
an invented cross-provider alias for their different delivery ids.

Host supplies workspace, receiving agent, authenticated principal (endpoint
operator, not the message author), tenant org, locator/observation, route choice,
configuration SHA/generation, and request fingerprint. Caller JSON cannot set
these. The actor remains a fact inside payload; a receipt or SceneRef grants
no connector rights. Tenant is `agentTenantOrg` from the recorded receiver org
or current agent identity, checked against currently served orgs. Conversation
kind is stated only by the authenticated source; body/title/sender never infer
it. Resource events with no conversation use the enterprise scene. Unknown
conversation kinds and missing locators remain unmapped.

Only business event JSON is stored, not external identity tokens, callback
secrets, dispatch prompts, credentials or deployment configuration. Native
payload is retained before the Dispatch 2.0 compatibility projection. MessageRouter
payload retains its entire event JSON, including source fields unknown to the
typed Dispatch 2.0 projection. Payload is bounded to 1 MiB. Unknown major Event
versions and categories are rejected; additive JSON fields remain compatible.

## Admission, retries and acknowledgement

One `scene_event_receipt` row is both envelope and routing receipt. The existing
`agent_event` / `agent_event_stream` tables are the older proactive collection
state; their batching, triggers and retention are unchanged and are not scene
identity or routing receipts. A transaction locks
the source/id key, looks up an existing receipt, or resolves the scene with
`scene.Resolve` and inserts the row. Scene and receipt commit atomically.
Concurrent redelivery returns the same row. A changed fingerprint at the same
key is a conflict. Configuration changes, kind corrections, renames and tenant
rebinds never remap an existing receipt. Every use of a resolved ref checks
current tenant/owner; stale receipts cannot enter another org's scene.

Receipt states: `ready` (resolved scene entry), `unmapped` (no scene entry),
`legacy` (old admission selected). `ready` proves entry routing, not handling
or Task success. There is no ordering claim across events. Scene activity only
moves forward; event timestamps are facts, not a run checkpoint.

Persistence/scene storage errors are retryable (HTTP 503 / native NACK).
Conflicting replay is HTTP 409. Unmapped unified admission is durably ACKed
with receipt id/reason and starts no handler or Task. Ready events keep the
old acceptance/job transaction and ACK rules: a receipt alone does not ACK
business admission when that transaction fails. A unified event without a
completion callback stops at the durable scene entry (202 with receipt/scene
ids), rather than running the old non-idempotent callback-less executor. This
is the event-only integration seam; attaching a business consumer requires its
own idempotent acceptance. Explicit cancel controls retain their existing
terminal/idempotent control semantics. Replay goes through the same
old idempotency guard, never executes a second path. No new automatic replay
or "send again" operation is introduced. Explicit mapping and release of held
events is a later administrative operation; changing the canary does not
release them.

## Configured canary and rollback

`runtime.event_scene_router` is absent/off by default. Targets are exact
`{workspace_id, agent_id, tenant_org_id}` triples; an empty target list admits
nobody. Each request reads one configuration snapshot. Both selected and
unselected traffic persist a receipt, selecting `unified` or `legacy` once.
Retries retain that choice even after a config change. The existing deployment
fence replica registry must show `[event-router:1]` on every live replica before
new unified acceptance; mixed binaries fail with retryable 503. Persisted commands
carry the internal receipt id so a worker cannot reinterpret a frozen missing
scene as an old command that still needs resolution. The paths are exclusive;
neither shadow execution nor new/old dual handling is allowed.

Turning enabled off stops new unified admission; ready events already handed
to existing jobs finish there, retries retain receipt/ref, and unmapped events
remain held. Deploy the schema/new binary before adding the config key. Remove
the key before rolling back to a binary that does not parse it. A binary older
than this receipt contract does not understand held events: do not replay held
source deliveries into it. Stop the selected source, finish accepted jobs and
retain the receipts before such a binary rollback. Never delete receipts to
retry a submitted business effect. The schema is additive and not dropped in
an operational rollback.

## Retention and extensions

Events and receipts share workspace ownership/deletion. No new forever retry
loop is added. Retention is the workspace's retained inbound evidence boundary;
payloads are not human chat history or execution state. An age-based purge
must preserve dedupe tombstones over the provider redelivery window; this is
deferred rather than deleting live keys on a guessed TTL.

A new Provider authenticates first, supplies Host metadata and a versioned
payload schema, and constructs a locator using the current scene contract.
A2A context/client namespaces and caller-stated DingTalk extensions do not
prove a locator. Webhook signatures and timer triggers must have explicit
trusted scene mappings. Run callbacks and internal wakes refer to an existing
Task/Run and its persisted SceneRef; they never grant new human authority.
These providers, mapping administration, ordered scene queues and business
handling changes are not part of this delivery.

## Verification gates

Protocol tests cover native payload preservation, actor/principal separation,
unknown kinds, version rejection and exact canary targets. PostgreSQL tests
cover concurrent receipt identity, atomic resolve/receipt, changed-payload
conflicts, sticky routing/config, missing kind/org, cross-owner/tenant fences,
and replay through a new Router instance. Handler tests cover both existing
adapters and no execution for unmapped admission. Local tests prove only these
boundaries; pre-release evidence must identify build SHA, configuration version,
provider receipt and persisted scene, and observe the deployed admission path.
Task/model results are not an acceptance gate for this first layer.
