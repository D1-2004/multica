# Runtimes and repos source map

- `server/internal/service/asb_capacity_gate.go` distinguishes short create pacing from cached full/429 results. `asb_capacity.go` waits the remaining pacing interval inside the current launch under the tenant lock; it preserves cancellation and never reports pacing as full quota. `asb_capacity_waiter.go` wakes the next eligible waiter after a recovery launch finishes.

- `server/internal/service/asb_capacity_region.go` maps live quota regions to ASB regional API hosts for cold creation. `asb_capacity.go` refreshes allocations after quota contention and after reclaim; only explicit create-time `403 QUOTA_EXCEEDED` errors trigger regional failover. Regional client copies do not change the shared tenant lock/cooldown or sandbox-ID-based lifecycle routing.
- `server/internal/service/asb_capacity.go` reclaims idle chat and issue sandboxes with the same policy and no retention grace. `cloud_sandbox_session.sql` and scope advisory locks fence active tasks and launches. `runtime_start.sql`, `asb_capacity_waiter.go`, and the ASB terminal wakeup in `task.go` select capacity waiters in creation order without task-kind priority or previous-agent affinity.
- `server/cmd/multica/cmd_runtime.go` registers `runtime list`, `usage`, `activity`, `update`, and `delete`.
- `server/cmd/multica/cmd_daemon.go` refuses daemon start/restart/stop from a daemon-managed task and authenticates local shutdown with a per-instance control capability.
- `runtime list` reads `/api/runtimes` and prints `id`, `name`, `runtime_mode`, `provider`, `status`, and `last_seen_at`.
- `runtime update` posts to `/api/runtimes/{runtime-id}/update`; with `--wait` it polls update status. Initiation enforces runtime-owner or workspace-owner/admin access through `canEditRuntime`; status polling additionally permits that request's immutable initiator so an in-flight poll survives an admin-role change (`server/internal/handler/runtime_update.go` and `runtime.go`).
- `runtime delete` deletes `/api/runtimes/{runtime-id}`; with `--cascade`, it first reads the `runtime_has_active_agents` conflict payload and posts those ids to `/api/runtimes/{runtime-id}/unbind-agents-and-delete` (the older `/archive-agents-and-delete` path still routes to the same handler for installed clients). Both delete paths run `unbindRuntimeForDelete` in `server/internal/handler/runtime.go`: user agents are unbound (`runtime_id = NULL`, MUL-5559), their task history is detached so deleting the runtime cannot cascade it away, active tasks are cancelled, and only `kind='system'` agents are hard-deleted.
- `server/cmd/multica/cmd_repo.go` registers `repo checkout <url> [--ref]`.
- `repo checkout` requires `MULTICA_DAEMON_PORT` plus a task-scoped `MULTICA_TOKEN`, sends that token as a loopback bearer with `workspace_id`, `workdir`, `ref`, `agent_name`, and `task_id`, then prints the checked-out path.
- `server/internal/daemon/health.go` accepts a checkout capability only while its task is active, binds it to that task/workspace/workdir/repo set, and resolves the checkout ref: request `ref` wins; otherwise it asks `server/internal/daemon/daemon.go` for the current task's project repo default ref.
- `repo checkout` requires `MULTICA_DAEMON_PORT`, sends `workspace_id`, `workdir`, `ref`, `agent_name`, `task_id`, and the daemon-managed optional `checkout_mode` to local daemon `/repo/checkout`, then prints the checked-out path.
- `server/internal/daemon/health.go` resolves the checkout ref: request `ref` wins; otherwise it asks `server/internal/daemon/daemon.go` for the current task's project repo default ref. It forwards the validated isolated-checkout mode into `repocache.WorktreeParams`.
- `server/internal/daemon/daemon.go` injects `MULTICA_REPO_CHECKOUT_MODE=isolated` for Linux and Windows Codex tasks. Linux keeps the isolated checkout it already had; Windows Codex now uses the same layout to cover its native sandbox, where a linked worktree's external gitdir stays read-only and `git add` / `git commit` fail from inside the checkout (multica-ai/multica#6449). That failure only bites a Windows user who opted into `windows.sandbox` — `server/internal/daemon/execenv/codex_sandbox.go` defaults both Linux and Windows Codex to `danger-full-access` — but the checkout layout is chosen per platform, not per sandbox policy, so it does not depend on a task's resolved policy. `server/internal/daemon/repocache/cache.go` implements the mode as a local clone with task-local Git metadata and the real repository as `origin`; on Windows it clones with `--no-hardlinks` so the checkout's objects are private copies rather than NTFS links that share one file and security descriptor with the cache. Other runtimes keep the linked-worktree path.
- When the bare cache is a partial clone, that isolated checkout must have `remote.origin.promisor` / `partialclonefilter` restored before its first `checkout`: `git clone --local` neither inherits them nor errors on the missing objects it leaves behind, so the checkout would otherwise succeed with an empty working tree. The linked-worktree path shares the cache's own config and needs no such repair.
- `server/cmd/server/router.go` registers daemon APIs under `/api/daemon`, including workspace repos and task claim.
- `server/internal/daemon/daemon.go` claims tasks, prepares workdirs, launches provider CLIs, and reports completion. It validates the task-scoped `mat_` credential, exports `MULTICA_TASK_CONFIG_ROOT`, and keeps that daemon-owned variable ahead of custom environment assembly so agents cannot override it.
- `server/internal/daemon/execenv/execenv.go` creates and restores the private per-task `multica-config` directory with mode `0700`; it does not copy the daemon Owner's Multica profile into the directory.
- `server/internal/cli/config.go` resolves Multica CLI profiles below `MULTICA_TASK_CONFIG_ROOT` when present while leaving ordinary `HOME`-based resolution unchanged outside tasks.
- `server/cmd/multica/cmd_agent.go`, `cmd_config.go`, `cmd_auth.go`, `cmd_login.go`, `cmd_setup.go`, `cmd_workspace.go`, `cmd_runtime_profile.go`, and `cmd_daemon.go` enforce the task boundary: API calls require task authentication, task-local config commands fail closed without their root, auth status hides credential material, and human/local profile or daemon commands reject managed task context.
- `cmd_daemon.go` scopes the two read-only diagnostics instead of rejecting them: `daemonStatusHealthPort` takes the injected `MULTICA_DAEMON_PORT` (never the `--profile` hash, which would report an unrelated daemon), `resolveDiskUsageRoot` takes `daemon.TaskWorkspacesRootEnv` (never the `$HOME`-derived default), `checkTaskDiskUsageScope` rejects the flags that widen the scan past this daemon, and the STATUS column plus the other-roots hint are skipped because both reach for Owner profile state.
- `server/internal/daemon/execenv/runtime_config.go` injects task/project/repo context into agent workdirs.

