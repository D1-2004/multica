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

FC DSH task launch automatically provisions the employee AgenticFS Home when
its binding is missing. Launch waits for the persisted provisioning intent to
complete and never falls back to an ephemeral filesystem. Human owners and
workspace administrators can also inspect or advance provisioning through
`GET/POST /api/agents/{id}/dsh-home`; this operator API is unavailable to task tokens. Placement and the cloud access
package are deployment-owned. A pending create must reconcile its persisted
intent; never work around it by sharing another employee's volume. A provisioned
Home does not by itself prove that a native Host or business task is running.

DSH Session identity is preserved exactly: official browser UUIDs and platform
`session-UUID` identities are distinct valid names. The internal native adoption
primitive requires the same PostgreSQL transaction as task/input creation and
rejects scope, task or request conflicts. This storage primitive supports platform-dispatched tasks; it does not expose a native browser entry.

A runtime is the execution target behind an agent. A daemon owns local runtime processes and claims queued tasks from the server.

FC/E2B sandbox lifetime equals this task's lifetime. Create and task-start
renewal both default to 4800 seconds (`--lifecycle.ontimeout kill`). The
platform does not renew again while the task runs, so work that outlives
that window can be killed with no Issue event. About 30 seconds after the
task ends, a non-DSH sandbox that no later task can reuse (a task with
neither an Issue nor a chat, such as a run-only autopilot) is released, and
any other sandbox, DSH employee hosts included, keeps only a 10-minute idle
window for the next turn on the same Issue or chat. `nohup` / `setsid` do not
make a process outlive the sandbox: when a task is cancelled or fails, the
platform may end its processes within seconds; otherwise they run until the
sandbox is released or idles out, and a later turn that reuses the sandbox may
still find them. Sandbox reuse is on for each agent unless its execution
setting `sandbox_connection_reuse` is off. When that switch is on, tasks in
the same scene and from the same trigger share one sandbox, up to 6 at a time
(or `runtime.fc_e2b.connection_reuse.max_concurrent_tasks`). The runtime image
does not have to declare `sandbox_connection_reuse_v1`. A scheduled scene
task with no single trigger shares the scene's public sandbox. A seventh
concurrent task, an A2A task, and an agent on the employee filesystem each
keep a private sandbox. Do not leave work in background
processes. Persist patches,
logs, and artifacts with `multica attachment upload <path>` before exiting.
Issue and chat tasks retain their existing bindings; Employee Direct files remain
private artifacts of their own Run. Server upload receipts include `size_bytes`
and `sha256`; comment JSON uses `size_bytes`, not `size`.

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

### DSH task trajectories

The task trajectory includes the root turn and its verified child activations.
Use the session selector to inspect each child's native tools and output. Native
sequence numbers belong to their own session; they are not a global task counter.
A resumed child contributes only the activation belonging to this task, excluding
seeded history and other tasks. An interrupted child is explicitly marked.

### ASB sandbox capacity

Cold launches read current ASB allocations and use the regional API endpoint
with the most free slots among the Agent's selected execution regions.
The Agent execution settings discover choices from `GET /api/runtimes/{id}/asb-regions`
and save the selection as `runtime_config.asb_regions`. An absent or empty array
means all API regions; a non-empty selection is a strict placement constraint.
Full selected regions wait without failing over outside the selection. A warm
sandbox in another or unknown region is replaced at the next launch. BUC users
can select only `cn-hangzhou`. New regional allocations and quota increases are
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

The default policy permits `*.alibaba-inc.com` and `*.dingtalk.com` as built-in
domain families. Other destinations still require an allow rule under default deny.

ASB sandboxes deny outbound connections unless the destination is allowed.
Required platform services and configured Agent MCP/service hosts are included
automatically. Workspace owners/admins can add exact domains or individual IPs
in the Runtime details page. Custom wildcards, URLs and CIDR ranges are rejected.
Defaults include DWS signed file transfers, document OSS, mail and Stream,
and `tp-alilang.alibaba-inc.com` for BUC trust-device registration. The two
managed WireGuard gateway IPs `140.205.109.26` and `140.205.109.30` are also
allowed because tunnel handshakes use literal UDP destinations.
Regional transfer subdomains under trans.dingtalk.com and down.dingtalk.com
are managed defaults. Enterprise-specific storage and third-party download
hosts still require an exact Runtime entry.
Changes apply at the next task launch: sandboxes using an older policy are
replaced, so files stored only in that sandbox do not carry over. Active tasks
finish with their existing policy. Ask the user to configure a missing
destination; do not try to bypass the sandbox network policy.

Human owners/admins can prepare and inspect the employee filesystem from the workbench configuration page. DSH plugin bindings and settings are managed in the workbench. A confirmed configuration save publishes the desired revision and dependency build intents; the next ordinary task applies that revision. Saving configuration neither starts an employee sandbox nor waits for a native page to become ready.

