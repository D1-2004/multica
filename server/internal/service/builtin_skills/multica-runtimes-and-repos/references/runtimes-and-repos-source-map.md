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

- `server/pkg/protocol/dsh_native.go` validates and clones official native prompt content with bounded transport space; `dsh_native_test.go` covers attachment-only input, invalid unions, nulls and content preservation.
- `server/internal/handler/dsh_native_claim.go` checks task-owned input, native binding and daemon capability before delivery. `daemon.go` requeues refused claims and excludes native attachments from generic callback text. Pure claim tests cover old daemons, changed scope, binding failures and unchanged ordinary inputs.
- `server/internal/daemon/dsh_native.go` and `server/pkg/agent/dsh_native.go` preserve native Session spelling and deliver the complete typed request to the authenticated Host control socket. Protocol-peer tests inspect actual serialized control requests without starting a local Host.
- `server/internal/service/dsh_native_chat_test.go` includes full-input replay comparison and an opt-in real PostgreSQL persistence/busy-steer rollback case. Active steering, browser forwarding and preproduction acceptance are still pending.


- `server/internal/dshhost/session.go` and migration `9238_dsh_browser_session_identity`: preserve official UUID and platform-prefixed Session identities; transactional native adoption refuses remapping and duplicate request ownership.
- `server/internal/service/dsh_native_chat.go`, `task.go`, `dsh_native_chat_test.go`: native admission shares the direct-chat transaction, rechecks human invocation and exact live grant/Host, binds immutable input identity and returns an existing task on an identical retry. Includes opt-in PostgreSQL concurrent replay, input rollback, lost-commit-response and revoked/stale-grant cases; these require real preproduction execution.
- `server/internal/service/dsh_native_session.go`, `dsh_native_session_test.go`: transactionally registers a human-owned chat and exact native mapping under Runtime/employee/workspace locks, resolves concurrent retries and rejects another creator or an issue scope. Registration is separate from input admission and never fabricates a task. Database concurrency and rollback cases require real preproduction execution.
- `server/internal/handler/dsh_native_prompt.go`, `dsh_native_prompt_test.go`, `server/cmd/server/router.go`: capability-authenticated `POST /api/dsh-native/prompts`, strict input boundary, current invocation checks, registration plus admission, safe retry receipts and replay-aware event publication. Pure boundary tests run with `MULTICA_HANDLER_UNIT_TESTS_ONLY=1`; real browser acceptance is still pending; Gateway v3 calls the same submission method through the separate input lane.

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

- `server/internal/service/fc_e2b_dsh_host.go`: shares cold startup, recovery, Runtime/employee locking and task/native-grant drain between human entry and platform launches; injects the deployment-owned gateway authority, sandbox origin and sandbox ID into the employee supervisor. Gateway readiness remains separate from native prompt/task admission.

- `server/internal/service/fc_e2b.go` / `fc_e2b_dsh_host.go`: mandatory DSH employee admission lock and real Home/Host receipt verification, independent of provider-derived catalog capability labels.

- `server/internal/service/fc_e2b_dsh_authority.go`: backend-initiated authorization polling, deployment signing, exact-Host decisions and bounded connection lifetime; no writer lifecycle mutations.

- `packages/core/api/dsh-native-schema.ts`, `packages/core/agents/dsh-home.ts`, `packages/views/agents/components/tabs/dsh-home-tab.tsx`: scoped human native-entry request, credential-safe parsing, explicit Prepare/Enter flow and ephemeral component-owned entry URL.

## FC stable provider scope

- `server/cmd/multica/cmd_runtime_stable.go`: explicit `--backend aliyun_fc --provider dsh` channel, Runtime, history and release operations; release-ID lifecycle actions preserve persisted scope.
- `server/internal/handler/runtime_fc_e2b_stable.go`: validates query/body `provider_scope`; history filters before limiting results.
- `server/internal/service/fc_e2b_stable_scope.go`, `fc_e2b_stable.go`: independent pointer initialization under the backend lock, inherited shared lookup, actual artifact capabilities separated from release targets, late-join filtering and scoped publication/rollback.
- `server/internal/handler/runtime_fc_e2b.go`: new stable Runtime resolves the selected provider pointer under the creation/publication lock, preserving the shared template's default provider when omitted.
- `server/migrations/9239_fc_stable_provider_scope.*.sql`: persisted scope and old-worker target/publication/creation guards; downgrade refuses to discard independent channel history.
- `server/internal/service/fc_e2b_stable_scope_test.go`: target/capability separation, late joins, explicit empty selection and idempotency scope.
- `server/internal/service/fc_e2b_stable_scope_database_test.go`: opt-in real preproduction test, temporary tables and transaction rollback against installed migration functions. Requires `DSH_STABLE_SCOPE_TEST_DATABASE_URL`; never runs local database services.
- `packages/core/api/client.ts`, `schemas.ts`, `stable-provider-scope-schema.test.ts`: optional provider query, release scope parsing and malformed/historical response checks. Existing shared-channel UI does not provide scoped publishing controls.

