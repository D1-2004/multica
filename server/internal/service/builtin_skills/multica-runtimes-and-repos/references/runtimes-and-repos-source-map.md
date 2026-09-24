# Runtimes and repos source map

- FC/E2B sandbox lifetime: `server/internal/service/fc_e2b.go` `defaultFCE2BTimeoutSeconds` (4800) and `createSandbox --timeout`; `fc_e2b_timeout.go` `sandboxTaskTimeout` / `renewSandboxForTask` use the same duration (config may raise it, never lower it). `--lifecycle.ontimeout kill`. Release after a terminal task: `fc_e2b_sandbox_release.go` (`TaskTerminal` via `TaskRuntimeTerminalObserver` from `task.go` `publishTaskEvent`; single-use non-DSH scopes released, others trimmed to `fcE2BSandboxIdleRetention` = 10 minutes; log event `fc_e2b_sandbox_lifecycle`). Documented in `docs/fc-sandbox-lifecycle.md`.
- Attachment upload receipts: `server/internal/handler/file.go` `AttachmentResponse.size_bytes` + `sha256`; CLI `server/internal/cli/client.go` `UploadFile` / `Receipt()`. Limit `maxUploadSize` 100 MB.

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
- `docs/security/asb-dws-network-audit.md` maps DWS direct-transfer, OSS, mail, Stream and distribution dependencies to reviewed default rules; the creation boundary accepts the two managed transfer families plus the built-in `*.alibaba-inc.com` and `*.dingtalk.com` domain families.

### ASB execution regions and BUC refresh

- `server/internal/handler/runtime_asb_regions.go`: live region discovery and selection validation.
- `server/internal/service/asb_capacity_region.go`: strict Agent placement selection and quota routing.
- `server/internal/service/asb_launcher.go`: selected-region constraints on warm sandbox reuse and cold creation.
- `server/internal/service/enterprise_identity.go`: independent ID-token expiry and encrypted refreshed-trio persistence.

# DSH employee Home provisioning

## Task and child trajectories

- `server/pkg/agent/dsh_native_history.go`: privately spools complete frozen history pages, replays in source order, and retains only task-owned events; per-task limits do not cap total Session history. `dsh_native_history_stream_test.go` covers history above 32 MiB, foreign turns, retained ownership, pagination/cancellation failures, temporary file cleanup and child activation bounds.

- `server/pkg/agent/dsh_native_children.go`: follows child references using the official parent/child/mode address and selects the exact activation after task resources are released; pagination retains that same address.
- `server/pkg/db/queries/runtime_start.sql`, `server/internal/service/runtime_start_attempt.go`, `server/cmd/server/runtime_sweeper.go`: persist and retry FC host-preparation waits without browser polling, through the existing task launch lease.
- `server/pkg/dshtrajectory/children.go`: validates request identity, parent lineage and contiguous child intervals; counts all included events without copying seeded or unrelated task history.
- `server/internal/handler/dsh_trajectory.go`: validates and stores the complete task artifact.
- `packages/views/common/task-trajectory/trajectory-model.ts`, `dsh-trajectory-dialog.tsx`: parse the task bundle and select root or child events while retaining native sequence numbers and interruption state.

- `server/pkg/protocol/dsh_native.go` validates and clones official native prompt content with bounded transport space; `dsh_native_test.go` covers attachment-only input, invalid unions, nulls and content preservation.
- `server/internal/handler/dsh_native_claim.go` checks task-owned input, native binding and daemon capability before delivery. `daemon.go` requeues refused claims and excludes native attachments from generic callback text. Pure claim tests cover old daemons, changed scope, binding failures and unchanged ordinary inputs.
- `server/internal/daemon/dsh_native.go` and `server/pkg/agent/dsh_native.go` preserve native Session spelling and deliver the complete typed request to the authenticated Host control socket. Protocol-peer tests inspect actual serialized control requests without starting a local Host.


- `server/internal/dshhost/session.go` and migration `9238_dsh_browser_session_identity`: preserve official UUID and platform-prefixed Session identities; transactional native adoption refuses remapping and duplicate request ownership.

- `server/internal/handler/dsh_home.go`: human/manage-authorized status and provisioning endpoints for FC DSH agents.
- `server/internal/dshhost/provision.go` and `provision_postgres.go`: durable placement, per-resource intent, receipt reconciliation and immutable binding.
- `server/internal/dshhost/cloud_storage.go` and `cloud_api.go`: NAS/RAM/FC calls, resource-chain verification and bounded ACS transport using the official signer.
- `server/cmd/server/dsh_storage_options.go`: deployment-owned placement and the Aone-managed cloud access package; the UI and FC launcher share the same provisioning callback with their own database handle. No task-supplied credentials.
- `server/internal/service/fc_e2b.go`, `fc_e2b_dsh_host.go`: first DSH task automatically provisions a missing filesystem binding; pending provisioning defers launch, errors block launch, and DSH never uses an ephemeral fallback.

