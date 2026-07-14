---
name: aone-deploy
description: Deploy this fork to the Aone pre-release environment, read runtime logs, safely update the env-vars trait, and diagnose failed builds or deploys. Use when asked to deploy, redeploy, check deployment status, read server logs, add or change environment variables, fix Aquaman YAML or env.value type errors, or investigate why pre-release is broken.
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
预发验证. Always trigger explicitly after a push or config change:

```bash
a1 app pipeline reenter --pipeline-id 66 --format json
a1 app pipeline status --instance-id <newPipelineInstanceId> --format json
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
# List names only (env 6721850 = 预发). Never print values into the transcript.
a1 env get 6721850 --format json | jq -r '.configurations.envTraits[] | select(.key=="env-vars") | .content.envs[].key'
```

### Mandatory serialization rule

Aone interpolates each trait `envs[].value` into StatefulSet YAML without
reliably forcing a string scalar. Raw multiline text can break YAML, while raw
values beginning with `[`, `{`, or `-` can become arrays or objects. Kubernetes
requires `containers[].env[].value` to be a string.

For every key introduced or changed in one operation, store the intended runtime
value as a **JSON string literal**:

```text
traitValue = JSON.stringify(runtimeValue)
```

Examples (non-secret):

```text
runtime: https://api.github.com
trait:   "https://api.github.com"

runtime: [{"key":"factory-default"}]
trait:   "[{\"key\":\"factory-default\"}]"
```

For PEM data, first make it one line with literal `\n` separators, then apply
`JSON.stringify`. The server accepts literal `\n` in `GITHUB_APP_PRIVATE_KEY`.
Do not use raw PEM lines, raw JSON arrays/objects, or YAML lists as trait values.

Apply this rule uniformly to all keys in the current change. Do not rewrite
unrelated legacy keys merely to normalize them.

### Safe read-modify-write

There is no per-key update. `create` fails with `注入规则已存在` until the old
trait is deleted, so treat the operation as a guarded replacement:

1. Read with `a1 env trait get --env-id 6721850 --trait-key env-vars --format json`.
2. Parse the response's `formData` JSON string in memory. Preserve every entry,
   update only the intended keys, reject duplicate keys, and JSON-stringify each
   new runtime value.
3. Retain the original `formData` in memory for rollback. Never print values,
   put them in a shell command shown to the user, or write them into the repo.
4. Delete `env-vars`, then recreate it with the original version and modified
   `formData`.
5. If recreation fails, immediately recreate the original trait.
6. Re-read and verify key count, uniqueness, target presence, and that each
   changed value parses as a JSON string. Output names and validation results
   only, never values.

Prefer an in-process orchestration script that passes the generated form data to
`a1` dynamically; this keeps secrets out of tool-call text and terminal output.

Config is snapshotted during the pipeline's 配置项合并 stage. After changing the
trait, always start a new `reenter`; an already-running instance will keep its old
snapshot.

### Recognize serialization failures

- `while scanning a simple key` / `could not find expected ':'`: raw multiline
  content, usually PEM, broke YAML.
- `Expected a string but was BEGIN_ARRAY ... env[N].value`: a raw JSON/YAML list
  was parsed as an array.
- `Expected a string but was BEGIN_OBJECT ... env[N].value`: a raw object was
  parsed as an object.

The Aone build component can display `FAIL` even when `componentData.jobs[].status`
is `SUCCESS`; in that case inspect the attached Aquaman environment error. It is
a post-build spec check, not a source build failure.

Config changes take effect only after a successful redeploy. Verify 代码合并,
构建, 制品扫描, 预发部署, and 预发集成测试 separately; `预发验证` remains a
manual gate.

Secrets frequently appear in Aquaman's expanded StatefulSet error text. Extract
only the error class and JSON path. If a full error was pasted into chat, advise
rotating every exposed credential. Base64 is transport encoding, not encryption;
use KeyCenter when the environment adopts managed secret injection.

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
