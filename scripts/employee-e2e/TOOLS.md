# Tools for Tag evaluation

Everything an eval round needs, with the exact entry point and the gotchas that have bitten us.

**Always first:** `unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy`. The external proxy 502s every intranet call (a1, normandy, 预发 endpoints, dws). The harness strips the proxy variables for its own subprocesses.

## 1. Messaging: dws CLI + dws-env

| Purpose | Command |
| --- | --- |
| Gateway / account status | `python3 ~/.agents/skills/dws-env/scripts/dws_env.py status` (must show `environment: prod`) |
| Act as an account | `python3 ~/.agents/skills/dws-env/scripts/dws_env.py as <selector> -- <dws args> --format json` (selectors: see ROLES.md) |
| Refresh tokens | `dws_env.py refresh <selector…>` (non-interactive; never `auth login` while a refresh works) |
| Send (user) | `dws chat +messages-send --as user --chat-id <cid> --text <t> --uuid <key> --ai-tag=false --yes` |
| Quote-reply | `dws chat +messages-reply --ref-msg-id <msgId> --group <cid> --content <t> --uuid <key> --ai-tag=false --yes` (1:1: `--open-dingtalk-id <peer>` instead of `--group`) |
| Read | `dws chat +chat-messages --chat-id <cid> --limit 100 [--time "<YYYY-MM-DD HH:MM:SS>"]` |
| Members / ids per viewer | `dws chat +chat-members-list --group <cid>`, `dws contact user search --keyword <name>` |
| Create group / add member / nickname | `dws chat +chat-create --name N --users uid,…`, `dws chat group members add --id <cid> --users uid`, `dws chat group update-nick --conversation-id <cid> --nick N` |
| Cross-org read/send for 主角 | `dws chat data-auth cross-org --all --grant-type timed --ttl 24h --yes` (as 主角, every 24 h) |

Rules and gotchas:

- **Prod gateway only.** Never `dws_env.py switch pre` or `restore`; other sessions flip the global `~/.dws/mcp_url` within seconds.
  - The harness pins prod per process with a private `DWS_CONFIG_DIR` (`~/d1/employee-e2e-evidence/.dws-prod-gw`): copies of profiles/token metadata and no `mcp_url`. `e2e.py gw prepare [--refresh]` rebuilds it.
  - `DWS_*_MCP_URL` environment variables are ignored by dws.
  - A message sent through the pre gateway is answered with 「本地 Agent 当前离线」.
- **Idempotency.** Every send carries `--uuid`. A timeout can still deliver; a retry that reports a repeated uuid means the first send landed.
- **Read back every send.** Exit code 0 is not delivery. Confirm exactly one landing by sender id + text + time. Short lines like 「好嘞」 need the sender id.
- **Never read with `--start`.** It silently drops ~43 % of messages and still says complete. Page back with `--time` and dedupe by messageId.
- **Limits.** 3000 characters per message. Markdown tables are swallowed (send lists). `createTime` is a local string with 1 s resolution, so compare with a 2 s tolerance.
- **Ids are observer-relative.** openDingTalkId differs per viewer, so always use the id the sending or reading account sees (`registry.json` `open_ids`).
- **@ and quotes.** A group @ needs both `--at-open-dingtalk-ids` and a `<@id>` placeholder in `+messages-send`; `+messages-reply` adds the placeholder itself. A quote-reply always @-mentions the quoted author, so never add a second @ for them.

## 2. The harness (`scripts/employee-e2e/e2e.py`)

| Command | What it does |
| --- | --- |
| `gw prepare [--refresh]` | private prod-gateway dws dir (+ token refresh) |
| `env watch --run-id R` | background pipeline-66 / SLS restart timeline (`env_timeline.jsonl`) |
| `env gate --run-id R` | block while a 预发 deploy is pending and 150 s after it |
| `read <conv> [--as A]`, `send <conv> --as A --text T` | ad-hoc transcript / send |
| `conv new-group <conv>` | create a registry group from its `fresh` template |
| `v2 dry-run [--suite G,M] [--capabilities k=on]` | parse, validate (unknown keys are errors), render every var_sets row, classify runnable/blocked; no send |
| `v2 run --run-id R --suite G --only G-01,…` | drive cases-v2 (var_sets, quote-reply, DEAP leases); grades each case immediately |
| `collect --run-id R [--no-sls]` | Langfuse traces attributed by openMsgId / job id; SLS agent lines |
| `v2 grade --run-id R [--baseline R0]` | regrade with evidence; writes `summary_v2.{json,md}` and diff FIXED/IMPROVED/UNCHANGED/REGRESSED |
| `run cases/golden20.json --run-id R` / `grade` | GoldenCase-20 and memory suites (v1 driver/grader) |
| `v2 sync-gaps` | regenerate `cases/v2/known_gaps.json` from the cases' `known_gap` |

