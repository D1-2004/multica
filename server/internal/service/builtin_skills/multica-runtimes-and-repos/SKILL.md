---
name: multica-runtimes-and-repos
description: "Use when a Multica runtime or daemon misbehaves: agent not running, task not claimed, runtime offline, workdir or session reuse, repository checkout."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Runtimes and Repos

## Quick start

For "agent did not run" or "repo checkout failed", read the chain before changing anything:

```bash
multica agent get <agent-id> --output json
multica runtime list --output json
multica repo checkout <repo-url>
```

Runtime and repo commands affect active agent execution. Do not restart daemons, update runtimes, or check out arbitrary repos just to test.
Daemon start, restart, and stop are unavailable from inside a daemon-managed agent task; lifecycle control remains an operator action.

## Core model

FC DSH employees require an independently provisioned AgenticFS Home before
their first task. Human owners and workspace administrators can inspect or
advance provisioning through `GET/POST /api/agents/{id}/dsh-home`; this is an
operator action, unavailable to task tokens. Placement and the cloud access
package are deployment-owned. A pending create must reconcile its persisted
intent; never work around it by sharing another employee's volume. A provisioned
Home does not by itself prove that a native Host or business task is running.

DSH Session identity is preserved exactly: official browser UUIDs and platform
`session-UUID` identities are distinct valid names. The internal native adoption
primitive requires the same PostgreSQL transaction as task/input creation and
rejects scope, task or request conflicts. This storage primitive does not yet
connect native browser prompts to task admission. Do not claim native chat is
available from the entry page or this schema support alone.

A runtime is the execution target behind an agent. A daemon owns local runtime processes and claims queued tasks from the server.

The chain is:

1. user action creates or updates an `agent_task_queue` row;
2. the task points at an agent and runtime;
3. server wakes the runtime over daemon websocket when possible;
4. daemon polls/claims the task;
5. server returns task context, repos, project resources, prior session/workdir hints, and task token;
6. daemon prepares a workdir and launches the provider CLI;
7. `multica repo checkout` talks to the local daemon, not directly to GitHub.

## CLI

```bash
multica runtime list --output json
multica runtime usage <runtime-id> --output json
multica runtime activity <runtime-id> --output json
multica runtime update <runtime-id> --target-version <version> --output json
multica runtime delete <runtime-id>
multica repo checkout <url>
multica repo checkout <url> --ref <branch-or-sha>
```

`runtime update` and `runtime delete` are writes. Starting a runtime update is limited to its owner or a workspace owner/admin; the original initiator may keep polling that specific in-flight request if their admin role changes. `runtime delete` removes a runtime registration; if active agents are still bound, it refuses unless the user explicitly passes `--cascade`, which unbinds those agents and cancels their queued/running tasks before deleting the runtime. Unbinding keeps the agents and everything they own — instructions, skills, chats, labels, channel installations, autopilots and task history — and only clears `agent.runtime_id`; an unbound agent cannot run until it is bound to a runtime again (`multica agent update <id> --runtime-id <runtime-id>`), and every trigger path refuses it with `agent_runtime_required`. `repo checkout` creates a dedicated branch in the task working directory. Most runtimes use a linked worktree; Linux and Windows Codex use task-local Git metadata so a task can stage and commit without making the shared `.repos` cache writable.

`repo checkout` requires both `MULTICA_DAEMON_PORT` and the task-scoped `MULTICA_TOKEN`; it is intended to run inside an authorized daemon task. If either is absent, you are not in the normal agent checkout path. The daemon accepts the token only while that exact task is running and binds checkout to its workspace, prepared workdir, and visible repo list. When a project `github_repo` resource has `resource_ref.ref`, `repo checkout <url>` uses that ref by default for the current task; an explicit `repo checkout <url> --ref <branch-or-sha>` overrides it.

## FC stable provider channels

Operators with stable publication permission can select an independent FC
provider channel. Always specify `--backend aliyun_fc`; the CLI default is ASB.

```bash
multica runtime stable channel --backend aliyun_fc --provider dsh --output json
multica runtime stable runtimes --backend aliyun_fc --provider dsh --output json
multica runtime stable release list --backend aliyun_fc --provider dsh --output json
multica runtime stable release create --backend aliyun_fc --provider dsh --template-id <immutable-template-id> --idempotency-key <operation-id> --output json
```

