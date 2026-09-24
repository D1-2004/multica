# 删除 DSH 原生页面与历史 Host 恢复扫描

状态：预发应用部署、10 分钟日志观察和新 Runtime 冷/热任务验收完成；未发布正式，未切换共享 stable Runtime。

## 用户决定

- 禁止遍历历史 running Host 恢复 DSH 输入通道。
- 删除原生页面创建平台 Task 的链路。
- 凭证与普通任务一致，不特殊处理。
- 最终明确：原生页面入口和功能直接去掉。

## 基线与范围

- 应用：`origin/deploy/pre` 的 `89356d36b`。
- Runtime：内网 `master` 的 `4437be197862d20913f9bc387f8cd5e37237aa71`。
- 两仓独立分支 `codex/dsh-native-without-task-20260924`，Runtime worktree 与应用同级；共享分支未修改。

## 实施结果

1. 删除历史 Host 输入恢复 worker、探测退避、原生 prompt 接入 API、原生 chat 注册/Task 事务适配。
2. 删除原生页面前端入口、API/hook/schema，后端浏览器访问凭证、代理、会话列表/路由/投影、页面重启。
3. 删除 Runtime 33124 公开网关、反向授权和输入协议、Session 输入包装器、原生插件快照与页面重启接口。
4. 删除 Profile 自动启动 worker 及其历史 Host 选择入口。工作台保存配置准备依赖构建，下一次普通任务启动时应用配置；前端保存不再等待员工沙箱启动。
5. 删除浏览器授权对沙箱回收和续期的阻塞。任务排空、代次校验、确认销毁、持久化存储和定时任务保持。
6. 任务执行仍使用 loopback 私有控制服务，模型/工具/子 Agent/schedule 保留普通 Task 凭证。
7. 更新当前文档、内置技能及 source map；历史迁移/历史计划保留。旧浏览器状态枚举保留只读解析兼容，不存在对应功能入口。

## 本地验证

- Go 服务构建通过：`go build -o /tmp/multica-dsh-without-native-page-server ./cmd/server`。
- service/handler/agent/daemon 的 `TestDSH|TestSandboxRelease|TestSendDirectChat|TestDirectChat` 定向检查通过；handler 使用 `MULTICA_HANDLER_UNIT_TESTS_ONLY=1`。
- `go test ./internal/dshhost ./internal/dshprofile ./internal/dshschedule` 通过；需显式真实数据库的测试仍跳过。
- 修正既有 schedule claim 测试模拟行遗漏 epoch 的问题，使与实际查询的 14 列一致；未改变 schedule 查询行为。
- core/views TypeScript 检查通过。
- 存储页与插件配置共 15 个前端测试通过：无原生入口、显式存储准备、未知写入结果不自动重试、保存配置不等待 Host 应用。
- Runtime Node 的 task-context/prompt-admission/employee-profile-plan/managed-profile 共 12 项通过；依镜像构建流程给锁定依赖应用已有 prompt-admission 补丁。
- Runtime Python 3.11 的 `multica_dsh*test.py` 共 48 项，45 通过，3 个真实 Host opt-in 跳过。
- 本地依赖安装使用锁文件且关闭 install scripts；没有启动本地应用或数据库。

## 部署与未验收项

- 本轮只部署预发应用，并创建私有候选 Runtime 和临时验收员工/Issue；未发布正式、未切换共享 stable Runtime、未修改历史数据库 Host 记录。
- 应用全部副本升级后，删除的扫描和授权链路才完全停止。旧副本仍会运行旧循环；不能用本地代码已删除推断线上已停止。
- 新 Runtime 不提供公开原生网关。已有带网关的 supervisor 配置不匹配时，通过现有任务排空/确切沙箱回收流程替换，不直接扫描或重启历史沙箱。
- 历史数据库授权表/迁移保留，运行代码不再签发、读取或用其续期沙箱。
- `fc-runtime-dev-loop` 候选镜像来源、真实 FC 普通任务冷/热验收已完成（见下）。本次未修改 persistent Daemon 生命周期或普通任务 wire，未另启本地设备。新旧镜像滚动组合不等同于全部正式验收。

## 预发发布授权与验证计划

2026-09-24 用户要求部署预发。只提交预发流水线 66；不推进正式。应用所有副本切换后，核对版本、健康、旧原生接口已移除，以及发布后日志无历史 Host gateway-authority / plugin-snapshot 循环。Runtime 使用本分支独立候选流水线，普通 DSH 任务做冷/热启动验收。

## 预发部署与验收结果（2026-09-24）

### 应用

