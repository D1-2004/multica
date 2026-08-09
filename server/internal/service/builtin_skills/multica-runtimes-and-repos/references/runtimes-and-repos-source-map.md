# Runtimes and repos source map

- `server/cmd/multica/cmd_runtime.go` registers `runtime list`, `usage`, `activity`, `update`, and `delete`.
- `server/cmd/multica/cmd_daemon.go` refuses daemon start/restart/stop from a daemon-managed task and authenticates local shutdown with a per-instance control capability.
- `runtime list` reads `/api/runtimes` and prints `id`, `name`, `runtime_mode`, `provider`, `status`, and `last_seen_at`.
- `runtime update` posts to `/api/runtimes/{runtime-id}/update`; with `--wait` it polls update status.
- `runtime delete` deletes `/api/runtimes/{runtime-id}`; with `--cascade`, it first reads the `runtime_has_active_agents` conflict payload and posts those ids to `/api/runtimes/{runtime-id}/archive-agents-and-delete`.
- `server/cmd/multica/cmd_repo.go` registers `repo checkout <url> [--ref]`.
- `repo checkout` requires `MULTICA_DAEMON_PORT` plus a task-scoped `MULTICA_TOKEN`, sends that token as a loopback bearer with `workspace_id`, `workdir`, `ref`, `agent_name`, and `task_id`, then prints the checked-out path.
- `server/internal/daemon/health.go` accepts a checkout capability only while its task is active, binds it to that task/workspace/workdir/repo set, and resolves the checkout ref: request `ref` wins; otherwise it asks `server/internal/daemon/daemon.go` for the current task's project repo default ref.
- `server/cmd/server/router.go` registers daemon APIs under `/api/daemon`, including workspace repos and task claim.
- `server/internal/daemon/daemon.go` claims tasks, prepares workdirs, launches provider CLIs, and reports completion.
- `server/internal/daemon/execenv/runtime_config.go` injects task/project/repo context into agent workdirs.