## Employee DSH configuration UI

- `packages/views/agents/components/tabs/dsh-config-tab.tsx`, `agent-config-navigation.ts`: one DSH configuration destination combining Home/native entry, Profile receipts and plugin management. Old `view=dsh_home` and `view=dsh_plugins` URLs normalize to `view=dsh`; non-DSH providers hide it, and Home still requires FC/cloud management permission.

- `packages/core/agents/dsh-home.ts`: workspace/employee-scoped status queries and explicit provisioning mutation with post-response reconciliation.
- `packages/views/agents/components/tabs/dsh-home-tab.tsx`: storage and host status, preparation and retry controls; owner/admin FC DSH visibility through `dsh-config-tab.tsx`.
- `packages/core/api/dsh-home-client.test.ts`, `packages/views/agents/components/tabs/dsh-home-tab.test.tsx`: malformed response, accepted/pending, unknown write outcome, no implicit writes and cache isolation checks.

## FC stable provider scope

- `server/cmd/multica/cmd_runtime_stable.go`: explicit `--backend aliyun_fc --provider dsh` channel, Runtime, history and release operations; release-ID lifecycle actions preserve persisted scope.
- `server/internal/handler/runtime_fc_e2b_stable.go`: validates query/body `provider_scope`; history filters before limiting results.
- `server/internal/service/fc_e2b_stable_scope.go`, `fc_e2b_stable.go`: independent pointer initialization under the backend lock, inherited shared lookup, actual artifact capabilities separated from release targets, late-join filtering and scoped publication/rollback.
- `server/internal/handler/runtime_fc_e2b.go`: new stable Runtime resolves the selected provider pointer under the creation/publication lock, preserving the shared template's default provider when omitted.
- `server/migrations/9239_fc_stable_provider_scope.*.sql`: persisted scope and old-worker target/publication/creation guards; downgrade refuses to discard independent channel history.
- `server/internal/service/fc_e2b_stable_scope_test.go`: target/capability separation, late joins, explicit empty selection and idempotency scope.
- `server/internal/service/fc_e2b_stable_scope_database_test.go`: opt-in real preproduction test, temporary tables and transaction rollback against installed migration functions. Requires `DSH_STABLE_SCOPE_TEST_DATABASE_URL`; never runs local database services.
- `packages/core/api/client.ts`, `schemas.ts`, `stable-provider-scope-schema.test.ts`: optional provider query, release scope parsing and malformed/historical response checks. Existing shared-channel UI does not provide scoped publishing controls.


## Employee plugin configuration

- `server/internal/handler/agent_dsh_plugin_config.go`: human owner/admin gate, audited reveal/write, bounded row validation and revision conflict checks. `dsh_plugin.go` preserves overrides on set replacement and serializes detach with configuration writes.
- `server/migrations/9240_agent_dsh_plugin_config.*.sql`, `server/pkg/db/queries/dsh_plugin.sql`: employee binding overrides and sequence revisions that cannot repeat after detach/re-attach. `scripts/generate-dsh-plugin-sqlc.py` regenerates only this query/model without modifying historical migration ordering.
- `server/internal/handler/dsh_plugin_dispatch.go`: effective employee configuration replaces workspace defaults; invalid configuration rejects the claim rather than silently dropping a plugin.
- `packages/core/api/agent-dsh-plugin-config-schema.ts`, `packages/core/dsh-plugins/queries.ts`, `packages/views/agents/components/tabs/dsh-plugin-config-dialog.tsx`: explicit audited read, ephemeral editor, revision-aware writes and no automatic retry. Saved configuration is not displayed as applied to the running Host.
- `server/internal/handler/agent_dsh_plugin_config_test.go`, `packages/core/api/agent-dsh-plugin-config-client.test.ts`, `packages/views/agents/components/tabs/dsh-plugins-tab.test.tsx`: isolation, malformed configuration/response, secret-safe errors and explicit read/save/conflict tests. Real preproduction persistence, concurrent writes and Host application require separate evidence.

## DSH plugin recipe identity and rebinding

