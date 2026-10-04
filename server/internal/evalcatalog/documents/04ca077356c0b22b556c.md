# FC sandbox and persistent local Daemon compatibility

## The relationship

FC and local execution share the same task execution core, but they are not the same lifecycle.

```text
Multica server
├─ cloud Runtime (aliyun_fc)
│  └─ create/reuse E2B sandbox for one task scope
│     └─ fixed root runner drops to uid 1000
│        └─ multica daemon run-once
│           └─ exact task claim -> shared handleTask/runTask -> exit
└─ local Runtime
   └─ persistent multica daemon on a real device
      ├─ probe local provider CLIs
      ├─ register runtime_mode=local rows
      ├─ workspace discovery + WS wakeup/heartbeat
      ├─ HTTP fallback + batch claim
      ├─ shared handleTask/runTask
      └─ update, reload, GC, token renewal, stop/deregister
```

The FC sandbox does not contain a persistent registered Daemon. Every task receives a short-lived `mdt_` token and starts `Daemon.RunOnce`. `RunOnce` seeds the already-created cloud Runtime in memory, claims the exact task, executes it through the shared task engine, reports completion, and exits. A warm sandbox may reuse its filesystem and provider proxy, but it does not reuse the previous Daemon process.

A real device runs `Daemon.Run`. It authenticates with a human PAT, probes installed providers, registers local Runtime rows, connects WebSocket wakeups, heartbeats, batch-claims tasks, and remains alive. Switching an FC Template never upgrades or reconfigures this process.

This distinction matters because an FC smoke cannot exercise local registration, device identity, workspace discovery, WS/HTTP fallback, runtime drift, auto-update, local-directory work, or shutdown/deregister. Conversely, a local smoke does not exercise E2B template selection, root runner hardening, sandbox token minting, or `run-once` startup.

## Source and artifact identities

Keep these identities separate in every report:

| Identity | Meaning |
| --- | --- |
| Runtime repository commit | Dockerfile/runner/tooling source; determines candidate image tag and display alias suffix |
| Multica commit | Exact `dt-fde-multica` source baked into `/usr/local/bin/multica`; contains Daemon and CLI code |
| E2B Template ID | Immutable FC execution artifact selected by the Multica Runtime row |
| Multica Runtime ID | Control-plane binding used by Agents and tasks |

Do not infer the Multica commit from the Runtime repository commit. The Runtime pipeline must emit both `runtime_commit` and `multica_commit`, and the helper requires both to match caller-supplied 40-character commits.

## Change classification and required gates

| Changed area | FC canary | Persistent local canary | Previous released local Daemon |
| --- | --- | --- | --- |
| Runtime Dockerfile, FC runner, E2B tooling | cold + warm FC task | usually not required | not required |
| `run_once.go`, FC claim flags, sandbox auth | cold + warm FC task | only if shared client/task code also changed | when server contract changed |
| shared `handleTask`, execenv, provider adapters, complete/fail client | required | required | when server contract changed |
| `Daemon.Run`, register, heartbeat, wakeup, batch poll, identity, update, GC, local directory | only if shared code changed | required | required when server changed |
| server `DaemonAuth`, register/heartbeat/claim/task wire | required | required | required |
| server-only additive response field | relevant path | relevant path | required to prove additive compatibility |

If server/API behavior changed, deploy that exact Multica commit to pre-release before running the matrix. Building it into the FC image changes the sandbox client only; it does not deploy the server.

## Rolling compatibility matrix

Daemon releases and server deploys are not atomic. A Daemon change is compatible only after these combinations pass:

1. New pre-release server + previous released local Daemon.
2. New pre-release server + candidate local Daemon.
3. New pre-release server + FC image built from the candidate Multica commit.
4. Old server + new FC image only when the intended release order allows the image to lead the server; otherwise forbid that order instead of testing an unsupported combination.

Wire changes should be additive or protected by capabilities/version gates. WebSocket failure must fall back to HTTP; a candidate cannot require a new response field from every old server replica during a rolling deploy.

