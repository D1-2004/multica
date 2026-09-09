# FC/E2B 任务启动时续期

FC/E2B 沙箱获取逻辑位于 `server/internal/service/fc_e2b.go` 的
`resolveSandboxOnConnection`，续期和释放接口位于 `fc_e2b_timeout.go`。
任务执行命令只会在沙箱准备及续期成功后提交。

## 获取与替换

1. 在现有 runtime/scope 数据库锁内查找可复用沙箱；没有则创建。
2. 检查沙箱就绪后，无论新建还是复用，都调用
   `POST /sandboxes/{sandboxID}/timeout`，请求体为 `{"timeout":3600}`。
   这是将剩余生存时间设为 3600 秒，不是在原到期时间上累加一小时。
3. 调用 `GET /sandboxes/{sandboxID}`，确认 ID 一致、状态为 `running`，
   `endAt` 至少达到续期请求开始后 3600 秒（容忍 5 秒时钟/时间截断误差）。
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

- 每次启动的续期目标固定为 3600 秒；`runtime.fc_e2b.timeout_seconds`
  仍控制创建时传给平台的初始生存时间，默认也是 3600 秒。
- 沿用现有 FC API 地址和密钥、数据库表及多实例获取锁，无新增迁移。
- 不需要 `runtime.fc_e2b.sandbox_renewal_enabled` 开关，也不使用定时扫描、
  ScheduleX 或 Redis 续期标记。
- 运行中不会周期续期。单次长任务超过本次续期后的到期时间，仍可能被平台回收；
  本方案不增加到期前回调、沙箱内部清理或任务结束补报机制。

## 验证

部署包含本改动的服务版本后，启动一次 FC/E2B 任务，按任务 trace 查询日志：

- `stage=fc_e2b_sandbox_renew status=succeeded`：应有 `sandbox_id`、
  `timeout_seconds=3600` 和 `expires_at`；成功日志表示已读取平台并确认到期时间。
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
| 2026-09-09 | 回滚定时续期，改为每次任务启动时续期 3600 秒，失败释放并最多替换一次 | 简化多实例续期协调，避免在无法确认寿命的沙箱上启动任务 |
