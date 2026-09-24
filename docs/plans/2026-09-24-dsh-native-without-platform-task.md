# 删除 DSH 原生页面与历史 Host 恢复扫描

状态：代码清理及本地验证完成；用户已授权预发部署，正在发布应用并准备 Runtime 候选验收。

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

- 无生产/预发部署、Runtime 切换、云端资源删除或数据库数据修改。
- 应用全部副本升级后，删除的扫描和授权链路才完全停止。旧副本仍会运行旧循环；不能用本地代码已删除推断线上已停止。
- 新 Runtime 不提供公开原生网关。已有带网关的 supervisor 配置不匹配时，通过现有任务排空/确切沙箱回收流程替换，不直接扫描或重启历史沙箱。
- 历史数据库授权表/迁移保留，运行代码不再签发、读取或用其续期沙箱。
- 依 `fc-runtime-dev-loop`，候选镜像不可变来源、真实 FC 普通任务及新旧版本兼容验收尚未运行。本地通过不代表已完成云端验收。

## 预发发布授权与验证计划

2026-09-24 用户要求部署预发。只提交预发流水线 66；不推进正式。应用所有副本切换后，核对版本、健康、旧原生接口已移除，以及发布后日志无历史 Host gateway-authority / plugin-snapshot 循环。Runtime 使用本分支独立候选流水线，普通 DSH 任务做冷/热启动验收。
