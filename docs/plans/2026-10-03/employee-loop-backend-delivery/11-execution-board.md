# 执行板：本机环境、分工、编号预留与发布循环

本文件记录 2026-10-03 起主代理（Coordinator）实际执行本计划时采用的事实和约定。它覆盖 Step 0 与 00-context 中写于另一台机器、已经过时的部分。产品验收仍以 [10-delivery-standard.md](10-delivery-standard.md) 为准。

## 1. 协作方式

- 子代理只在自己的 git worktree 里开发，在本地提交到自己的分支，然后交给主代理。
- 主代理负责全部合入：合到唯一的交付分支 `feat/tag-multitenant`，做定向回归，推送 `aone`，触发预发流水线 66，并确认部署结果。
- 部署确认后，主代理通知相关子代理开始真实验证。验证可以串行，也可以并发；并发时每个子代理各用一个独立会话，因为近期对话会被读入上下文。
- 审查与部署并行进行。可选择由 Codex 或 Opus 异步审查；审查发现的问题跟随下一次推送修复，不阻塞预发。

## 2. 本机环境（已实测）

| 项 | 当前事实 |
| --- | --- |
| 远端 | `aone`（code.alibaba-inc.com）与 `origin`（gitlab.alibaba-inc.com）是同一个库。推送一律推 `aone`，走 ssh。 |
| 交付工作树 | `/Users/yuanzhan/d1/dt-fde-employee-delivery`，分支 `employee/backend-delivery`，跟踪 `aone/feat/tag-multitenant`。子代理工作树放在 `/Users/yuanzhan/d1/dt-fde-employee-wt/<包>`。 |
| PG | 共享容器 `multica-postgres-1`（pg17，127.0.0.1:5432），每个工作树一个库。生成 `.env.worktree` 必须用 `/bin/bash`（Homebrew bash 5.3 遇到 heredoc 会卡死）。新库迁移约 7 秒。271 问题已由 9540 别名修复。 |
| Redis | 每个代理使用自己的容器和端口（164xx）。不要连 16379；不要 flush 他人的库。 |
| Go | PATH 上是 1.24.3；在 `server/` 下 GOTOOLCHAIN 会自动切到 1.26.1。 |
| 已知基线失败 | handler 在 `-race` 下失败 `TestEmployeeProviderDeadlineHTTPRetryAndReplay`；handler 全量 80 个失败（另有 3 个 nil-Queries panic 需 skip）；service 4 个；migration lint 有 23 个历史重复前缀。比较时按测试名对比失败集合，不看整体退出码。 |
| GawkBot | `~/github/gawkbot`，固定版本 `71e82a1809565281cbd0bf8185d3c125b715d934`（Sustainable Use License）。 |

## 3. 真实验收环境（L2）

- **测试 Tag**：预发 QwenTag-Pre（`5f8b5b73-…`）里的「Tag · RealNiubility」`33af235e-e03b-4be2-be3b-bbae8b97fce5`，即 Qwen-Real。
  - 模式 employee；租户 `cc357586-…`；org 44675729（RealNiubility）；runtime `b706c1f6`，模板 taa2f9vh。
  - 该 Tag 的镜像、提示词和全部租户配置都可以按测试需要修改。不改其他 agent。
- **场域**：
  - 已有：冬翔 ↔ Qwen-Real 单聊（scene `e961aa28-…`），单聊里有一个每小时例行任务。
  - 由 Q 新建：RealNiubility 专用测试群和 Director 单聊。
- **演员**：
  - 优先用真人号：DingTalk-FDE Director、冬翔（主角号，跨组织）。
  - 其次用 `dws dingtalk-tag manage` 号池里的数字员工演员。DEAP 演员不能 @ 人，也不能和其他数字员工单聊。
  - 单聊用例尽量用真人号。
- **DWS**：一律走线上网关。按进程用私有 `DWS_CONFIG_DIR` 固定网关（`DWS_*_MCP_URL` 环境变量无效）。禁止 `switch pre`，也禁止 `restore`。
- **观测**：
  - Langfuse：environment=pre；employee_loop 的 session 是 scene_id，agent_task 的 session 是 cid，两者都要查。
  - SLS：用 normandy 查，预发标签 `…_default_prehost`。
  - log tail 只用于实时查看，它会集中落在同一个 pod 上。

## 4. 发布与「已部署」判定

1. 合入交付工作树，跑定向回归。
2. `git push aone HEAD:feat/tag-multitenant`。
3. `a1 cd-pipeline run 66 --app 342160 --cr-id 36355253`。CR 失效时按分支查找现有 CR，没有则新建。
4. 判定「已部署」，以下三条同时满足：
   - 发布分支 head 包含交付提交（`merge-base --is-ancestor`）；
   - 两个 pod 在本次预发部署之后都出现 `server starting`；
   - deployment-fence 显示两个副本都带上新的 `[employee-loop:N]`。
5. 预发服务端不上报 SHA（build_id 为 `dev@unknown`）；「预发集成测试」阶段耗时 0 秒，只是空操作。

预发每天约有 28 次部署，来自多个会话。真实用例前后都要记录两个 pod 的启动时间。用例窗口内如果发生重启，该用例判为 `invalid_env` 并重跑，不算通过也不算失败。

## 5. 编号与标记

- **迁移编号**：本交付预留 9900–9989，9990–9999 留给修复。
  - P1 9900–9909，P2 9910–9919，A 9920–9929，B 9930–9939，F 9940–9949，C 9950–9959，D 9960–9969，G 9970–9989。
  - 其他会话请避开这一段。
- **`EmployeeLoopReplicaMarker`**：其他会话也在提升这个标记。
  - 第一波只由 P2 提升。
  - 合入时由主代理对照远端最新值重新编号，并在同一提交里同步更新 router 就绪串和 `docs/employee-loop.md`。
- **读写顺序**：reader 先上线。旧二进制读不懂的新工作（新 job kind、新 queue context、新枚举）只有在全部副本都具备对应标记后才能生成。

## 6. 第一波（2026-10-03 开工）

| 包 | 工作树 / 分支 | 本波范围 |
| --- | --- | --- |
| P1 | `p1-lifecycle` | Task 生命周期 v2：waiting、单一转移入口、CompleteGoal、等待事实、自主轮次计数 |
| P2 | `p2-wake` | job kind、typed Task wake 的受理与读取、人类优先、未知 kind 改为 hold、worker 按 kind 分流、产出门禁 |
| A1 | `a1-collection` | `taskinput` 领域：集合、邀请、输入、ready 意图、答复绑定规则、外发扫描 |
| B | `b-cron` | 场域例行任务的冻结 occurrence 与 reader，run_only 产出；同时定义 AutomationOrigin |
| F1 | `f1-webhook` | Webhook 可信入口：原始字节验签、provider ID 去重、冻结 ReceivedAt |
| C1 | `c1-watchdog` | 活动水位、停滞 episode、确定性提醒文本、执行环境失联的分类 |
| D1 | `d1-resources` | 附件来源核验、文本读取与上限、视觉路径就绪性探查 |
| G1 | `g1-verification` | 验证规格来源、验证记录、持久提炼意图、中文词法召回 |
| Q | `q-harness` | `scripts/employee-e2e`、真实场域、GoldenCase-20 与 BASE-TASK 基线 |

## 7. 待补资源

跨场域收集（COL）需要三位同组织真人参与者。目前只有 Director 和冬翔两人，COL 的真实验收标为 pending，等待补充 RealNiubility 真人账号（需要本人扫码登录）。领域开发不受影响。
