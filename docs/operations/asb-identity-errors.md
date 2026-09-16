# ASB 企业身份启动错误

BUC attach 或任务启动前的身份探测失败时，Multica 保留上游错误原因，写入任务错误、聊天消息和 Runtime 启动记录。无需重建 Runtime 镜像。

| 错误码 | 原因 |
| --- | --- |
| `ASB-BUC-TRUST-DEVICE-LIMIT` | `BIZ_SERVICE_CONTAINER_TRUST_DEVICE_REGISTER_USAGE_EXCEEDS_LIMIT`：阿里郎可信设备注册使用量超限，需由阿里郎管理员核查额度。回收 ASB 沙箱不等于释放可信设备额度。 |
| `ASB-BUC-ZT-TOKEN-NOT-FOUND` | BUC 身份验证返回 `zt token not found`。该信息本身不能证明凭证过期，需结合地域和身份链路排查。 |
| `ASB-BUC-IDENTITY-MISMATCH` | 沙箱身份与绑定员工不匹配。 |
| `ASB-BUC-IDENTITY-FAILED` | 其他绑定或验证失败，详情保留可安全展示的上游消息。 |

后端先从 attach 响应中提取已知原因，再限制消息长度；失败原因不会因为位于长异常末尾而被 512 字节截断丢弃。BUC 探测只输出失败码和消息，不输出身份数据或凭证。探测期间短暂的 `zt token not found` 仍可等待收敛；已返回明确根因的 attach 错误不会被当作正常的通道等待。

若响应缺少已知原因，后端在回收本次新建沙箱前请求 `diagnostics/logs?scope=lifecycle`，独立限时 3 秒。只从当前沙箱的 inline 日志提取已知错误签名，不向用户暴露原始日志，也不追踪日志下载 URL。诊断不支持、超时或缺失时保留原始失败，不阻止沙箱清理。

这些改动用于后续启动失败；已经持久化的历史任务错误不会自动改写。
