---
name: scene-memory-e2e
description: >
  跑 Coordinator Scene Memory 预发 e2e：读文档里的剧本，用 dws-env 在冬翔/测试号/dxxh
  之间发消息，用 SLS 看下一轮 Coordinator 上下文有没有召回。用户说「场域记忆 e2e」
  「跑剧本」「验证 scene memory」或 /scene-memory-e2e 时必须用。
compatibility: Requires dws-env, dws CLI on 预发, logged-in a1 and normandy.
---

# Scene Memory e2e

剧本和验证标准只在：

`docs/plans/2026-09-02-coordinator-scene-memory-e2e.md`

不要在本 skill 里复制谁发给谁。Daemon 可选召回仍见 [references/daemon-recall.md](references/daemon-recall.md)。

## 怎么跑

1. 读文档，按切片选剧本。验证对象永远是 **下一轮** 的 SLS，不是本轮 IM 回复。
2. dws 保持预发。`python3 "$HOME/.agents/skills/dws-env/scripts/dws_env.py" status`
3. 发消息一律 `as 主角|测试号|配角`。冬翔→测试号用 cid `cid+bEFv7ngm9n79Q1vL9HYJw==`，禁止 `+dm --to 东翔测试号`。
4. `sendStatus=SUCCESS` 后等 Flush（需要记忆的剧本），再发文档里的「下一轮」。
5. 按文档「观察面」把钉钉回读、SLS、预发库、log tail、Issue/assoc、Router（仅事项）都走一遍。召回只认下一轮 SLS `user_prompt`。
6. 预发部署 SUCCESS 即可跑。流水线走 `aone-deploy`。不用菲迪。

SLS 查法细节走 `inspect-coordinator-sls`。沙箱走 `inspect-fde-llm-trace`，不能顶 Coordinator 上下文。