`--provider` maps to the API's `provider_scope`. Without it, channel/create use
the shared channel and release history includes all scopes. An explicit empty
`provider_scope` history query selects only shared releases. Named scopes are FC
only. A provider inherits the shared pointer until its first scoped release
creates an independent pointer. That release must complete the normal validation,
drain, rollout and observation gates. Future shared releases exclude independent
providers; new stable Runtimes resolve their provider's pointer. The artifact's
`providers` still describes its real capabilities; `target_providers` describes
the release selection. Never relabel artifact capabilities to limit a rollout.

Only one release per backend may be active, including across scopes. Deploy
scope-aware servers to every replica before creating a scoped release. During a
mixed-version deployment, database guards reject cross-scope targets, publication
and stale shared-template creation; an old worker may stall a release and is not
an acceptable operator for it. Use release-ID actions to pause, resume, advance,
complete observation or roll back. Active-release rollback restores each target's
previous artifact. After completion, publish the previous immutable artifact as
a new release with the same provider scope. Never delete the independent channel
or its history to simulate rollback. Scoped operator controls are currently in
the CLI/API; the shared-channel UI does not select provider channels.

## Task CLI boundary

The daemon injects a task-scoped `mat_` credential for Multica API commands and a private task-local Multica configuration root. Inside that managed task context:

- API commands such as `issue list`, `issue get`, and `issue runs` use the injected task identity and never fall back to the daemon Owner's saved Multica profile.
- `config show` and `config set` operate only on task-local Multica state. A missing task config root fails closed.
- `auth status` may verify the task identity but omits all token material from its output.
- `daemon status` and `daemon disk-usage` report on the runtime hosting this task: `status` probes the daemon-injected health port, and `disk-usage` scans the daemon-injected workspaces root. Both refuse `--profile`, `disk-usage` also refuses `--all-profiles` and `--workspaces-root`, and its STATUS column stays blank because filling it would spend the Owner's credential.
- Human/local profile and daemon commands — including `login`, `logout`, `setup`, `workspace switch`, local runtime profile path mutation, `daemon start` / `stop` / `restart`, `daemon logs`, and `daemon probe-runtimes` — are unavailable. `daemon stop` in particular would terminate the daemon running this task and every sibling task on it.

The daemon still preserves the real `HOME` and XDG variables for provider tools such as `gh`, `aws`, `kubectl`, and npm. This is CLI resolution hardening, not hard filesystem confidentiality: a process under the same OS user can still open an explicitly known Owner path. Dedicated Unix users, containers, VMs, or an equivalent OS boundary are required for that stronger isolation.

## Debugging an agent that did not run

Check in this order:

1. Was a task supposed to be created? Inspect issue/comment/autopilot context.
2. Is the assignee an agent or squad? A squad routes to its leader.
3. Is the agent archived or bound to a runtime the actor cannot use?
4. Is the runtime online? `multica runtime list --output json`.
5. Did the daemon heartbeat recently? Runtime `last_seen_at` is the visible clue.
6. Did the task get claimed or is it stuck pending/running/waiting for local directory?
7. If repo checkout failed, classify it after checking whether repo context was
   present in the task/project context.

## Repos

The runtime brief lists repos available to this task. Treat that list as the authority for agent checkout unless the user explicitly asks to bind a new project resource.

Workspace repos and project resources are not the same thing:

- workspace repo metadata can appear in workspace context;
- `github_repo` project resources are durable project context and can affect future tasks; optional `resource_ref.ref` pins the default checkout ref for tasks in that project;
- `local_directory` resources point at a path owned by a daemon and carry local-machine assumptions.

Do not add a project resource just because `repo checkout` failed. First determine whether the user asked for durable project context or just a task checkout.

More source-backed details: `references/runtimes-and-repos-source-map.md`.

### ASB sandbox capacity

Cold launches read current ASB allocations and use the regional API endpoint
with the most free slots. New regional allocations and quota increases are
picked up on the next capacity check. A shared 30-second busy cooldown limits
quota checks while full; upstream rate limits may extend that delay. An explicit `QUOTA_EXCEEDED`
response tries other available regions, refreshes quotas once, then remains
queueable if capacity is still unavailable. Permission errors remain errors.
Regional routing applies to ASB service domains; custom gateways retain their
configured endpoint and routing.

The shared five-second create interval is pacing, not evidence that quota is
full. Cold launches wait only its remaining duration and continue in the same
startup attempt, without entering the capacity retry queue. Full-capacity and
upstream rate-limit waits remain shared across replicas. Finishing a recovery
launch immediately wakes the next eligible capacity waiter.