- `server/internal/handler/agent_package_dsh_plugins.go`: pinned export, explicit destination mapping, private configuration, human actor requirement and atomic binding replacement.
- `server/internal/handler/agent_package_configuration.go`: plugin and secret requirements, preview redaction and runtime validation.
- `server/internal/handler/agent_source_preview.go`: plugin configuration and revisions participate in source preview conflict detection.
- `server/internal/agentsource/agent.schema.json`: portable plugin package identity and configuration contract.
- `packages/views/agents/create/package-requirements-form.tsx`: explicit matching destination selection and new credential entry.
- These paths save configuration; they do not prove Host/Profile application or live plugin execution.

- Pre-release publication integration: `agent_package_service.go` and `agent_package_section_codecs.go` keep the single paired import/export registry; the DSH plugin codec applies explicit mappings and exports the current pinned private configuration. ZIP, Git, builder and rollback use this same transaction. `agent_package_reuse.go` requires explicit plugin secret inputs even when an environment alias matches.


## Durable employee Profile revisions

- `packages/core/agents/dsh-profile.ts` and `packages/views/agents/components/tabs/dsh-profile-status.tsx`: workspace/employee-scoped status reads, explicit preparation, desired versus confirmed versions and failed build display. Plugin edits invalidate the receipt cache.

- `server/internal/dshprofile/profile.go` and `postgres.go`: configuration-sensitive immutable revisions, credential-independent build keys, parent/publication locks, queued build intents and exact generation/configuration receipt fencing. Public status excludes private descriptors and includes employee-scoped package build states, including failures.
- `server/migrations/9241_dsh_employee_profile.*.sql` through `9244_dsh_plugin_build_identity.*.sql`: durable Profile/build state and separately created concurrent unique indexes. Workspace teardown removes these rows in the parent transaction.
- `server/internal/handler/dsh_profile.go`, `server/internal/service/fc_e2b_dsh_profile.go`: human manager-only status/preparation, transaction-bound effective employee settings and shared Runtime/employee lock order. No endpoint fabricates a Host receipt or starts a build/Host on read.
- `packages/core/api/dsh-profile-schema.ts` and `dsh-profile-client.test.ts`: exact string revisions, workspace-bound requests and malformed receipt rejection.
- `server/internal/dshprofile/postgres_test.go`: opt-in isolated-schema preproduction tests for cross-replica publication, pending builds, replay timestamps, config edits, changed revisions/generations and retiring Hosts. Local runs skip the database test.
- `server/internal/dshprofile/delivery.go`, `server/internal/service/fc_e2b_dsh_delivery.go`, `fc_e2b_dsh_host.go`: immutable publication metadata, scoped artifact download, bounded Profile transfer into a private file, task admission and live Host receipt acknowledgement. Configuration bytes never enter build or artifact-install requests.
- `server/internal/service/fc_e2b_dsh_delivery_test.go`, `fc_e2b_dsh_host_test.go`: artifact identity/receipt rejection, large Profile transport, actual revision matching and opt-in multi-pool Host/revision tests. Local tests do not certify employee Home or live application.
- `server/internal/dshprofile/build_worker.go`, `build_postgres.go`, `build_artifact.go`: durable claimed execution phases, persist-before-create, unknown-create reconciliation, scoped immutable transfer receipts, object readback verification and confirmed cleanup.
- `server/internal/service/fc_e2b_dsh_build.go`, `server/internal/dshhost/fc_build.go`, `server/cmd/server/main.go`: capability-gated FC build driver, exact persisted sandbox identity, fixed idempotent helper commands and shared-ledger background reconciliation. No employee mounts, role or configuration in build sandboxes.
- `server/internal/storage/s3.go` and `s3_test.go`: bounded PUT presigning against the public object endpoint without an empty-body checksum; signed URLs remain transient.
- `server/internal/service/fc_e2b_dsh_build_test.go` and `server/internal/dshhost/fc_build_test.go`: scoped transfer grants, identity receipts, CLI error redaction, duplicate-create discovery and confirmed cleanup boundaries. These unit tests do not prove real FC/OSS execution.
- `server/migrations/9245_dsh_plugin_build_worker.*.sql` and `9246_dsh_plugin_build_due.*.sql`: additive execution metadata and a separate concurrent due-work index; old Profile readers remain compatible.
- `server/internal/handler/workspace.go` and `GuardBuildDeletion`: workspace-parent then build-row locking prevents deleting the recovery ledger while cloud execution is active or unresolved.
- `server/internal/dshprofile/build_worker_test.go`: simulated lost create/SQL receipts, stale claims, scope changes, malformed artifact receipts, object byte mismatch and cleanup failures. `build_postgres_test.go` uses two real preproduction pools for concurrent claims and deletion fencing; local runs skip it.