## Binary surface comparison

Before a live local smoke, compare the installed released binary with the candidate:

```bash
DAEMON_COMPAT=.agents/skills/fc-runtime-dev-loop/scripts/daemon_compat.py
python3 "$DAEMON_COMPAT" compare \
  --baseline-bin "$(command -v multica)" \
  --candidate-bin /absolute/path/to/candidate-multica
```

The comparison fails when a provider disappears or when a public `daemon start` flag disappears. An explicit allow flag records a reviewed, intentional breaking change; do not add an allow merely to make the test green.

## Persistent local device smoke

Use a temporary built binary, isolated profile, unique daemon ID, unique device name, temporary workspace root, and both `--no-auto-update` and `--no-auto-reload`.

Important safety facts:

- A named profile isolates local config/PID/logs, but `profile.workspace_id` does not limit registration. A human PAT registers the Daemon in every Workspace that user can access. List them before starting and require `--allow-multi-workspace-registration` in any automation when count is greater than one.
- The default daemon identity is machine-level and shared across profiles. Always pass a fresh explicit `--daemon-id`; otherwise a smoke can upsert the user's real device rows.
- Registration also sends legacy daemon IDs. Check that none collides with an existing Runtime before start.
- `dta_` Workspace tokens do not currently authenticate `/api/daemon/*`; use a short-lived PAT belonging only to a dedicated test Workspace when possible.
- Restrict `PATH` to the provider under test. Otherwise every installed provider is registered in every visible Workspace.
- Refuse enabled custom Runtime Profiles for the smoke. Never delete a shared profile-backed Runtime.

Build with auditable version metadata:

```bash
SMOKE_SHA="$(git rev-parse HEAD)"
SMOKE_VERSION="$(git describe --tags --match 'v[0-9]*' --always --dirty)"
(
  cd server
  go build \
    -ldflags "-X main.version=$SMOKE_VERSION -X main.commit=$SMOKE_SHA" \
    -o /tmp/multica-daemon-smoke ./cmd/multica
)
```

Start with a unique identity. If the candidate temporarily lacks the released `--workspaces-root` flag, that is a surface regression; `MULTICA_WORKSPACES_ROOT` can keep diagnosis moving but does not make the compatibility comparison pass.

After start, validate the live ledger:

```bash
python3 "$DAEMON_COMPAT" verify-live \
  --candidate-bin /tmp/multica-daemon-smoke \
  --profile <isolated-profile> \
  --daemon-id <fresh-daemon-id> \
  --expected-cli-version "$SMOKE_VERSION" \
  --expected-provider codex
```

The verifier requires `status=running`, exact daemon/version identity, registration in every PAT-visible Workspace, an exact provider set, server-side `runtime_mode=local,status=online`, WebSocket connection evidence, and heartbeat acknowledgement.

Bind a private temporary Agent to one recorded Runtime and create a nonce-marked Issue. Then verify the actual execution:

```bash
python3 "$DAEMON_COMPAT" verify-task \
  --candidate-bin /tmp/multica-daemon-smoke \
  --profile <isolated-profile> \
  --issue-id <issue-id> \
  --runtime-id <runtime-id> \
  --expected-marker <nonce>
```

This proves task wakeup/batch claim, provider execution, message reporting, and complete callback. A merely queued, dispatched, or running task does not pass. Exact output is the default. If a known Agent/provider wrapper intentionally adds completion prose, use `--marker-mode contains` explicitly and retain that wrapped output as evidence; do not silently weaken the default.

## FC task canary

After create/switch read-back, bind a private temporary Agent to the candidate FC Runtime and create a nonce-marked Issue. Verify one completed task on the exact Runtime with the same `verify-task` command. This is the first point at which the workflow may claim the Multica orchestration loop is complete.

When the change affects sandbox reuse or continuation, submit a second task in the same scope and prove the intended warm/cold behavior from runtime-start evidence. Pass `--task-id <exact-run-id>` to `verify-task` when the scope now has multiple runs. Template build smoke alone only checks the image; it does not perform a Multica claim.