- `server/internal/service/asb_network_policy.go` composes default-deny ASB policies from platform dependencies, Agent configuration and Runtime metadata. `asb_client.go` enforces deny at creation; `asb_launcher.go` refuses warm reuse after a policy change.
- `server/internal/handler/runtime_asb_network.go` serves owner/admin-only GET/PUT `/api/runtimes/{runtimeId}/asb-network-policy`; `packages/views/runtimes/components/asb-network-policy-section.tsx` edits exact additional targets.
- `docs/security/asb-dws-network-audit.md` maps DWS direct-transfer, OSS, mail, Stream and distribution dependencies to reviewed default rules; only two managed transfer families accept wildcards at the creation boundary.
# DSH employee Home provisioning

- `server/internal/handler/dsh_home.go`: human/manage-authorized status and provisioning endpoints for FC DSH agents.
- `server/internal/dshhost/provision.go` and `provision_postgres.go`: durable placement, per-resource intent, receipt reconciliation and immutable binding.
- `server/internal/dshhost/cloud_storage.go` and `cloud_api.go`: NAS/RAM/FC calls, resource-chain verification and bounded ACS transport using the official signer.
- `server/cmd/server/dsh_storage_options.go`: deployment-owned placement and the Aone-managed cloud access package; no task-supplied credentials.

## Employee DSH Home UI

- `packages/core/agents/dsh-home.ts`: workspace/employee-scoped status queries and explicit provisioning mutation with post-response reconciliation.
- `packages/views/agents/components/tabs/dsh-home-tab.tsx`: storage and host status, preparation and retry controls; owner/admin FC DSH visibility through `agent-overview-pane.tsx`.
- `packages/core/api/dsh-home-client.test.ts`, `packages/views/agents/components/tabs/dsh-home-tab.test.tsx`: malformed response, accepted/pending, unknown write outcome, no implicit writes and cache isolation checks.

## Native browser authorization

- `server/internal/handler/dsh_native.go`: human entry/revocation, capability exchange/check and fresh workspace management checks.
- `server/internal/service/fc_e2b_dsh_native.go`: deployment-owned FC origin, application authority independent of the task relay, and bounded exact gateway readiness verification; no Host lifecycle mutations.
- `server/internal/dshhost/native_access*.go`: digest-only durable access, one-time exchange and running Host predicates.
- `server/internal/handler/dsh_native_test.go`, `server/internal/service/fc_e2b_dsh_native_test.go`: callback replay, identity and permission boundaries, readiness mismatch and old-image rejection. Runtime/browser and PostgreSQL acceptance must run in preproduction.

- `server/internal/service/fc_e2b_dsh_host.go`: injects the deployment-owned gateway authority, sandbox origin and sandbox ID into the employee supervisor. Gateway readiness remains separate from native prompt/task admission.

- `server/internal/service/fc_e2b.go` / `fc_e2b_dsh_host.go`: mandatory DSH employee admission lock and real Home/Host receipt verification, independent of provider-derived catalog capability labels.

- `server/internal/service/fc_e2b_dsh_authority.go`: backend-initiated authorization polling, deployment signing, exact-Host decisions and bounded connection lifetime; no writer lifecycle mutations.
