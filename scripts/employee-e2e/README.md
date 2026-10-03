# EmployeeLoop real-IM e2e harness

> Suites: **cases-v2** (`cases/v2/`, 88 cases G/M/C/P/T; source `cases/v2/_build`, readable `cases/v2/SUITE.md`,
> harness requirements `cases/v2/harness-gaps.md`, story `cases/v2/world.json`) and **GoldenCase-20**
> (`cases/golden20.json`, source text `cases/golden20.md`), plus the memory suite (`cases/memory/`).
> Known gaps: `cases/known_gaps.json` (GoldenCase-20), `cases/v2/known_gaps.json` (cases-v2), `cases/memory/known_gaps.json`.
> Who plays whom: `ROLES.md`. Commands and gotchas: `TOOLS.md`. Round procedure: `.agents/skills/tag-eval/SKILL.md`.

Drives real DingTalk conversations with the 预发 test employee **Qwen-Real**
(Tag 33af235e, RealNiubility) and grades them against written criteria.
Python 3.11, stdlib only. No secrets live in this directory.

## Layout

| Path | Role |
| --- | --- |
| `registry.json` | Environment ids, the employee, actors (observer-relative openDingTalkIds), DEAP actor pool, conversations + scene ids |
| `cases/golden20.json` | GoldenCase-20 (11 DM + 9 group chat-behaviour cases) with each case's 判定 |
| `cases/base_task.json` | BASE-TASK smoke (Python wait + compute → progress → continue → thanks) |
| `cases/known_gaps.json` | Known-gap ratchet: a listed case that starts passing must be removed in the same change |
| `el2e/dwsgw.py` | dws wrapper pinned to the **prod** gateway through a private `DWS_CONFIG_DIR` |
| `el2e/im.py` | send with `--uuid` + readback (exactly one landing), transcript reads, reply polling, DEAP noise filters |
| `el2e/envguard.py` | restart-window guard: pipeline 66 timeline (`a1`), per-pod `server starting` (SLS), pre-case gate |
| `el2e/driver.py` | the driver: plays the humans and records raw observations, never grades |
| `el2e/evidence.py` | Langfuse traces (exact attribution), SLS lines, Multica API reads |
| `el2e/leak.py` | user-visible text checks: internal tool/enum names, UUIDs, raw JSON, stack/signal, secrets, sentinels |
| `el2e/grader.py` | the grader: applies 判定 to evidence, writes per-case JSON, run summary and baseline diff |
| `cases/memory/*.json` | Memory suite (M7): MEMX-*, MEM-01..04, BASE-MEMORY, its own `known_gaps.json` (merged by the grader) |
| `el2e/conv.py` | `e2e.py conv new-group <conv>`: a fresh group (new scene) from a registry `fresh` template |
| `tests/test_harness.py` | offline unit tests (`python3 -m unittest discover -s scripts/employee-e2e/tests -v`) |

Evidence goes to `~/d1/employee-e2e-evidence/<run-id>/` (mode 0700, outside the repo and `/tmp`):
`manifest.json`, `env_timeline.jsonl`, `cases/<case>.a<N>.driver.json`, `langfuse/<trace>.json`,
`evidence/<case>.a<N>.lf.json`, `evidence/sls_*.json`, `final_transcripts.json`, `graded/*.json`,
`judgements.json`, `summary.json`, `summary.md`.

## Hard rules

- Unset all six proxy variables for intranet calls (the harness does it for its subprocesses).
- DWS sends go through the **prod** gateway only. The private dir (`~/d1/employee-e2e-evidence/.dws-prod-gw`)
  holds copies of `profiles.json`/`token.json`/`dws-env-roles.json`, symlinks `app.json`/`identity.json`
  and never an `mcp_url`/`terminal_url`, so other sessions flipping `~/.dws/mcp_url` cannot reroute us.
  `DWS_*_MCP_URL` env vars are ignored by dws. Never `dws_env.py switch pre` or `restore`.
  Tokens live in the macOS keychain: `e2e.py gw prepare --refresh` refreshes in `~/.dws` and re-copies.
- Every send carries `--uuid` (derived from the case marker) and is read back; a retry that reports a
  repeated uuid means the first attempt landed. Never read with `--start` (drops messages).
- `createTime` is a local `YYYY-MM-DD HH:MM:SS` string; comparisons keep a 2 s tolerance.
- Our own sends also carry `messageAiSendFlag=DWS`; tell the employee apart by its observer-relative `senderId`.
- One case at a time per conversation. EmployeeLoop reads the recent conversation (20 messages / 16 KiB),
  so interleaving pollutes context. Different conversations may run in parallel.
