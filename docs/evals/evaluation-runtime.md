# 评测运行时

## 角色与号池

| 角色 | 职责 | 授予与核验 |
| --- | --- | --- |
| 测试主持人 | 分配剧本、角色、独占群和窗口；推进与收尾 | 验收负责人授权；固定 case manifest 与创建对象清单 |
| 请求者 | 在群内提出工作、纠正、停止与完成标准 | 使用本人真实测试账号；Host 从可信来源冻结主体与授权 |
| 参与者 | 回答明确邀请；可设置迟答、乱序、重复、未授权成员 | 请求者批准联系人白名单；精确 invitation / participant grant |
| 数字员工 | 按已授予能力收取消息、执行并交付 | 当前原生身份、tenant、Runtime、工具与资源读写权限 |
| 独立验证者 | 回读消息、文件、API、日志及模型轨迹 | 单独登记读取范围；不从消息正文取得权限 |
| 环境负责人 | 账号、模型、运行时、存储、发布和隔离故障资源 | 提供专用对象及最小权限，记录恢复点 |

身份由平台/profile 与可信 Host 冻结。角色、实际执行主体和消息渠道分别登记；正文、引用、URL 和展示名不扩权。

## 拉群与场域准备

1. 主持人使用明确 DWS 身份创建专用测试群，并登记创建回执。
2. 加入请求者、获准参与者和数字员工；核验实际群成员与可信 @ 标识。
3. 通过 `scene.Resolve / scene.Lookup` 取得当前 `scene_id`；外部 cid 仅作消息渠道定位。
4. 为隔离案例创建第二个独立群；续接案例保留同一场域。
5. 登记参与配置、能力、常驻 routine、现有未终结任务与测试时间窗。
6. 结束后恢复配置，退出/清理自己创建的对象并回读。

## 环境配置

| 环境 | 最小组成 | 使用范围 |
| --- | --- | --- |
| 本地合同 L0 | 独立工作树、PG 库/schema、必要专用 Redis、fake 模型与渠道 | 确定性协议、权限、事务、并发和恢复 |
| 集成候选 L1 | 目标分支、已迁移隔离 PG、专用 Redis、共同 fixture | 模块接线、组合与故障回归 |
| 真实预发 L2 | 指定 pre endpoint、工作区/员工/租户、实际模型、Runtime、DWS、SLS、LF、号池和独占群 | 群聊理解、真实执行、退出、投递、文件、到点与下一轮记忆 |

Multica 后台预发与 DWS 网关分别固定。DWS 使用进程私有 profile，发前核对组织和执行身份，不切全局配置。实际账号、凭据和资源 ID 只进入本次受控 manifest，不进入定义页。

## 搭建与模拟工具

现有运行入口：`scripts/employee-e2e/e2e.py`。真人用于单聊和必须 @ 的台词；DEAP 演员只按当前能力与租赁范围参加，普通群话、引用回复、@ 和单聊分别核验。号池约束与实际成员以 [ROLES.md](../../scripts/employee-e2e/ROLES.md) 回读，不沿用旧人物配置。

```bash
cd scripts/employee-e2e
python3 e2e.py gw prepare --refresh
python3 e2e.py env watch --run-id <run-id>
python3 e2e.py conv new-group <registry-fresh-template>
python3 e2e.py run cases/golden20.json --run-id <run-id> --only <case-id>
python3 e2e.py collect --run-id <run-id>
python3 e2e.py grade --run-id <run-id> --baseline <baseline-run-id>
```

`run` 的真实执行需主持人固定演员、grant、独占场域、预算和窗口；当前页面不调用这些命令。原始 evidence 写入仓外受控目录，凭据留在 keychain / 私有 profile。运行程序会守卫部署/重启窗口。命令细节：[TOOLS.md](../../scripts/employee-e2e/TOOLS.md) 与 [harness README](../../scripts/employee-e2e/README.md)。

| 阶段 | 工具 | 完成条件 |
| --- | --- | --- |
| 账号/身份 | DWS profile / 多身份演员工具 | 当前组织与账号正确，认证有效，读发范围已授予 |
| 本地栈 | `make worktree-env`、`make migrate-up ENV_FILE=.env.worktree`、专用 Redis | 目标库、端口、迁移与资源归属已核验 |
| 完整应用 | `make setup-worktree`、`make start-worktree` | 本工作树前后端启动并可访问 |
| 群和成员 | DWS 群创建、邀请、成员回读 | 场域独占、成员齐备、可信 @ 有效 |
| 任务和材料 | fixture、业务 API、随机文件工具 | 对象真实存在，标准答案与输入分开 |
| 调度来源 | 测试 routine / 签名 Webhook 工具 | 配置修订、真实时点、可信签名、幂等来源固定 |
| 故障注入 | 隔离 PG/Redis/Runtime/消费者 | 只影响获准专用实例，有恢复点 |
| 验证 | DWS、业务 API、SLS、LF、Runtime transcript、Host checker | 声明证据完整、精确关联、独立回读成立 |

## 模拟运行顺序

固定版本与前置 → 主持人分配角色 → 角色按剧本发送 → 等待对应终态 → 独立回读 → 对照断言 → 下一轮/新 Task 验证 → 清理恢复 → 保存结果。

缺角色或能力记 `blocked / waiting_actor`；缺证记 `incomplete`；部署或重启穿过窗口记 `invalid_env`。定向失败注入不操作共享预发实例。当前页面只展示这些运行定义，不启动真实运行。
