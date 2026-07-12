---
name: aone-deploy
description: Deploy this fork to the Aone pre-release environment, read its runtime logs, change its runtime config, and diagnose a failed deploy. Use when asked to deploy, redeploy, check the deployment, read server logs, set an environment variable, or investigate why pre-release is broken.
metadata:
  version: "1.0.0"
---

# Aone Deploy & Operations

The Aone deployment of this fork (app `dt-fde-multica`, ID `342160`) runs backend,
web, nginx, and a health server in **one container**, built from
`APP-META/docker-config/` and orchestrated by `src/main.sh`.

## Prerequisite: unset the proxy

The shell exports an external HTTP proxy that cannot reach the Alibaba intranet.
Every `a1` command and every `*.alibaba-inc.com` request fails with `Bad Gateway`
or a timeout until it is unset. This looks like an auth/VPN problem but is not.

```bash
unset ALL_PROXY all_proxy HTTP_PROXY http_proxy HTTPS_PROXY https_proxy
```

Do this in each Bash invocation (the shell re-reads the profile every time).

## Pipelines

| Pipeline | Name | Notes |
| --- | --- | --- |
| 65 | 日常 | unused |
| 66 | 预发 | the deployment we operate; web UI shows it as `flowId=1005452` |
| 67 | 正式 | manual only |

Pipeline 66 runs 代码合并 → 构建 → 预发部署 → 预发集成测试, then parks at the
manual **预发验证** gate. Deploying never publishes to production.

## Deploy

Pushing to `develop` does **not** redeploy once the flow instance is parked at
预发验证. Always trigger explicitly after a push:

```bash
a1 app pipeline run --pipeline-id 66            # re-enter; returns newPipelineInstanceId
a1 app pipeline status --pipeline-id 66 --format json | jq -r '.stages[] | "\(.name): \(.status)"'
```

`make deploy` (scripts/aone-deploy.sh) is the fallback when the flow has been
emptied of change requests: it finds or creates a CR on the current branch,
submits it into pipeline 66, and triggers the run.

Poll to completion with a Monitor loop on `预发部署` reaching `SUCCESS`; a
deploy that fails leaves the stage `FAIL` and the pods in a crash loop.

## Read runtime logs

The backend serves its own log tail — no pod shell needed. It requires
`MULTICA_LOG_TAIL_TOKEN` (already set in the pre-release env) as a bearer token,
and is disabled when the token or `MULTICA_LOG_DIR` is unset.

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://pre-fde-workbench.dingtalk.com/api/internal/logs/tail?file=backend&lines=200&contains=dingtalk"
```

- `file` = `backend` | `frontend` | `health` | `bootstrap` (whitelisted; not a path)
- `bootstrap` carries **migration output** and startup orchestration — read this
  first when the app fails to start
- `contains` filters server-side; `lines` caps at 2000

Handler: `server/cmd/server/log_tail.go`, route in `server/cmd/server/router.go`.

## Runtime config (environment variables)

Runtime config lives in the Aone environment trait, is rendered to
`antx.properties` on the host, and is loaded by `src/main.sh` through an explicit
**whitelist**. A key that is not in `RUNTIME_CONFIG_KEYS` in `src/main.sh` will
never reach the process, no matter what the console says.

```bash
# read (env 6721850 = 预发)
a1 env get 6721850 --format json | jq -r '.configurations.envTraits[] | select(.key=="env-vars") | .content.envs[].key'

# write: read-modify-write the whole trait (there is no per-key update; create
# fails with "注入规则已存在" unless the trait is deleted first)
a1 env get 6721850 --format json \
  | jq -c --arg v "$VALUE" '(.configurations.envTraits[] | select(.key=="env-vars") | .content) | .envs += [{"key":"NEW_KEY","value":$v}]' > payload.json
a1 env trait delete --env-id 6721850 --trait-key env-vars
a1 env trait create --env-id 6721850 --trait-key env-vars --version 0.0.1 --form-data "$(cat payload.json)"
```

Config changes take effect only after a redeploy (`a1 app pipeline run`).

Keys that gate product surfaces (easy to get wrong):

- `DINGTALK_CLIENT_ID` / `DINGTALK_CLIENT_SECRET` — DingTalk **login**, and (via
  the credential fallback in `router.go`) the direct capability client that backs
  **directory search**. Requires the `qyapi_addresslist_search` scope on the
  DingTalk app for search to return results.
- `MULTICA_DINGTALK_SECRET_KEY` — a **separate** switch that enables the DingTalk
  **bot / integrations** surface (installations, device-flow install, the 集成 tab).
  A base64-encoded 32-byte key (`openssl rand -base64 32`). Without it the
  integration listing endpoints report `configured: false` and the UI shows
  "not enabled" — the login credentials above do NOT enable this.

## Diagnose a failed deploy

```bash
# 1. which stage/job/task failed
a1 app pipeline stage list --pipeline-id 66 --format json | jq -c '.[] | {stageId, name, status}'
a1 app pipeline stage job list --stage-id <id> --format json | jq -c '.[] | {jobId, name, status}'
a1 app pipeline stage job task list --job-inst-id <id> --format json | jq -c '.[] | {taskId, name, status}'

# 2. platform's startup-failure diagnosis (reads bootstrap.log for you)
a1 app pipeline stage job task status --task-id <deploy-task-id> view-diagnosis

# 3. the deploy pods (host IPs change per deploy)
a1 app deploy-order batch hosts <deploy-order-id> --batch-num=1
```

A failed deploy order stays `DEPLOYING` and **blocks the next deploy**
("有发布单正在运行中,不可退出部署"). Close it, then re-trigger:

```bash
a1 app pipeline stage job task status --task-id <deploy-task-id> finish
a1 app pipeline run --pipeline-id 66
```

`a1` cannot tail pod logs directly — use the log endpoint above instead.

## Database

Pre-release runs PolarDB PostgreSQL 17 (`multica_pre`), reachable from a dev
machine with the proxy unset. Get the URL from the env trait
(`DATABASE_URL`). Migrations run at container start from `docker/entrypoint.sh`
→ `migrate up`, so **a failing migration means the pods never start**.

Before shipping risky migrations, rehearse them against the real pre-release
database inside a transaction that is always rolled back — a local Postgres runs
as superuser and cannot reproduce PolarDB's permission model:

```go
tx, _ := conn.Begin(ctx)
defer tx.Rollback(ctx)          // never commit
tx.Exec(ctx, string(migrationSQL))
```

See the migration rules in CLAUDE.md ("Aone Fork") — especially that PolarDB
refuses `CREATE EXTENSION` to the app role, and that the runner keys applied
migrations on the full filename stem.

## Upstream sync

The fork tracks `multica-ai/multica`. Sync procedure and its hard rules
(migration renumbering, never `git stash` mid-merge) are in CLAUDE.md under
"Aone Fork".