- 预发 is redeployed ~28×/day. The driver waits at a gate while a pipeline-66 deploy is pending and for
  150 s after it. A case whose window contains a pod restart or deploy is `invalid_env` and is rerun.

## Actors and scenes

- Humans: 冬翔 (`zhujue`, the 主角 profile in the 钉钉 org acting cross-org in RealNiubility; needs
  `chat data-auth cross-org --all --grant-type timed --ttl 24h` to read/send new RealNiubility groups) and
  DingTalk-FDE Director (`director`). Use humans for every 1:1 case.
- DEAP actors (`deap_pool` in the registry): 50 「书名·人物」 digital employees created by Director.
  Get a profile with `dws_env.py as 'Real Niubility/DingTalk-FDE Director' -- dingtalk-tag manage login --agent-uuid <uuid>`,
  then `e2e.py gw prepare`. Check the prod lease board first
  (`multica --profile fde-prod issue metadata list 5598799a-… --output json`, workspace 1ccd28c9-…);
  never connect, publish or edit an actor. Limits: no @, no DE↔DE DM, auto template reply when DM'd,
  join intro/placeholder cards (filtered by the grader).
- **Measured 2026-10-03:** a DEAP actor *can* address Qwen-Real in a group by quote-replying any
  Qwen-Real message (`chat +messages-reply`): the reply renders as `@Qwen-Real …`, arrives as a native
  @ event, and Qwen-Real answers. A plain (non-quote) actor line is never delivered to the employee.