When an ASB tenant has no free instance slots, Multica can terminate an idle
task sandbox to make room for a new launch. Chat and issue sandboxes have the
same reclaim policy, with no minimum idle time or chat retention grace.
Active tasks and in-flight launches remain fenced by task state and scope locks.
ASB capacity retries use creation order regardless of chat, issue, autopilot,
or retry priority. Files stored only in an idle sandbox may be lost on reclaim.

### ASB network allowlist

ASB sandboxes deny outbound connections unless the destination is allowed.
Required platform services and configured Agent MCP/service hosts are included
automatically. Workspace owners/admins can add exact domains or individual IPs
in the Runtime details page. Custom wildcards, URLs and CIDR ranges are rejected.
Defaults include DWS signed file transfers, document OSS, mail and Stream.
Regional transfer subdomains under trans.dingtalk.com and down.dingtalk.com
are managed defaults. Enterprise-specific storage and third-party download
hosts still require an exact Runtime entry.
Changes apply at the next task launch: sandboxes using an older policy are
replaced, so files stored only in that sandbox do not carry over. Active tasks
finish with their existing policy. Ask the user to configure a missing
destination; do not try to bypass the sandbox network policy.

Human owners/admins can use the employee Configuration → Execution → DSH Home page to prepare storage and read its persisted status. The page distinguishes storage readiness from host running state and never creates resources on render. Interrupted or pending requests are reconciled through the same employee intent; credentials and placement remain deployment-owned.

Native prompt admission is exposed through `POST /api/dsh-native/prompts` with a session Bearer capability and exactly `workspace_id`, `agent_id`, `generation`, `sandbox_id`, `session_id`, `request_id`, `workdir` and `content`. Request IDs are canonical nonzero UUIDs; workdir must be `/mnt/multica-dsh/workspaces/<session_id>`. The server derives the human from the grant and registers or resolves that human's platform chat session from the native identity. Registration creates the session and mapping together, without a task or synthetic input; a later admission failure can leave this empty session for a retry. Clients cannot select a platform chat ID, human, model or credentials.

The native chat admission service then writes the human input, platform task and exact native Session/request binding in one PostgreSQL transaction. Registration and admission take the same Runtime and employee admission locks as Host startup, then require current invocation permission, FC DSH runtime and an unexpired grant for the current Host generation. Existing chat mappings must belong to the same human; issue mappings cannot be adopted as chats. Identical request retries reuse the original task/message without another launch; changed input or scope conflicts are rejected. The response contains `session_id`, `request_id`, `chat_session_id`, `task_id`, `message_id`, `queued` and `replayed` (201 for new admissions, 200 for replay). An unconfirmed admission returns 503 and must be retried with the same identity. Gateway v3 forwards browser prompts through the independent reverse input lane; real PostgreSQL/browser acceptance remains pending. This HTTP endpoint alone does not make native UI business prompts available.

Typed native input is preserved internally as `dsh_native_prompt` in the task-owned user message and daemon claim. It carries the original `requestId`, `sessionId`, `mode`, text/image/file content and optional `clientTimeZone`; transcript text is a display summary. Claims verify the persisted employee/Session/request binding and require `dsh-native-prompt-v1`; an old daemon cannot execute an attachment summary as a replacement. The managed FC launcher rejects missing or mismatched native launch identity, preserving both UUID and `session-UUID` names. Busy `steer` is explicitly rejected until active-task input admission exists; it is never silently converted to a queued turn. The public endpoint above remains text-only. Gateway v3 submits full native requests through a separate signed reverse input lane. These protocol changes are not a claim of browser or real database acceptance.

Human employee managers can request a short-lived native entry with `POST /api/agents/{id}/dsh-native/access` and an empty body. The server requires a provisioned Home, then starts or recovers its FC DSH Host under the same Runtime and employee locks used by platform tasks, without creating a task. It verifies the exact live gateway readiness receipt. It returns `access_id`, `entry_url` (a one-minute fragment credential) and `expires_at`; the fragment is consumed at the separate `/_multica/open` gateway entry page, including when an older session Cookie exists. Do not log, share or persist the URL. `DELETE /api/agents/{id}/dsh-native/access/{accessId}` revokes the grant. Opening an entry can start, renew or recover the Host; revoking access does not destroy it. Existing task admissions and live native grants block template replacement until they drain; an uncertain create or destruction never permits another writer. The employee DSH Home tab exposes Prepare DSH after storage is ready, then a short-lived Enter DSH link. The link is cleared on expiry or employee change and excluded from query/mutation data; errors do not trigger automatic startup retries. This entry does not prove native prompt/task admission is accepted.

