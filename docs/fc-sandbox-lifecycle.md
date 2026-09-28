# FC/E2B 沙箱寿命

FC/E2B 沙箱获取逻辑位于 `server/internal/service/fc_e2b.go` 的
`resolveSandboxOnConnection`，续期和释放接口位于 `fc_e2b_timeout.go`。
任务执行命令只会在沙箱准备及续期成功后提交。

## 为什么寿命会杀掉还在干活的格子

沙箱创建带 `--lifecycle.ontimeout kill`。到期时云平台直接杀掉整台沙箱，
里面的 Coding Agent、`nohup` 后台进程、未上传的产物一起消失。Issue 上
没有退出事件，看起来像任务正常结束。

寿命有两处曾经对不齐：

1. **创建**用 `runtime.fc_e2b.timeout_seconds`（或环境默认）。
2. **每次任务启动续期**曾写死 `POST /timeout {"timeout":3600}`。

续期是覆盖剩余时间，不是累加。所以即使把 Diamond 的
`timeout_seconds` 调大，下一次任务启动仍会把寿命打回 3600 秒。
运行中不会再续。单次任务只要超过这次续期后的到期时间，就会被 kill。

DWH 一格开发（取源 + OpenCode 30–60 分钟 + 提 PR + 打包）经常接近或超过
一小时；前台把任务撑住也挡不住 3600 秒这面墙。

当前创建和续期走同一套寿命：默认 **4800 秒**，配置值只有更高时才采用。
低于 4800 的 Diamond 旧值会被抬到 4800，避免静默回到一小时墙。

任务结束时 `daemon run-once` 退出，但它只杀 agent 自己的进程组；agent
工具起的命令在新的进程组或会话里，`nohup` / `setsid` 的进程也一样，会一直
活到沙箱被释放或过期，Issue / 聊天 scope 复用时还会带进下一轮任务。取消的
任务由服务端主动结束这些进程，见下文「取消后结束沙箱内进程」；完成和失败的
任务不结束进程，沙箱按「任务结束后的释放」处理。后台作业语义尚未提供。

## 取消后结束沙箱内进程

PRI-52 实测：从界面取消 run 后，SDK / CLI 两条路径都写回 `cancelled` 并在
30 秒后 `idle_trimmed`，但沙箱内的 Python 和 `sleep` 在取消后 80 多秒仍在运行，
CLI 样本的 Python 已被 PID 1 接管。

实测的进程树：`envd` → runner 包装（`multica-fc-runner --runtime-id … --health-port …`）
→ `multica daemon run-once`（同样的参数）→ `pi` → 工具命令。runner、daemon、pi
和 envd 同在会话 / 进程组 1；工具命令各自新开会话，退出后留下的孤儿由 PID 1 接管。
FC 沙箱里 root 没有 `CAP_SYS_PTRACE`，直接读其他用户进程的 `/proc/<pid>/environ`
会被拒绝；用 `setpriv --reuid/--regid --clear-groups` 切到该进程的 uid 后可以读。
`stat` 和 `cmdline` 任何人都可读。

任务一进入 `cancelled`，`TaskRuntimeTerminalObserver` 立即（与下面的释放并行，
不等 30 秒宽限）对该任务每个 start attempt 用过的沙箱执行一次
`fc_e2b_task_stop.go` 的脚本，10 秒后再执行一次，补上期间新 fork 或被 daemon
拆树后成为孤儿的进程。这一步和 runner 注入 `FC_E2B_TASK_ID` 都受
`runtime.fc_e2b_sdk_rollout` 控制：只对开关选中的 workspace / agent / runtime 执行，
随 SDK 传输一起生效；没选中的任务保持改造前的行为（不注入标记、不结束进程，交给沙箱释放）。
一次取消清理在排期时（任务写成 `cancelled` 的那一刻）冻结一份运行配置快照，两遍都按它判定、选传输：
两遍之间热关，已选中的这次清理照样执行第二遍；两遍之间热开，之前没选中的这次取消也不补跑第二遍。
热切换只影响之后新发生的取消和新的启动。
脚本以 root 运行，参数是 runtime id、本任务健康端口（由 task id 算出）和 task id。
只结束能证明属于本任务的进程；证明不了的一律留下，不按启动时间或父进程猜。