- Conversations: `dm_zhujue` (冬翔's DM, carries the hourly routine e02d1d7b; its posts are filtered),
  `dm_director` (default DM for 1:1 cases), `group_e2e` (EL-E2E-1003: 冬翔 + Director + Qwen-Real + the
  default AI小钉 bot), `group_de_probe` (EL-E2E-1003-DE: Director + Qwen-Real + 红楼·林黛玉).
  Scene ids are discovered from Langfuse by `collect` and written back to the registry.

## Running

```bash
cd scripts/employee-e2e
python3 e2e.py gw prepare --refresh
python3 e2e.py env watch --run-id R1003-baseline &      # restart timeline (keep running)
python3 e2e.py run cases/golden20.json --run-id R1003-baseline --only DS-19,DS-01
python3 e2e.py collect --run-id R1003-baseline           # after ≥60 s (Langfuse ingestion lag)
python3 e2e.py grade --run-id R1003-baseline [--baseline <older-run>]
python3 e2e.py read group_e2e --limit 20                 # ad-hoc transcript
```

## Driver and grader

The driver only acts and observes. The grader works from a fresh final transcript of every
conversation, so late messages (a background task's result) are graded too:

- Employee messages are attributed to a step by `quotedMessage.messageId` first, else to the latest
  step of the case's span in that conversation. Unquoted hourly-routine posts are ignored.
- Langfuse attribution is exact: an `employee_loop` trace belongs to a step when the step's
  openMsgId is in the first model request's current window; an `agent_task` trace belongs to the
  case through `idx.employee_job_id`.
- Hard checks: reply count per step, required/forbidden content, max length, sentinels (whole or split),
  leaks, and evidence checks (model calls per wake, no task effect for a step, one Task with N Runs).
- Semantic rubric items need a judgement in `<run>/judgements.json`
  (`{"DS-07.a1": {"verdict": "fail", "rationale": "...", "evidence": ["trace …", "msg …"]}}`).
  Until then a case that passes the hard checks is `needs_review`, never `pass`.
- Verdicts: `pass`, `fail`, `needs_review`, `invalid_env` (restart/deploy/gateway problem in the window,
  rerun), `harness_error`. `--baseline <run>` adds FIXED / IMPROVED / UNCHANGED / REGRESSED / NEW.

## Sharing the harness

- Other packages keep a private registry (their own groups/scenes): `EL2E_REGISTRY=/path/to/registry.json`.
  Copy `registry.json`, add your conversation, and pass your own `--run-id`. The private DWS dir is shared.
- Scene allocation on 2026-10-03: GoldenCase reruns use `group_e2e`; G1 owns `dm_director` (MEM-01),
  P2 owns `dm_zhujue` for its regression case; B/C1/D1 create their own groups.

## Lessons from the 2026-10-03 baseline

- History pollution is real: EmployeeLoop reads the last 20 turns of the scene. Running several
  GoldenCase DM cases back to back made 「三项候选」 ambiguous (DS-04 a1) and a repeated task text
  produced two identical task candidates (BASE-TASK a2). Randomized codes are not enough for cases
  that rely on "the" previous set; give such cases a fresh conversation (`--conversation`) or vary
  the task content. The grader only judges inside the case span, but the model still sees older turns.
- In groups the native subscription delivers only messages that @ the employee, so un-@ context lines
  never reach EmployeeLoop and the group history contains only @ turns. Silence checks on un-@ lines
  are vacuous under this transport; record that, do not count them as judgement.
- `normandy` (SLS) can fail admission for long stretches; the grader then relies on the pipeline-66
  deploy timeline and `GET /api/internal/logs/tail?contains=server%20starting` (repeat ≥6× to hit both pods).

## Memory suite (M7)

- Clean scenes, no visible markers: each group case family gets a new group (`e2e.py conv new-group group_mem_g2`,
  reset its `cid` to null before the next run); DM cases declare `idle_before_min: 30` so the Host's segmentation
  separates them (`run --max-idle-wait 1900` to wait instead of deferring). Values are randomized per run
  (`{WEEKDAY}`, `{ROOM}`, `{SECRET}` …); chained cases reuse their parent's values with `vars_from`.
- Steps can quote-reply (`reply_to`, how a DEAP actor addresses the employee), send background chatter (`burst`),
  pause (`pause_s`) and clean up after grading (`teardown`). A role mapped to `null` (e.g. a third human) makes the
  case `not_run`.
- Evidence checks read what the model was shown on the wake's first request: `memory_block`, `history`,
  `system_prompt`, `trace_metadata` (memory_manifest, transcript_status …), `packet` (agent_task input: MEMORY
  section, `memory:` context), `named_trace` (employee_verified_distill) and `scene_memory` (management API, scene
  layer only). Sentinels accept several `conversations`; `lang: zh` requires Chinese replies.

## cases-v2 (P0: var_sets, quote-reply, grader v2, DEAP actors)

The 88 cases in `cases/v2/{G,M,C,P,T}.json` (schema `el2e.cases.v2`, background facts in
`cases/v2/world.json`) run through their own driver and grader:

```bash
python3 e2e.py v2 dry-run [--suite G,M] [--capabilities routine_pause=on]   # parse + validate + classify, no send
python3 e2e.py v2 run --run-id R --suite G --only G-01,G-03                 # drive; grades each case at once
python3 e2e.py collect --run-id R --no-sls && python3 e2e.py v2 grade --run-id R   # add Langfuse evidence, regrade
python3 e2e.py v2 sync-gaps                                                 # cases/v2/known_gaps.json from known_gap
```

- **Strict schema** (`el2e/cases_v2.py`): unknown keys at suite, case, step or check level are errors;
  references (since_step, reply_to, check steps, only_if, quotes_step) must point at real steps.
- **var_sets**: one row per attempt, seeded by `run:case:attempt`, merged over the driver codes
  (`driver.code_vars`; a name clash is a load error). Variables render first, then `{=ALIAS}`.
- **Capabilities** (`cases/v2/capabilities.json`, override with `--capabilities name=on`): harness
  switches follow the code; a case needing an unimplemented harness capability is `blocked_harness`;
  release/ops switches that are off keep a case from running; a check whose `requires` is off is `vacuous`.
- **Quote-reply** (`reply_to`): step / employee_reply_of (grader attribution, last reply) / observed /
  employee_latest, with fallback; an unresolved target is `harness_error`, never a plain send. Quoting
  the employee drops an explicit `@employee` (a quote already @-mentions its author).
- **Senders**: humans send with `--ai-tag=false`; every landing is located as a reader sees it by the
  sender's id and can't reuse a message an earlier step claimed (short lines like 「好嘞」 are safe).
  DEAP actors (`daiyu`, `wangxifeng`, `baochai`) never @ or DM, are leased on the DWH board per case
  (`--no-lease` to skip) and are read back by a human reader.
- **Grader v2** (`el2e/grader_v2.py`): statuses pass / fail / vacuous / na / unsupported / pending_evidence;
  `tier: target` misses with all hard checks passing give `degraded`; unmatched non-optional observe
  steps fail; sentinels scan only the case span; an uncovered window is `harness_error`.