The gateway authority is the deployment app origin (`web.app_url` in Diamond or `MULTICA_APP_URL` in environment mode), independent of the FC task relay URL. A missing authority fails closed. The backend now opens a signed reverse authorization transport to the v3 FC gateway before issuing an entry. It polls only the deployment-derived gateway origin, never follows redirects, and signs each decision with a deployment key derived in a separate HKDF domain; only the public key enters FC. Requests bind one exact Host and are never replayed after an uncertain exchange. Each browser request and periodic WebSocket check still consults current database authorization. This transport does not create or renew a Host. The capability callbacks remain available via `POST /api/dsh-native/access/exchange` and `/check`, using the entry/session Bearer credential and exact `workspace_id`, `agent_id`, `generation`, `sandbox_id` body. These callbacks independently check the current employee manager and the persisted running Host. Their credentials are not Multica API tokens or model/tool credentials.

Gateway v3 uses `/_multica/inputs` with its own control-only transport token and `multica-dsh-native-input-v1` signature domain. Input polls carry one request at a time, with a 4 MiB frame bound and at most 8 MiB of pending native input; authorization polls retain their smaller independent budget. Only the backend can sign the durable task admission. The gateway returns native `accepted:true` only after a correlated platform receipt, never after a timeout, and never falls back to direct Host prompting. A lost input response must be retried with the same native request ID. Opening an entry establishes both reverse lanes; neither lane holds a writer lease. Native Session creation defaults to a generated Session and `/mnt/multica-dsh/workspaces/<session_id>`; arbitrary workspaces/cwd are refused. Fork/adoption of existing different directories, active steering, queue projection and real browser parity remain pending. Gateway v2 candidates cannot satisfy v3 readiness and require the normal drained sandbox replacement.

Employee Host support is established by the fixed Home initialization and native supervisor receipts during launch. FC template catalog capabilities are derived from provider fingerprints and do not read arbitrary Docker labels. Do not treat a missing `dsh_employee_host_v1` catalog flag as proof that a template lacks the protocol, or a manually supplied flag as proof that it supports it; older images must still fail the actual fixed-command checks.

Human employee owners and workspace owners/admins can manage private plugin settings
through `GET/PUT /api/agents/{id}/dsh-plugins/{pluginId}/config`. The plugin must
already be attached in that workspace. Reads and writes are audited without values;
agent actors cannot use this credential-management surface. PUT requires
`expected_revision` from a fresh read and `config_override` containing `row_id`
and an object `config`. A null override explicitly inherits workspace settings;
an empty config selects package bundle defaults. Overrides replace the chosen row
wholesale, never merge another employee's identity. Stale writes return 409, including
a detach/re-attach cycle. Replacing the attached set preserves retained overrides.
The normal plugin list contains no employee overrides. These revisions describe
persisted configuration, not a Host/Profile application receipt.

Recipe export snapshots the effective employee configuration, package name, exact
version, integrity, selected row and enabled state. Private values become
`secret_ref` aliases. Preview lists `requirements.dsh_plugins`; confirm with
`dsh_plugin_bindings` mapping each recipe ref to an already imported destination
plugin UUID, plus new `secrets`. Name, version and integrity must match exactly;
missing mappings return 422 and artifact drift returns 409. No code is installed
from the recipe and no destination workspace credentials are inherited. Legacy
ref-only recipes require explicit mapping and use private package defaults.
Import and sync of plugin configuration require a human actor. Explicit empty
plugin lists clear bindings, while omitted lists preserve them. Sync rejects a
preview if employee plugin configuration changed since it was prepared. All
bindings and configuration are applied in the source transaction. Actual plugin
execution and Host/Profile application remain separate acceptance gates.