- `server/internal/service/fc_e2b_dsh_input.go` signs correlated input receipts after current grant authorization and the shared handler admission callback. `fc_e2b_dsh_authority.go` starts independent input and access workers, bounds input polls and exposes no signing key to FC. `fc_e2b_dsh_input_test.go` covers refusal, replay, unknown outcomes, signature domains and a blocked input with concurrent successful access checks. Gateway readiness requires version 3; real preproduction/native acceptance remains pending.

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

- `server/internal/dshprofile/profile.go` and `postgres.go`: configuration-sensitive immutable revisions, credential-independent build keys, parent/publication locks, queued build intents and exact generation/configuration receipt fencing. Public status excludes private descriptors.
- `server/migrations/9241_dsh_employee_profile.*.sql` through `9244_dsh_plugin_build_identity.*.sql`: durable Profile/build state and separately created concurrent unique indexes. Workspace teardown removes these rows in the parent transaction.
- `server/internal/handler/dsh_profile.go`, `server/internal/service/fc_e2b_dsh_profile.go`: human manager-only status/preparation, transaction-bound effective employee settings and shared Runtime/employee lock order. No endpoint fabricates a Host receipt or starts a build/Host on read.
- `packages/core/api/dsh-profile-schema.ts` and `dsh-profile-client.test.ts`: exact string revisions, workspace-bound requests and malformed receipt rejection.
- `server/internal/dshprofile/postgres_test.go`: opt-in isolated-schema preproduction tests for cross-replica publication, pending builds, replay timestamps, config edits, changed revisions/generations and retiring Hosts. Local runs skip the database test.
- `server/internal/dshprofile/delivery.go`, `server/internal/service/fc_e2b_dsh_delivery.go`, `fc_e2b_dsh_host.go`: immutable publication metadata, scoped artifact download, bounded Profile transfer into a private file, shared native/task admission and live Host receipt acknowledgement. Configuration bytes never enter build or artifact-install requests.
- `server/internal/service/fc_e2b_dsh_delivery_test.go`, `fc_e2b_dsh_host_test.go`: artifact identity/receipt rejection, large Profile transport, actual revision matching and opt-in multi-pool Host/revision tests. Local tests do not certify employee Home or live application.
- `server/internal/dshprofile/build_worker.go`, `build_postgres.go`, `build_artifact.go`: durable claimed execution phases, persist-before-create, unknown-create reconciliation, scoped immutable transfer receipts, object readback verification and confirmed cleanup.
- `server/internal/service/fc_e2b_dsh_build.go`, `server/internal/dshhost/fc_build.go`, `server/cmd/server/main.go`: capability-gated FC build driver, exact persisted sandbox identity, fixed idempotent helper commands and shared-ledger background reconciliation. No employee mounts, role or configuration in build sandboxes.
- `server/internal/storage/s3.go` and `s3_test.go`: bounded PUT presigning against the public object endpoint without an empty-body checksum; signed URLs remain transient.
- `server/internal/service/fc_e2b_dsh_build_test.go` and `server/internal/dshhost/fc_build_test.go`: scoped transfer grants, identity receipts, CLI error redaction, duplicate-create discovery and confirmed cleanup boundaries. These unit tests do not prove real FC/OSS execution.
- `server/migrations/9245_dsh_plugin_build_worker.*.sql` and `9246_dsh_plugin_build_due.*.sql`: additive execution metadata and a separate concurrent due-work index; old Profile readers remain compatible.
- `server/internal/handler/workspace.go` and `GuardBuildDeletion`: workspace-parent then build-row locking prevents deleting the recovery ledger while cloud execution is active or unresolved.
- `server/internal/dshprofile/build_worker_test.go`: simulated lost create/SQL receipts, stale claims, scope changes, malformed artifact receipts, object byte mismatch and cleanup failures. `build_postgres_test.go` uses two real preproduction pools for concurrent claims and deletion fencing; local runs skip it.
