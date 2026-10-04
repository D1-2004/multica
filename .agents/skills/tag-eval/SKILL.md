---
name: tag-eval
description: >
  Run one Tag / EmployeeLoop evaluation round on 预发 against Qwen-Real (Tag 33af235e):
  deploy proof, dws pre-flight, pick cases (cases-v2 G/M/C/P/T, GoldenCase-20, memory),
  drive real DingTalk conversations, grade, analyse failures with Langfuse/SLS.
  Use when asked to "跑一轮 Tag 评测 / 回归 / GoldenCase / cases-v2", "验收 EmployeeLoop", or /tag-eval.
compatibility: Needs dws + dws-env skill, a1, normandy, multica (profile pre-fde), Langfuse creds in ~/.grok/langfuse.env.
---

# Tag eval round

Everything lives in `scripts/employee-e2e/`:
- `README.md` explains the harness.
- `ROLES.md` says who plays whom.
- `TOOLS.md` lists commands and gotchas.
- `CLOSEOUT.md` fixes the fifth-batch capability boundaries and explicit default dry-run list.
- `references/harness-source-map.md` maps each driver/grader capability to its source and evidence limits.
- For continuation work, follow `docs/employee-delivery-workflow.md` and the current run manifest; historical actor/runtime/revision values here are not live facts.

The cases are `cases/v2/*.json` (88 cases, read `cases/v2/SUITE.md`), `cases/golden20.{json,md}` and `cases/memory/`.

## Model split (user rule)

| Work | Model |
| --- | --- |
| Running cases, first-pass grading, Langfuse/SLS evidence analysis | **Sonnet** (subagent) |
| Independent review of a verdict before it is reported | **local Codex** (`codex:rescue` / codex-companion) |
| Root-causing a **confirmed** product failure (code reading, fix design) | **Opus**, only then |

Keep briefs narrow. Do not re-run a case that is already verified. Hand off in 30 lines or fewer.

## 1. Pre-flight

1. Unset all six proxy variables.
2. Prove which code is live:
   - Run `scripts/employee-e2e/ops/verify_deploy.sh <sha> <deploy-start-ISO>`.
   - It must show: the release branch contains the sha, both pods restarted after the deploy, and the fence marker on every replica.
   - Note the pipeline-66 run id.
3. Check dws:
   - `dws_env.py status` must say `environment: prod`. Never `switch pre` or `restore`.
   - Run `python3 scripts/employee-e2e/e2e.py gw prepare --refresh`.
   - As 主角, renew `chat data-auth cross-org --all --grant-type timed --ttl 24h` if it is older than 24 h.
4. Snapshot the agent (`multica --profile pre-fde --workspace-id 5f8b5b73-… agent get 33af235e-…`). Record the model, `event_trigger_enabled` and the instructions hash.
5. Start the restart timeline in the background: `e2e.py env watch --run-id <R>`.

## 2. Pick cases

- `e2e.py v2 dry-run [--capabilities name=on,…]` lists what is runnable. It reports `runnable`, `runnable_partial` (some checks vacuous), `waiting_ops` / `waiting_release`, and `blocked_harness` (P1/P2 harness features).
- Harness switches follow registered driver support, not platform proof. recall/react remain excluded, including their preserved draft functions. Turn on platform/release/ops capabilities only for what is actually true on 预发 now. For example `G_memory=on` after the memory batch is deployed, and `routine_pause=on` after pausing routine e02d1d7b.
- Run one case at a time per conversation. EmployeeLoop reads the scene's recent history.
- Different conversations may run in parallel.

## 3. Run and grade

```bash
python3 scripts/employee-e2e/e2e.py v2 run --run-id <R> --suite G --only G-01,G-03   # grades each case at once
python3 scripts/employee-e2e/e2e.py collect --run-id <R> --no-sls                   # ≥60 s later (Langfuse lag)
python3 scripts/employee-e2e/e2e.py v2 grade --run-id <R> [--baseline <R0>]
```

- `invalid_env` (restart, deploy or wrong gateway in the window) means rerun the case. It is never pass or fail.
- `harness_error` is a harness defect; fix the harness, do not grade the case.
- `needs_review` means the hard checks passed. Judge the case's `semantic` rubric. Write `<R>/judgements.json` entries (`"<case>.a<N>": {verdict, rationale, evidence:[trace ids, message ids]}`).
- `incomplete` is missing required evidence; `partial` is semantic success with vacuous/platform/pending checks or unproven restart windows. Neither counts as a full pass. A judgement cannot override a hard failure or missing evidence.
- Send each verdict to Codex for an independent review before reporting it.

## 4. Analyse failures

For each `fail` / `degraded`:

- Open the attributed `employee_loop` trace (`langfuse_lookup.py trace <job id without dashes> --json`).
- Read the first `employee_model` input: what the model saw (persona, memory block, history, current window). Then read its tool calls and the Host's tool results.
- Use SLS / log tail for delivery, scene and job lines.
- Classify the failure:
  - **product:** the model or the Host was wrong.
  - **platform gap:** for example, un-@ group lines are not delivered.
  - **test design.**
- Only a confirmed product failure goes to Opus for root cause.

## 5. Report

Write the scoreboard (`summary_v2.md`) to the coordinator / user in Chinese. It must include:

- the live pipeline run;
- pass/fail per case with trace ids;
- the reasoning chain for each failure;
- known gaps that started passing. These must be removed from the case's `known_gap` (ratchet).

Send notices to 冬翔 only as 教练, by stable id. Never message real colleagues.