Employee Profile version status is available to human employee managers at
`GET /api/agents/{id}/dsh-profile`. `POST` to the same endpoint with `{}` prepares
one durable revision and its immutable dependency build intents. It accepts no
client-supplied revision, descriptor, placement or credential. Repeated preparation
of unchanged settings reuses the revision; changing credentials changes the
Profile revision without rebuilding identical package bytes. Status contains
`state`, string-valued `desired_revision` and `applied_revision`,
`applied_generation`, `applied_sandbox_id` and `current`, without configuration.
`waiting_for_builds`, `build_failed` and `pending_host` are not successful application.
The DSH Home page shows desired/last-confirmed versions and per-package build
status. A failed package is shown as failed, not as indefinite preparation.
The status API exposes package/version/state, attempt `id` and `can_retry`, never private settings.
Human managers may `POST /api/agents/{id}/dsh-profile/retry` with the observed
`revision` and `build_id`. Only a failed build with confirmed sandbox/artifact
cleanup can be retried. The transaction archives the old intent and gives the
new attempt a new ID; stale requests return 409 and cannot retry a later failure.
The Home page offers Retry build only for an eligible failed attempt. Refresh
after an uncertain response; never automatically repeat a retry request. A historical
receipt after a saved edit is `configuration_changed`; a retired/replaced Host
cannot be current. Preparation does not start a Host. The durable receipt store
requires matching live Host generation, exact descriptor and fresh configuration
under transaction locks. Native entry and platform task admission prepare the
same employee revision. Pending builds do not start a new writer; existing
unknown creates and retiring generations still reconcile. Published artifacts
are installed under the employee Home only after identity and digest validation.
The Profile is transferred in bounded chunks into an immutable 0600 file, so a
large configuration does not exceed Linux environment-variable limits. The Host
reads that file and must confirm its exact revision/digest before the server
records application. A changed or missing receipt retires the generation through
the existing drain/destroy flow. Code wiring alone does not prove live acceptance.

The plugin build ledger separates creation, remote execution, object verification
and cleanup. A replacement claimant reconciles the saved create intent rather
than creating another sandbox. A build becomes ready only after reading back the
stored archive and matching its digest and length; sandbox cleanup still requires
confirmed absence. Workspace deletion returns a conflict while a build has an
active or unresolved cloud intent, preserving the recovery record. Every backend
replica runs the shared PostgreSQL worker. The catalog admits a ready DSH template
by immutable ID; the fixed helper must confirm `dsh_plugin_build_worker_v1` inside
the sandbox before receiving any transfer grants. The disposable build sandbox
has no employee Home, employee role or employee configuration. Its fixed helper
receives short-lived HTTPS grants for exactly the saved source and artifact keys;
neither URLs nor employee credentials enter the ledger or helper receipts.
Repeated starts reconcile the same sandbox receipt without rebuilding; failed
uploads are cleaned up by the saved object key after sandbox absence is confirmed.
Code wiring and queued revisions do not prove cloud build or employee activation.

### Persistent native reminders

The managed Schedule adapter uses task-authenticated
`GET/POST /api/tasks/{taskId}/dsh/schedules` and
`DELETE /api/tasks/{taskId}/dsh/schedules/{scheduleId}`. GET and DELETE select
`session_id` in the query. POST accepts `session_id`, `schedule_id`, `prompt`,
`first_due_at` (the resolved UTC instant) and `every_seconds` (zero for one-shot,
otherwise at least 300). The actor, workspace, employee and creating task come
from the short-lived task token, never from request fields or a native entry URL.
Do not retain a task token for a future reminder.

Management requires an active dispatched/running FC DSH task bound to that exact
native Session and current member invocation permission. An automated parent
must have a persisted Schedule occurrence to resolve standing ownership; an
accountability field alone is insufficient. The same reminder identity and
content can be retried under a later authorized task of the same owner. It keeps
its original provenance and cannot resurrect a cancelled or consumed reminder.
Only the owning member can cancel it. Lists return an explicit `truncated` bit.

A persistence receipt is not delivery. `scheduled`, `overdue`, `cancelled` and
`consumed` distinguish the ledger state; consumed means task admission, not model
completion or a delivered message. The branch implements management, persistent
retry, fresh automatic task admission and original-Session native transport.
Admission rechecks the owning member's current invocation permission and uses
fresh task credentials. It does not pretend the owner sent another human message.
The native tool adapter, recurring same-Session batching and real preproduction
acceptance remain unfinished; do not advertise an available reminder feature.

`MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED` defaults off. Enable only after all
application replicas support occurrence-aware native claim/launch, so older
replicas cannot reinterpret reminders. Turning it off pauses discovery without
consuming pending records. It is not enabled in preproduction yet.