- **能证明属于本任务**：
  - runner：命令行同时带 `--runtime-id <runtime>` 和 `--health-port <本任务端口>`；
    环境里写着别的任务 id 时不算（端口碰撞）。
  - 任务标记：环境里有 `MULTICA_TASK_ID=<task>` 或 `FC_E2B_TASK_ID=<task>`，
    按进程自己的 uid 读取。runner 启动时两个都注入；daemon 给 agent 重新写入
    `MULTICA_TASK_ID`，`FC_E2B_TASK_ID` 原样继承；pi 以 `detached` 启动工具命令，
    环境取自自己的 `process.env`，所以工具命令和它们的孤儿都带标记。A2A 子 agent
    的 `MULTICA_TASK_ID` 被清空，靠 `FC_E2B_TASK_ID` 识别。
  - 已证明进程的全部子孙。
  - 已证明进程所在会话 / 进程组（0、1 除外）的其它成员，前提是该会话 / 组的
    leader 已被证明或已退出：只有 leader 的子孙能进入，所以它由本任务进程创建。
    leader 活着且不属于本任务时（例如 runner 所在的 envd 会话）不扩展。
- **不结束**：
  - 环境里写着别的任务 id 的进程，包括同沙箱另一任务在本任务 runner 之后放出、
    被 PID 1 接管的孤儿（PRI-61）。
  - `/usr/local/libexec/multica-*`、`/.fce2b/` 下的沙箱服务，除非是本任务 runner
    的子孙。共享的 provider 代理可能带着第一个任务的 id，不能凭标记结束。
  - 没有任务标记、也不在上述子孙或会话里的进程。
- **证明不了、因此会留下的情况**：
  - 自己清空环境（如 `env -i`）又脱离会话的孤儿。如果所在会话还有本任务的进程，
    可以按会话一并结束；否则留到沙箱按下文释放。
  - 环境读不到的进程：改过身份后变成 non-dumpable 的进程，以及 envd 这类能力集比
    执行者更高的 root 进程。计入回执 `unreadable`，不结束。
  - 显式改写了 `MULTICA_TASK_ID` / `FC_E2B_TASK_ID` 的进程，按改写后的值归属。
  - 处理取消的副本在这几秒内重启时，回退到 TTL。
- **DSH 原生任务**：DSH host 和员工级 DSH 进程启动时不继承调用方环境，不带任务 id，
  各自是存活的会话 leader，不会被结束；DSH 工具命令带着所属任务的
  `MULTICA_TASK_ID`，取消时只结束本任务的那些。这一条依据模板代码，尚未在 DSH
  沙箱实跑。
- **结束**：先 `SIGSTOP` 冻结并重扫一次，防止 fork 或改父进程逃逸；再 `SIGTERM`
  加 `SIGCONT`，最多等 5 秒后 `SIGKILL`。回执为
  `{"version":3,"runners","marked","unreadable","found","terminated","killed","remaining"}`，
  写日志 `event=fc_e2b_task_processes_stopped`（带 `pass`），有残留时为 Warn。

回归：`TestFCE2BTaskStopScriptEndsOnlyTheTaskProcesses`（Linux）在同一会话里放两个
任务的进程，真实执行脚本。以 root 运行时，fixture 切到 nobody，按 FC 的方式经
`setpriv` 读取标记。

沙箱本身仍按下文释放：Issue / 聊天 scope 剩 10 分钟，到期由云平台销毁。

## 任务结束后的释放

原先任务结束后没有任何释放，沙箱一律等 4800 秒 TTL。2026-09-23 巡检：
正式 7 天冷创建约 2000 次/天，复用率约 8%；一分钟跑完的 run-only
autopilot 也要占满 80 分钟，单个员工同时挂着 60 多个空转沙箱。

任务进入终态（completed / failed / cancelled）时，`task.go` 的
`publishTaskEvent` 和 `captureTaskCompleted/Failed/Cancelled`（归档员工这类
只记指标、不发事件的取消也走这里）通知 `TaskRuntimeTerminalObserver`，
实现在 `fc_e2b_sandbox_release.go`，同一任务同时只处理一次。它不阻塞
状态转换：后台等 30 秒（让 runner 上报收尾），再在 2 分钟预算、最多 4 个
并发（每个占一条连接池连接，默认池 25）、最多 512 个排队内处理该任务每个
start attempt 用过的沙箱。A2A 暂停直接取消本地任务，不经过这两处，仍按
TTL 回收：