For a switch, record `previous_template_id` before mutation. If the canary fails, switch back to that ID and read it back. For create, keep or delete the failed candidate according to the user's debugging intent; never silently leave a Runtime presented as verified.

## Cleanup

1. Archive the exact temporary Agent.
2. Delete the exact temporary Issue.
3. Stop the isolated Daemon and verify `status=stopped`.
4. Run `verify-stopped`; every recorded Runtime must be offline or absent.
5. Before deleting a Runtime, re-read its ID, daemon ID, `runtime_mode=local`, `profile_id`, and Agent bindings.
6. Use strict Runtime delete only. If it returns 409, stop and report the bound Agent; never cascade an unknown object.
7. Verify all ledger Runtime IDs are absent, the Agent is archived, and the FC candidate Runtime snapshot did not change. If archive retains `runtime_id`/`runtime_bound`, report that immutable audit residue explicitly; do not invent an unbind operation or use cascade.
8. Remove only the exact isolated profile and temporary workspace root. Never clean the whole `~/.multica` tree.

## Verified 2026-09-03 evidence

The candidate built from `dt-fde-multica@003d8242ffc8112a8ef9b504c9cfa492923b3e4c` successfully registered eight local Runtime rows across the PAT user's two pre-release Workspaces, connected WebSocket heartbeats, batch-claimed task `f0f7b9cc-175d-4e02-8e39-a02f5a2ce258`, ran local Codex, returned `LOCAL_DAEMON_COMPAT_OK_003d8242`, and received the complete callback acknowledgement. Stop changed all rows offline; the exact Issue, Runtime rows, profile, and PAT copy were removed. The temporary Agent remains as an archived audit record because the product exposes archive, not hard delete.

The same check also found two compatibility regressions relative to installed Multica 0.4.33: candidate help lacked `--workspaces-root`, and candidate probing omitted `zeroclaw`. Functional task execution passed, but binary-surface compatibility did not; keep those as explicit rollout blockers unless separately reviewed and fixed.

## Verified post-deployment rolling matrix (2026-09-03)

The pre-release server build `096b5c07d5c8e6975c553c4b886f87a91b2a4f8f` contains Skill/Daemon source commit `675e7abcf9703cf5efa50ffdad7c9da2afed51e5`. Aone pipeline `66`, run `3106557446`, completed build, artifact scan, pre-release deployment, and integration test successfully before waiting at its manual pre-release verification gate.

Against that deployed server:

1. Installed Multica `0.4.33` registered two Codex-only local Runtime rows across both PAT-visible Workspaces, connected WebSocket/heartbeat, completed exact-marker task `ffdf878d-356b-4d7f-a711-c0908be5324b`, stopped, and left zero Runtime rows after strict deletion.
2. Candidate Multica `postdeploy-675e7abcf` repeated the same lifecycle and completed exact-marker task `9eb068ee-ec98-4c9c-b40f-99e389c5b968`; stop and strict cleanup also left zero Runtime rows.
3. FC candidate Runtime `f069b307-2bc1-4ab5-92ff-7fdd5bcf2114` completed RunOnce task `326ee13c-1851-4646-80a7-b24267764bf3` with marker `FC_POSTDEPLOY_OK_096B5C07`.

The functional rolling matrix therefore passed on the deployed server. Binary-surface compatibility remains failed because candidate `675e7abcf` still removes `--workspaces-root` and `zeroclaw` relative to 0.4.33. Do not collapse these two results into one green status.

The shared pre-release flow later superseded that deployment with run `3106563734`. Its build commit `9b8fcfa46f18d3bbe39765c8519139f8083d6307` still contains `675e7abcf`; build, artifact scan, deployment, integration test, and the post-deploy health/config probes succeeded before the run waited at the same manual verification gate. No Daemon/server functional revision changed between the smoke and this superseding deployment.
