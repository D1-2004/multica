# 场域例行任务与会话内场域配置（2026-10-02）

分支 `feat/tag-multitenant`，建在 AgentScene（`docs/agent-scene.md`，PRI-84 / PRI-91）之上。接口和规则以 `docs/context-capabilities.md` §9、§10 为准，这份文档讲为什么这么做、怎么上线和回滚。

## 1. 做了什么

1. **例行任务**：挂在一个场域（群聊或单聊）上的定时任务或 Webhook 任务。每次运行都带这个场域的配置；Host 在场域里发「开始」和「结束」两条消息，结束消息附最终输出。企业级和个人级没有例行任务（与 Claude Tag 一致：routine 只属于 channel 或 DM）。
2. **配置页**：手机「QwenTag配置」新增「例行任务」tab；网页场域树的群聊和单聊节点新增「例行任务」子 tab。两处共用 `ScopeRoutines`，Tag 员工页自动带上。
3. **会话内修改场域配置**：有当前场域的任务会挂载 `config-qwen-tag-scene` 技能和同名 MCP 服务器，可以读取和修改**当前场域**的提示词、例行任务、公开给场域的 Skill 和连接器开关、远程 MCP 服务器；连接账号一律发配置链接。

## 2. 关键取舍

- **复用 Autopilot。** 例行任务就是一个 `run_only` autopilot 加一行 `context_scope_routine`。Cron 的租约和幂等、Webhook 的 token、签名、去重和持久 worker 都直接沿用。场域托管的 autopilot 在 autopilot 接口上返回 409 `managed_by_scene`，只能从场域配置改。
- **场域只来自 routine 行。** 运行上下文由 Host 冻结（`agent_scene` + `scene_routine`），Webhook body 改不了场域。场域在运行创建时校验（`scene.CheckTenant`），员工换绑企业后运行记为跳过，不会落到别的场域。
- **群例行任务不带个人能力。** 只用场域的配置运行，避免一个人的账号被群里所有人的定时任务使用；单聊例行任务只有在创建时能证明对方 staffId 时才带上对方的个人能力。
- **完成消息先不进 Coordinator。** 冬翔决定：先由 Host 直接发确定性的开始和结束消息，等改 EmployeeLoop 时再换成交给 Loop 的完成事件（见 §5）。
- **MCP 按场域按任务签发。** 认领时签发 `sct_` 场域令牌放进路由路径。现有 daemon 改写托管 MCP 配置时只保留 `Authorization`，所以令牌不能放在额外的请求头里；放在路径里，现有沙箱镜像不用重建。令牌单独拿到没用：调用时还要同一个任务的 task token，并且服务端每次都会重新解析任务的场域。路径里的令牌在访问日志和沙箱 relay 日志中都会被替换成 `[redacted]`。
- **会话内修改按场域放开，但有护栏。** 冬翔的原话是「按照场域来，改动范围就是当前场域的」：工具不接受场域参数。护栏有三条：
  - 例行任务的运行只读（Webhook 输入是外部内容）；
  - 每次写入都由 Host 在场域发一条变更通知；
  - 技能要求改之前先复述，等请求人确认后再改。
- **借鉴 GawkBot**（`najmuzzaman-mohammad/gawkbot@71e82a1`）：
  - 去重只在同一场域内；重复注册不会改启停状态和所属场域；
  - 工具返回 `tell_the_human`，让员工如实转述；
  - 时区显式保存，最小间隔为 15 分钟；
  - 开始消息不贴 prompt；
  - 找不到场域时记失败，不回落到别处。
  
  它的这些做法**没有**照搬：共享 token 加自报身份、工具带 channel 参数、跨频道的 upsert、只按 UTC。

## 3. 迁移

| 编号 | 内容 | 能否重复执行 |
| --- | --- | --- |
| 9520 | `context_scope_routine` 表（无外键，工作区删除时清理） | `IF NOT EXISTS` |
| 9521 | `(autopilot_id)` 唯一索引，单独一个 `CONCURRENTLY` 文件 | 是 |
| 9522 | `(scene_id, dedupe_key)` 唯一索引，单独一个 `CONCURRENTLY` 文件 | 是 |

推送前要对照预发 release 分支检查撞号（其他在途 CR 可能占用 95xx 段）。

## 4. 上线和回滚

**上线检查**

- 本地 Go：例行任务 7 个用例、场域配置 MCP 6 个用例、令牌用例；handler 全量失败集合与基线相同（63 = 63）；contextcap、service、dingtalkresponse、middleware、sandboxrelay 全部通过。
- 前端：core 和 views 的 typecheck 通过；configure 页 70 个用例通过（新增 4 个例行任务用例）；schema 畸形响应用例通过。
- 预发验证顺序：
  1. 在测试群建一个每 2 分钟一次的 Cron，确认开始和结束消息；
  2. 用 curl 调 Webhook 任务，并伪造 body 里的 cid，确认不改变场域；
  3. 在单聊里让员工建一个例行任务；
  4. 在群里让员工改提示词，确认只在本群生效；
  5. 拿别的任务的令牌调用、任务结束后调用、在例行任务运行中写入，都应被拒绝。

**滚动发布期间**

- 旧副本不认识 `scene_routine` 上下文。在旧副本上认领的例行任务没有场域层，等同于普通 autopilot。
- 旧 worker 遇到没有 callback 的开始/结束消息会在校验时失败，这条消息丢失，不会误发。
- 旧副本没有 `/api/scene-config/mcp`，在新副本上认领、在旧副本上调用的工具会返回 404。
- 发布完成后以上问题消失。

**回滚**

- 重新部署上一版本即可。新表不会被旧代码读取。场域托管的 autopilot 在旧版本里是普通的 `run_only` autopilot，会继续按计划运行，只是没有场域层和开始/结束消息。
- 如果需要停止，在 autopilot 页面暂停即可（旧版本没有 409 保护）。

## 5. 已知限制和后续

- **完成事件接入 Loop。** EmployeeLoop 落地时，把结束消息换成交给场域 Loop 的完成事件。调研结论：
  - Coordinator 现在没有「非消息事件」的入口，也没有不经回调的主动发送；
  - GawkBot 的做法是 worker 自己发完再唤醒 lead，run 在「已发出」时就记为成功，不建议照抄。
- **会话内修改的开关和审批。** 后续考虑：
  - 给场域加一个「会话内修改：允许/禁止」开关（Claude Tag 的 Channel member edits）；
  - 高风险变更（新增远程 MCP、开启连接器、高频例行任务）改为在场域里弹审批卡片。
- **单聊例行任务的创建条件。** 在配置页创建，需要这个单聊里已经有对方发来的入站消息（用于发送对象），否则返回 `dm_target_unknown`。
- **Webhook 地址的获取。** 会话里创建的 Webhook 任务，完整地址不会进入会话。管理员需要在配置页用「重新生成 Webhook 地址」拿到地址。
- **失败暂停不通知。** 连续失败的自动暂停沿用 autopilot 失败监控，暂停时还不会在场域里发说明。
- **旧 daemon 不挂载场域配置 MCP。** 不支持托管 MCP 路由的旧 daemon 走 legacy 路径，不挂载 `config-qwen-tag-scene` MCP，只有技能文档。