### Explicit failed dependency build retry

- `server/internal/dshprofile/retry.go`: saved revision/source and exact attempt CAS; only failed/done rows reset, archived attempt metadata stays in PostgreSQL.
- `server/migrations/9247_dsh_plugin_build_attempts.up.sql`: attempt history on the existing workspace-owned build row.
- `server/internal/handler/dsh_profile.go`: human employee management boundary and strict retry request; 409 on changed or uncleaned attempts.
- `packages/views/agents/components/tabs/dsh-profile-status.tsx`: explicit retry, no automatic mutation retry, status refresh after uncertain receipt.

## Persistent DSH Schedule management

- `server/internal/handler/dsh_schedule.go`: task-token-only management boundary;
  route/task/workspace cross-checks, strict body fields and private no-store replies.
- `server/internal/service/dsh_schedule.go`: current bound task, employee/runtime,
  member and invocation checks under transaction locks; native Session isolation,
  immutable retry, independently verified create/cancel provenance, atomic cancelled
  publication, individual lifecycle readback and bounded creation-ordered lists.
  Runtime-then-employee lock order; acquiring locks does not grant permission.
- `server/internal/dshschedule`: durable registration, tombstones, due planning,
  occurrence identity and same-transaction task/binding/receipt advancement.
- `server/internal/handler/workspace.go`: removes reminder content and occurrence
  rows in workspace teardown under the existing parent transaction.
- `docs/plans/2026-09-15-dsh-schedule-admission.md`: trigger evidence, authorization
  contract, contrast cases, integration and verification boundaries.

- `server/internal/service/dsh_schedule_dispatch.go`: fresh automatic task admission
  with current permission, immutable chat input, original native scope and wake
  only after commit; no previous execution credential copied.
- `server/internal/dshschedule/worker.go`: durable due discovery, bounded retry and
  next-due compare fence for delayed errors from other replicas.
- `server/internal/dshschedule/execution.go` and
  `server/internal/handler/dsh_schedule_dispatch.go`: reconstruct native input
  exclusively from committed occurrence evidence; refuse scope/capability drift.
- `server/internal/scheduler/jobs_dsh_schedule.go`: shared PostgreSQL lease job;
  `cmd/server/main.go` isolates its bounded scan loop from existing jobs.

- `server/cmd/server/main.go` and `src/main.sh`: default-off
  `MULTICA_DSH_SCHEDULE_DISPATCH_ENABLED` deployment gate. All replicas must
  understand the scheduled native transport before dispatch is enabled.

- `server/internal/dshschedule/batch.go`: official one-shot priority and complete
  recurring batch framing/identity. Scope and standing ownership cannot mix.
- `server/internal/dshschedule/postgres.go`: NOWAIT on complete due siblings,
  per-occurrence ordinals, one task per batch, and atomic receipt/advancement.
- `server/internal/dshschedule/execution.go`: complete ordered receipt readback;
  changed membership, order or native request identity fails closed.

- `server/internal/service/dsh_schedule_recovery_database_test.go`: opt-in preproduction test for first publication after source completion/due time, fresh-current-task authority, original source preservation, unbound/accountability-only/foreign creator rejection and cancelled replay. Not executed locally.

- `server/internal/dshhost/filesystem_sandbox.go`, migrations `9257`–`9260`, `server/internal/service/employee_filesystem_test.go`: employee storage shared across independently selected execution sandboxes; persistent native session host routing. Filesystem write conflict management is outside this feature.
- `server/internal/handler/dsh_home.go`, `packages/views/agents/components/agent-overview-pane.tsx`: all-FC filesystem provisioning/configuration and representative host status, with DSH settings kept together.



## Native input removal

- `server/cmd/server/main.go`, `router.go`: no historical Host input worker or native prompt admission endpoint.
- `server/internal/service/dsh_access.go`, `server/internal/handler/dsh_native_invoke.go`: invocation checks retained for task-backed schedule admission.
- `docs/plans/2026-09-24-dsh-native-without-platform-task.md`: native-page removal scope and validation evidence; no deployment claim.

- `server/internal/service/fc_e2b_dsh_profile.go`: saved configuration prepares build intents; application occurs at ordinary task startup. No employee Host is started by a Profile worker.
- `packages/core/agents/dsh-home.ts`, `packages/views/agents/components/tabs/dsh-home-tab.tsx`: filesystem preparation only, with no native-page API or navigation.
- `packages/views/agents/components/tabs/dsh-plugins-tab.tsx`: configuration save is confirmed by the persisted desired revision, independently of task/Host execution.
