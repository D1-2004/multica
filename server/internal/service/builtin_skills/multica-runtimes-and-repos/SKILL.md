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