| 沙箱 | 条件 | 动作 |
| --- | --- | --- |
| 员工文件系统 scope（`employee_filesystem_sandbox`），非 DSH provider | 任务既无 Issue 也无聊天，scope 就是该任务自己的 | 走 `RetireUnlessBusy`：确认销毁后 scope 回到 offline |
| 员工文件系统 scope | 其他（含所有 DSH provider 的 scope，一次性的也算） | 剩余寿命设为 10 分钟 |
| 普通沙箱 | 无 Issue 无聊天，也不在复用缓存里 | `DELETE` |
| 普通沙箱 | Issue / 聊天 scope | 剩余寿命设为 10 分钟，并把 `fc_e2b_sandbox_session.expires_at` 同步下调 |
| 旧的员工级 host（`dsh_employee_host`） | 任意 | 不动 |

10 分钟内同一 Issue / 聊天有新一轮任务，启动时照常续到 4800 秒并复用；
过期后下一轮会新建。以下情况保留沙箱不动（回退到原来的 TTL）：

- 同一沙箱上还有未结束的任务，或同一 Issue / 聊天上有未结束的任务
  （它可能已续期但还没把沙箱记到 attempt 上）；
- 有有效的原生浏览器授权，包括别的沙箱上的会话路由过来读取本沙箱的授权
  （路由授权兑换后也会续期目标沙箱，续期失败就撤销）。原生入口先续期、释放 scope 锁、再调网关，
  最后才写授权，中间可能隔几秒。所以入口在授权提交后会再续期一次，
  释放侧缩短寿命后也会复查授权，查到就恢复完整寿命；两边任意交错，
  最后生效的都是完整寿命；
- scope 锁正被某次启动持有（`pg_try_advisory_lock` 拿不到就跳过，
  那次启动自己会续期）。

DSH 员工沙箱即使一次性也不当场退役，只留 10 分钟：任务启动之外，原生入口
和 profile worker 也会在持有 scope 锁时续期员工沙箱，之后才发授权或操作
沙箱（网关启动最长 320 秒），这段时间没有任何持久记录。原生入口会选最近
用过的 scope，常常就是刚结束的一次性 scope，当场退役会让这次入口失败。
10 分钟窗口比网关启动长，入口在授权提交后的续期会把寿命恢复；续期失败则
撤销授权并返回可重试的 503，不会交出一个会话比沙箱活得更久的入口。
要当场释放 DSH 一次性沙箱，需要在续期时持久记录一个受 scope 锁保护的标记，
属于后续工作。

删除 Runtime 时，任务先解绑 Runtime 再广播取消，释放侧找不到 Runtime，
这些沙箱仍按 TTL 回收。

每一步都记 `event=fc_e2b_sandbox_lifecycle`：`action` 为
`created` / `reused`（任务启动时）、`released` / `idle_trimmed` /
`retained` / `release_failed`（任务结束后），带 `reason`、`scope`、
`sandbox_id`、`task_id`。在 SLS 按 `sandbox_id` 就能串起一台沙箱从创建到释放。

正式和预发共用同一个 FC 账号。DSH 员工沙箱创建时带
`multica.origin=<应用域名>` 标签，列表里能分辨环境；普通沙箱由
`e2b` CLI 创建，CLI 不支持写 metadata，只能靠各环境 SLS 里的
lifecycle 日志归属。

## 获取与替换

1. 在现有 runtime/scope 数据库锁内查找可复用沙箱；没有则创建。
2. 检查沙箱就绪后，无论新建还是复用，都调用
   `POST /sandboxes/{sandboxID}/timeout`，请求体为 `{"timeout":4800}`
   （或配置中更大的值）。这是将剩余生存时间设为该秒数，不是累加。
3. 调用 `GET /sandboxes/{sandboxID}`，确认 ID 一致、状态为 `running`，
   `endAt` 至少达到续期请求开始后的目标秒数（容忍 5 秒时钟/时间截断误差）。
   有 scope 的沙箱将云平台返回的实际到期时间写入已有
   `fc_e2b_sandbox_session.expires_at`，再继续任务启动。
4. 就绪或续期失败时，先将复用记录标记为 `stale`，再调用
   `DELETE /sandboxes/{sandboxID}` 释放沙箱，并创建一个新沙箱重新走上述流程。
   新沙箱仍需续期成功。单次获取最多尝试两个沙箱，避免平台故障时无限创建。
5. 释放失败会记日志，并继续尝试替换；未释放的实例最终由平台 TTL 回收。
   数据库失效标记写入失败则终止启动，避免删除仍可被复用的沙箱。