The native DSH page, entry/access APIs, browser proxy, session list/routing and browser restart have been removed. Native browser/plugin inputs cannot create platform Tasks. There are no reverse authority/input polls, historical Host input recovery worker, or Profile worker that starts historical employee Hosts. Runtime task execution retains the loopback private control service and ordinary task-bound model/tool credentials. Native plugin snapshot import is removed; saved workbench configuration is authoritative. Task launch and queued dependency builds still perform scoped sandbox operations.

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
Queued FC tasks waiting for Profile builds or host reconciliation are retried by
the existing background sweeper under the normal per-task launch lease; an open
browser is not required, and other session sandboxes remain independent.
The DSH configuration page shows desired/last-confirmed versions and per-package build
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
under transaction locks. Platform task admission prepares the
saved employee revision. Pending builds do not start a new writer; existing
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
`first_due_at` (the resolved UTC instant), `every_seconds` (zero for one-shot,
otherwise at least 300) and `source_task_id` (the original creating task recorded
by the private adapter). The current actor, workspace, employee and bearer task
come from the short-lived task token. The original task must independently match
the employee/Session and creator; it is provenance, never a grant. Responses retain
`source_task_id`. A durable intent can be first published after its due instant
under a fresh authorized task; an ended bearer task or another creator is denied.
POST may include `cancelled: true` and `cancellation_task_id` for an offline
create/delete pair. Both original creating and cancelling tasks must resolve to
the currently authorized owner. Creation and the tombstone commit together; a
late create retry cannot expose or reactivate it. `GET` on the individual
`/schedules/{scheduleId}?session_id=...` path reads scheduled, consumed and
cancelled state without republishing another owner's create. A missing record is
404; this is not the same as a truncated active list.
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
Complete recurring batches are now admitted as one task, with one latest
occurrence per due rule and one-shot priority. Different standing owners are kept
separate. The native tool adapter and real preproduction acceptance remain unfinished; do not advertise an available reminder feature.

`MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED` defaults off. Enable only after all
application replicas support occurrence-aware native claim/launch, so older
replicas cannot reinterpret reminders. Turning it off pauses discovery without
consuming pending records. It is not enabled in preproduction yet.


### Employee filesystem on FC runtimes

Employees with a provisioned filesystem mount the same AgenticSpace in every FC provider sandbox at `/mnt/multica`. `$MULTICA_FS_ROOT/files` is the shared user file directory; `DSH_HOME=/mnt/multica/home` is one subdirectory. Provisioning and status use human-managed `/api/agents/{id}/filesystem`; `/dsh-home` remains an API alias. Non-DSH FC providers expose Filesystem configuration; DSH keeps filesystem and plugins together in its DSH configuration.

Execution sandboxes are selected by employee and conversation/task scope, with independent lifecycle records in `employee_filesystem_sandbox`. A scope's startup does not reserve the entire filesystem. Platform task bindings record their sandbox scope so subsequent platform and scheduled turns return to that host. Shared-file concurrent modifications are coordinated by users and agents; the platform does not provide automatic conflict merging or a filesystem-wide writer queue. Older single-employee-writer descriptions above are superseded by this contract.

Disabled employee plugin bindings remain saved in the workbench but are excluded
from the executable Profile. A disabled older package must not insert the same
loader rows as its enabled replacement.

Stable image releases retain runtime-start failure statistics as observations.
The absolute 5% and relative 3 percentage point failure thresholds log warnings
but do not block rollout advancement or observation completion. Target update
failures, incomplete cutovers and release state checks remain enforced.

Long-lived native DSH Sessions may exceed the 32 MiB task artifact limit. The runner validates the complete frozen history prefix with temporary page spooling and replays it in source order before deciding whether a request is absent. Only the owned task turn and referenced child activations enter the artifact; their size limits and ownership checks still apply. A truncated or inconsistent history cannot authorize a retry.


For DingTalk Stream robots, `/new` (or `/reset`) by itself arms a durable reset
for the next message. The adapter preserves canonical `CommandText` separately
from the stripped model input, including leading robot mentions. `/new <text>`
starts that message with a fresh provider session. Visible history is retained.

DSH reset uses a durable conversation epoch: the reset task and later platform
turns bind a new native Session. Retries retain their committed Session/request;
old native sessions and their schedules retain their original epoch. The schema
expansion release must reach every replica before removing the legacy scope
uniqueness index and enabling epochs. Do not roll back to pre-epoch binaries after
activation; use a forward repair that preserves all epochs.


## Employee Direct tasks

Employee Direct runs an independent EmployeeTask through the existing task queue
without an Issue or permanent Autopilot. Its queue ID, EmployeeTask ID, and Run ID
are distinct. Read the persisted task result/messages/trajectory using the task's
authorized identity; a shared workspace or public Agent does not make Direct
content public.

A local daemon must authenticate its runtime binding and advertise
`employee-direct-v1`. FC candidate templates declare that protocol with the
recognized `multica-m7-v<fingerprint>-r2-<commit>` alias only after the candidate
build verifies its embedded daemon. The immutable template ID remains execution
identity. An r1 template or an older local daemon cannot consume the Direct
prompt; ordinary Issue, chat and Autopilot work retains its existing behavior.
Human management/read permission alone does not authorize claiming or completing
a task. Do not forge capability headers or edit runtime metadata to enable it.

Direct supports text, persisted execution traces, and task-scoped file artifacts.
Use the existing `multica attachment upload <path>` command while the current
queue task's token is active. The server resolves the EmployeeTask and Run from
that queue; do not supply an Issue/Chat binding or use another task's credentials.
Wait for a successful receipt with a non-empty attachment id. A local path, an
upload still in progress, or a failed metadata commit is not a published artifact.
Repeating the same filename and bytes within that queue returns the same ready
reference; an in-progress attempt may ask the caller to retry.

Use `multica attachment download <attachment-id>` for authenticated reads.
Direct files are encrypted in the existing object store and served through the
private attachment API. The returned URL is an authorized-access link, not a
public CDN link or proof of native DingTalk file delivery. A completion notice
may reference only persisted ready artifacts. A DingTalk requester who has not
been mapped to an authorized workspace identity does not gain download access
because a manager can inspect the file. Direct artifacts cannot be implicitly
rebound to an Issue or Chat.

This does not prove restoration of an old execution. A database cancellation
records the request; it does not prove that the provider process has exited.
