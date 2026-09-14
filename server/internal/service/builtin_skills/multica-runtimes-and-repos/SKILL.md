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

The native chat admission service then writes the human input, platform task and exact native Session/request binding in one PostgreSQL transaction. Registration and admission take the same Runtime and employee admission locks as Host startup, then require current invocation permission, FC DSH runtime and an unexpired grant for the current Host generation. Existing chat mappings must belong to the same human; issue mappings cannot be adopted as chats. Identical request retries reuse the original task/message without another launch; changed input or scope conflicts are rejected. The response contains `session_id`, `request_id`, `chat_session_id`, `task_id`, `message_id`, `queued` and `replayed` (201 for new admissions, 200 for replay). An unconfirmed admission returns 503 and must be retried with the same identity. Gateway prompt forwarding and real PostgreSQL/browser acceptance remain pending; this endpoint alone does not make native UI business prompts available.

Human employee managers can request a short-lived native entry with `POST /api/agents/{id}/dsh-native/access` and an empty body. The server requires a provisioned Home, then starts or recovers its FC DSH Host under the same Runtime and employee locks used by platform tasks, without creating a task. It verifies the exact live gateway readiness receipt. It returns `access_id`, `entry_url` (a one-minute fragment credential) and `expires_at`; the fragment is consumed at the separate `/_multica/open` gateway entry page, including when an older session Cookie exists. Do not log, share or persist the URL. `DELETE /api/agents/{id}/dsh-native/access/{accessId}` revokes the grant. Opening an entry can start, renew or recover the Host; revoking access does not destroy it. Existing task admissions and live native grants block template replacement until they drain; an uncertain create or destruction never permits another writer. The employee DSH Home tab exposes Prepare DSH after storage is ready, then a short-lived Enter DSH link. The link is cleared on expiry or employee change and excluded from query/mutation data; errors do not trigger automatic startup retries. This entry does not prove native prompt/task admission is accepted.

The gateway authority is the deployment app origin (`web.app_url` in Diamond or `MULTICA_APP_URL` in environment mode), independent of the FC task relay URL. A missing authority fails closed. The backend now opens a signed reverse authorization transport to the v2 FC gateway before issuing an entry. It polls only the deployment-derived gateway origin, never follows redirects, and signs each decision with a deployment key derived in a separate HKDF domain; only the public key enters FC. Requests bind one exact Host and are never replayed after an uncertain exchange. Each browser request and periodic WebSocket check still consults current database authorization. This transport does not create or renew a Host. The capability callbacks remain available via `POST /api/dsh-native/access/exchange` and `/check`, using the entry/session Bearer credential and exact `workspace_id`, `agent_id`, `generation`, `sandbox_id` body. These callbacks independently check the current employee manager and the persisted running Host. Their credentials are not Multica API tokens or model/tool credentials.

Employee Host support is established by the fixed Home initialization and native supervisor receipts during launch. FC template catalog capabilities are derived from provider fingerprints and do not read arbitrary Docker labels. Do not treat a missing `dsh_employee_host_v1` catalog flag as proof that a template lacks the protocol, or a manually supplied flag as proof that it supports it; older images must still fail the actual fixed-command checks.