任务启动被取消时不再替换沙箱；尚未记录的新建实例会尝试释放。
每次云平台 HTTP 请求最多等待 10 秒，释放有独立的 10 秒清理时间。

## 配置与范围

- 创建 `--timeout` 与任务启动续期共用 `sandboxTaskTimeout()`：默认 4800 秒，
  `runtime.fc_e2b.timeout_seconds` 仅在大于 4800 时生效。
- 沿用现有 FC API 地址和密钥、数据库表及多实例获取锁，无新增迁移。
- 不需要 `runtime.fc_e2b.sandbox_renewal_enabled` 开关，也不使用定时扫描、
  ScheduleX 或 Redis 续期标记。
- 运行中仍不周期续期。单次任务超过本次续期后的到期时间，仍可能被平台回收。

## 验证

从旧定时方案升级时，先从目标环境 Diamond 的 `dt-fde-multica-runtime.json`
删除 `runtime.fc_e2b.sandbox_renewal_enabled` 字段，再发布服务。
运行配置采用严格解析；保留旧字段会导致后端以 `unknown field` 错误启动失败。
这项清理需按环境分别执行，不能用预发配置覆盖正式配置。

部署包含本改动的服务版本后，启动一次 FC/E2B 任务，按任务 trace 查询日志：

- `stage=fc_e2b_sandbox_renew status=succeeded`：应有 `sandbox_id`、
  `timeout_seconds=4800`（或配置中更大的值）和 `expires_at`；成功日志表示已读取平台并确认到期时间。
- 在同一会话内再启动任务，即使仍有充足 TTL，也应再次出现续期成功日志。
  同时核对 `fc_e2b_sandbox_session` 中相同沙箱的实际到期时间。
- 续期失败时应有 `fc_e2b_sandbox_renew status=failed`、释放结果和
  `fc_e2b_sandbox_replace status=started`，随后是新 ID 的创建及续期结果。
  两个候选都失败时任务启动报错，不会向它们提交执行命令。

本地回归使用模拟云平台 HTTP 接口和 PostgreSQL，覆盖每次复用续期、新建续期、
失败替换、释放失败、替换次数上限、多实例并发和单连接池获取。
这些回归不替代预发云平台的实际验证。

## 变更历史

| 日期 | 变更 | 原因 |
| --- | --- | --- |
| 2026-09-28 | 一次取消清理的两遍固定使用排期时的配置快照 | PRI-67：两遍各自重读配置，两遍之间热关会漏掉补杀，热开会给没选中的任务补跑第二遍 |
| 2026-09-28 | 取消清理和 `FC_E2B_TASK_ID` 注入改由 `runtime.fc_e2b_sdk_rollout` 按作用域控制，与 SDK 传输共用一个开关 | E2B 改造整体可灰度、可热关，不再“部署即生效”；与性能优化开关分开（PRI-47 方案 B） |
| 2026-09-27 | 取消后只结束能证明属于本任务的沙箱进程：runner 命令行、按进程 uid 读取的任务标记、其子孙及由本任务创建的会话；runner 额外注入 `FC_E2B_TASK_ID`；去掉按启动时间认领孤儿 | PRI-61 在真实 FC 上发现，按“被 PID 1 接管且在 runner 之后启动”认领孤儿，会结束同沙箱另一任务之后放出的孤儿 |
| 2026-09-23 | 任务结束后释放不可复用的非 DSH 沙箱，其余（含 DSH 员工沙箱）只保留 10 分钟空闲窗口；DSH 等待日志带上错误原因；DSH 沙箱打 `multica.origin` 标签 | 沙箱只靠 4800 秒 TTL 回收，92% 为一次性冷创建；等待日志只有 reason 看不到云平台结果；正式/预发共用账号无法按环境统计 |
| 2026-09-20 | 创建与任务启动续期统一为默认 4800 秒，配置只允许更大 | 3600s 墙会杀掉仍在前台跑的长任务；续期写死 3600 使 Diamond 调大无效 |
| 2026-09-09 | 回滚定时续期，改为每次任务启动时续期 3600 秒，失败释放并最多替换一次 | 简化多实例续期协调，避免在无法确认寿命的沙箱上启动任务 |
| 2026-09-09 | 补充发布前移除旧 Diamond 续期开关的步骤 | 预发部署发现旧字段会被新代码严格解析拒绝；删除后实例启动恢复 |
