# Common FC Runtime development cases

Use this page for the selected runtime milestone in the parent SKILL.md.
Required paths below describe operational FC/local acceptance when those
running surfaces are in scope. Source review or build-only delivery does not
create Agents/Issues, switch a Runtime or run real canaries; report those
milestones unverified. Operational completion claims still require all
applicable gates and real task evidence.

Use this table before choosing commands. The main failure mode is applying the right command to the wrong deployment surface.

| Case | Required path | Why |
| --- | --- | --- |
| Runtime Dockerfile/tool version only | Runtime branch pipeline → Template → Runtime configure → FC task canary | Server and local Daemon did not change |
| `dt-fde-multica` Daemon shared task code only | Build FC with exact Multica commit + persistent local smoke | Both `RunOnce` and `Run` reuse shared execution code |
| Local-only Daemon lifecycle code | Build candidate CLI + released/candidate local matrix | FC `RunOnce` does not exercise registration/heartbeat/update/GC |
| Server `/api/daemon/*` or task wire change | Deploy exact server commit to pre-release first, then previous-local + candidate-local + FC matrix | Image build does not deploy the server; rolling skew is expected |
| Runtime and Multica change together | Lock both 40-character commits and require both CI markers | Runtime commit and embedded Daemon commit are different identities |
| First branch build | Add branch-only candidate YAML, push, discover pipeline ID/path, run doctor | Formal master pipeline is never a development shortcut |
| CI failed before Template | Fix the source or transient dependency and build a new commit/run | Runtime mutation remains blocked |
| CI succeeded, PAT/API failed | `inspect-build` with expected branch and both commits, then retry configure | Rebuilding cannot repair authentication |
| Create response lost | Rerun the same name with `--reconcile-only`; never make a normal create retry while outcome is unknown | Reconcile-only performs no POST and refuses zero/multiple/mismatched recovery |
| Switch canary failed | Switch back to reported `previous_template_id`, read back, preserve failed run evidence | A successful PATCH is not workload success |
| Create canary failed | Keep for debugging or explicitly delete the exact candidate | Do not silently call it complete |
| Target Runtime is stable-managed | Create a separate candidate Runtime | Stable rows belong to stable release coordination |
| Template lacks requested provider | Pick a declared provider or rebuild provider manifest | Metadata rewriting cannot add a working runner |
| Multiple PAT Workspaces during local smoke | Prefer a dedicated one-Workspace account; otherwise explicitly allow and ledger every Runtime | Profile workspace selection does not scope Daemon registration |
| Provider disappears between binaries | Fail the surface comparison unless intentional and reviewed | Starting successfully must not hide a local capability regression |
| New CLI field/flag removed | Treat as compatibility break, even if env fallback works | Scripts and managed-device startup rely on the public CLI surface |
| Agent/provider wraps a nonce in completion prose | Keep exact matching as default; rerun verification with explicit `--marker-mode contains` and retain the wrapper output | Task transport may be correct even when behavioral output is intentionally wrapped |
| Same Issue has cold and warm runs | Select each immutable run with `--task-id`; correlate both with sandbox-start evidence | Counting all completed runs cannot identify the warm task |
| Agent archive retains a Runtime binding | Report the archived audit record; if strict Runtime delete returns 409, stop without cascade | Archive is not hard delete or guaranteed unbind |

## Completion vocabulary

Use precise status terms:

- `build_verified`: Aone `SUCCESS`, ACR/Template ready, image-level sandbox smoke passed, and Runtime/Multica commit markers match.
- `runtime_configured`: candidate Runtime create/switch read-back matches cloud/backend/channel/provider/visibility/Template invariants.
- `fc_task_canary_verified`: a Multica task on that exact FC Runtime completed with the expected nonce.
- `local_daemon_verified`: a candidate persistent Daemon registered, heartbeated, completed a task, stopped, and deregistered cleanly.
- `rolling_compatibility_verified`: previous released local Daemon, candidate local Daemon, and candidate FC image all passed against the intended server.

Do not collapse these into a generic `complete=true`. State which gates were required by the change classification and which were actually observed.

## Safe retry rules

- Never submit a second CI run while the first is still pending/running.
- Never mutate a Runtime after a failed or provenance-mismatched run.
- A `switch` may be retried after read-back; it reports the previous Template for rollback.
- A `create` uses its exact unique name as an operation key. Reuse one matching Runtime, fail on multiple matches or mismatched configuration.
- Network uncertainty after create means `--reconcile-only`, not another POST. Zero matches remain unknown until visibility settles or authoritative server evidence proves non-commit.
- Do not use `--cascade` in compatibility-smoke cleanup.