- **Grader v2.** Check statuses: pass / fail / vacuous (capability off, or no message to inspect) / na (`only_if`) / unsupported / pending_evidence.
  - Verdicts: pass, fail, degraded (only `tier: target` checks missed), needs_review (semantic rubric pending), invalid_env (restart, deploy or wrong gateway in the window), harness_error, not_run.
  - Semantic verdicts go in `<run>/judgements.json` with evidence.
- **var_sets.** One row per attempt, seeded by `run:case:attempt`, merged over the driver codes. Variables render first, then `{=ALIAS}`.
- **Quote-reply targets.** `reply_to` = step / employee_reply_of / observed / employee_latest (+fallback). An unresolved target is a `harness_error`, never a plain send.
- **Evidence** lives in `~/d1/employee-e2e-evidence/<run-id>/` (mode 0700, outside the repo).
- **Tests:** `python3 -m unittest discover -s scripts/employee-e2e/tests -v` (offline).
- **Suite sources:** edit `cases/v2/_build/*.py`, then `python3 cases/v2/_build/build.py` regenerates G/M/C/P/T.json, world.json, SUITE.md and harness-gaps.md in place.

## 3. Logs: SLS via normandy

```bash
normandy log list --source sls --project dt-fde-multica-sls --logstore application-log \
  --query '__tag__:__user_defined_id__: acni_ag_dt-fde-multica_default_prehost and <agent uuid | job id | "server starting">' \
  --from <ISO> [--to <ISO>] --size 100 [--offset N] -o json
```

- **No SQL on `content`.** Analytics over it silently returns `[]`. Page raw lines 100 at a time, sequentially, and aggregate locally.
- **Pod restarts.** `"server starting"` lines (backend.log) give each pod's go-live time. The two 预发 pods are `dt-fde-multica033008056137.pre.na620` and `dt-fde-multica033060149134.pre.na620`.
- **Admission timeouts.** normandy admission (`n.alibaba-inc.com`) can time out for hours. Fall back to the log tail:
  - Call `GET https://pre-fde-workbench.dingtalk.com/api/internal/logs/tail?file=backend&contains=server%20starting` with `MULTICA_LOG_TAIL_TOKEN` from `a1 env trait get --env-id 6721850 --trait-key env-vars`.
  - Each request hits a random pod, so repeat it at least 6 times.
  - The log is overwritten on every restart. Never print the token.

## 4. Langfuse

- **Skill:** `.agents/skills/inspect-langfuse-trace`, script `python3 .agents/skills/inspect-langfuse-trace/scripts/langfuse_lookup.py`. Credentials are read from `~/.grok/langfuse.env`.
- **Trace id** = employee job id or task id without dashes.
- **Sessions:** `employee_loop` traces use session = scene_id; `agent_task` traces use session = cid. Query both, plus tag `agent-33af235e-…` with `--environment pre`.
- **Reasoning chain:** the first `employee_model` generation's input shows exactly what the model saw (persona, memory block, history, current window). Its output tool calls show what it did.
- **Lag:** ingestion takes 30–60 s, so a trace queried right after a case can 404. Wide windows 504; narrow them.

## 5. Deploy proof (pipeline 66 → 预发)

| Step | Command |
| --- | --- |
| Run status | `a1 cd-pipeline run get --latest --pipeline-id 66 --app 342160 --format json` (or a run id) |
| Wait for a run (background) | `scripts/employee-e2e/ops/wait_p66.sh <runId> <outfile>` |
| Prove a commit is live | `scripts/employee-e2e/ops/verify_deploy.sh <sha> <since-ISO>` |

`verify_deploy.sh` checks three things:

- **Release ancestry:** the latest `releases/*342160*` branch contains the commit (merge-base).
- **Restarts:** both pods logged `server starting` after the deploy.
- **Fence:** `GET /api/internal/deployment-fence` shows every live replica with the expected `[employee-loop:N]` marker.

Gotchas:

- The server reports no SHA (`dev@unknown`).
- Other sessions trigger pipeline 66 about 28 times a day and cancel each other's runs.
- A case that overlaps a deploy is `invalid_env` and is rerun.

## 6. Agent state

```bash
multica --profile pre-fde --workspace-id 5f8b5b73-f912-4879-9a29-b763d103fedf agent get 33af235e-e03b-4be2-be3b-bbae8b97fce5 --output json
```

- **Scene memory:** `GET /api/agents/33af235e-…/scene-memory` with the same profile token and `X-Workspace-ID`.
- **Tenants and scenes:** `GET /api/agents/33af235e-…/tenants[/44675729/groups|persons]`.
- **Always pass `--profile pre-fde`.** The global multica config points at prod.
- **Snapshot before a round.** Record `model`, `coordination_mode`, `event_trigger_enabled` and instructions sha before each round (`evidence.agent_config_snapshot`).