- CR `36313412`：[变更单](https://cd.aone.alibaba-inc.com/unite/micro/cr/app/342160/36313412)。
- Pipeline `66`，Run `3109784459`：[发布](https://cd.aone.alibaba-inc.com/unite/micro/publish/app/342160?flowId=1005452)。代码合并、构建、制品扫描、预发部署和集成测试均成功；2026-09-24 16:48:41 +0800 完成部署，停留人工预发验证，不推进正式。
- 应用代码提交 `6ad422ec2464f36421a8214a051e1413f83be025`；实际构建/发布合并提交 `2f63bb428a76a9a66b5e8a8c56ee3c1901ad40c0`，已核对包含前者。
- 两个预发副本 `33.8.56.137`、`33.60.149.134` 均 Ready，实际 imageID 同为 `sha256:6bff6bf1f1012c2997afb9d74c309d429b36cf5ee077f9328e47525169a0c421`，不是只检查期望镜像字段。
- `/health` 200；旧原生页面、`/api/dsh-native/access/check`、`/api/dsh-native/prompts` 均 404。

### 扫描对照

Normandy SLS：Project `dt-fde-multica-sls` / Logstore `application-log`，限定预发标签 `acni_ag_dt-fde-multica_default_prehost`。

- 发布前 16:30–16:40：`DSH background session input connection is not ready` 共 **159** 条。
- 发布后 16:49–16:59：正常日志 **11,436** 条；后台输入探测、`gateway-authority`、`plugin-snapshot` 相关日志合计 **0** 条。
- 结论限于被删除的历史 Host 恢复/原生页面链路。真实任务启动、沙箱回收和排队依赖构建仍按需调用 E2B，不能把“扫描停止”描述成“所有 E2B 调用为零”。

### Runtime 与普通任务

- Pipeline `313082`，Run `75805733`：[候选构建](https://code.alibaba-inc.com/dingtalk-ai-lab/multica-fc-hermes-runtime/ci/jobs?pipelineId=313082&pipelineRunId=75805733&createType=yaml)。状态 SUCCESS，真实沙箱 smoke exit 0。
- Runtime commit `3df10d4cd5aada0645827490a4a729b3728cf1cb`；Multica commit `6ad422ec2464f36421a8214a051e1413f83be025`。
- Template `9466pj91the6stguprm2`，alias `multica-m7-va2eb67817f146ef4-r1-3df10d`，provider fingerprint `a2eb67817f146ef4`。
- 私有 candidate Runtime `ed96f586-12b1-4b36-849e-e9f599419413`；读回 online、dsh、aliyun_fc、candidate、ready 与 exact Template 均匹配。未替换任何共享 stable Runtime。
- 冷任务 `606bfea3-d0dc-4f1a-88ea-2a4816941135`：16:58:34–16:59:41，completed。
- 热任务 `db05afe7-cd5d-43b0-a88d-c26941d80891`：17:00:32–17:01:26，completed。
- 两次均使用沙箱 `sbx-02d122f5-972c-42b2-a2b8-fc17569b1c1b`，精确返回 `DSH_NO_NATIVE_PAGE_OK_3DF10D4`。手动 rerun 创建独立原生 Session，沙箱复用成功。
- 两份加密存储轨迹读回并校验 SHA256；实际 tool/call 与 tool/result 证明 Python 检查 127.0.0.1:33124 未监听，exit code 0，非仅依据模型自述。
- 临时员工 `22783ad6-aaf0-4a54-9cf4-bff1c076dd2c` 已归档，临时 Issue `1f4a117d-021e-41e4-abeb-e375cccd93ff` 已删除并读回 404。候选 Runtime 保留为已验证产物；员工归档绑定作为审计记录保留，沙箱走既有任务结束 idle 生命周期。

### 构建过程记录

- 首个候选运行 `75801222` 因候选 YAML 文件名不符合 helper 校验约定主动取消，未做 Runtime 切换。
- `75802402` 构建/smoke 成功，但缺少 `multica_commit` 回执，未作为最终验收产物；补齐独立来源输出后，以 `75805733` 完成来源验证。

### 共享预发后续发布核对

17:02:09 另一项共享存储权限修复触发 Run `3109789327`，并非本次验收文档提交触发。其构建提交 `8acbe6a0b83ed527be6a2301b18bd8eed91fd0bb` 已核对完整包含本次代码 `6ad422ec2464f36421a8214a051e1413f83be025`。17:10:05 部署和集成测试成功，停留预发验证。最终健康接口仍为 200，已删除的原生授权/提交接口仍为 404；没有操作或退出其它 CR。
