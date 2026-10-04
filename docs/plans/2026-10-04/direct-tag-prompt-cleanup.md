# Direct / Tag 提示词清理

- 目标：保留现有拼装，删除 Direct 中 Multica 操作引导；修正 Tag 岗位内的进度/最终回复冲突，不靠追加优先级提示解决。
- 验收：岗位/上下文/技能与原工作包保留；稳定执行面不再增加平台执行/输出说明；最终结果仍由 Host 发送，显式文件/主动消息边界保留；Tag 不再要求 reply/final 或固定命令定位。
- 环境：当前 checkout 的定向 Go 检查；预发 Tag 配置 API。只变更 Tag instructions，使用模板更新与显式租户 Apply，不热改继承的员工。
- E2E：部署候选后检查 Direct 普通任务与例行任务各自仅一份终态回复、显式文件/其他目标消息仍正常。本轮不发布镜像或发送测试消息，不声称模型运行验收通过。
- 证据：原 Langfuse Direct traces `85be76e7879a42c997fcb13fc067e69b`、`ff75a48e8db043f48bcfaa7c897123ff`。预发 workspace `5f8b5b73-f912-4879-9a29-b763d103fedf`，模板 `cfa971c0-11e2-441c-918f-483418d814ea`，读取时 revision=11、无未发布变更。
- 源码：完成。Direct 稳定 brief 只保留岗位/上下文/技能；Daemon 和 context 文件不再包 Multica 开场；claim Output 收为两句交付合同，不再追加优先级或操作说明。普通任务保持原路径。
- 本地验证：handler 的 Direct 输出/所有权合同、Daemon `TestDirectTask` 和 execenv 全套测试通过，`git diff --check` 通过。此前例行任务数据库检查被本地缺少 `agent.coordination_mode` 与 Employee 表阻断，未修迁移或声称其通过。
- 配置：通过模板更新与两个显式租户 Apply 发布 revision 13；模板和 `Tag · 钉钉`、`Tag · RealNiubility` 均回读匹配新提示词，员工仅 `instructions` 与 `updated_at` 改变。原文见 [备份](tag-instructions-before.md)，新文见 [当前提示词](tag-instructions.md)，回执见 [API 证据](tag-prompt-apply.json)。还原时以备份更新模板、发布更高 revision 并 Apply 相同租户，不直接修改员工。
- 交付边界：源码和配置完成，整理为独立本地提交交给“发布和验收”任务 `01a10278-0dd8-7a83-be4c-72fc351bbecf`，由其在合适窗口审查、合并、发布并验证；本任务不抢占发布窗口。预发所有现存租户均已 Apply revision 13，无未发布变更，最终全租户回读见 API 证据。新任务会读到新版 Tag 配置；已经冻结的任务输入不重写。模型 E2E 与 Runtime 滚动兼容未验证，后续部署时按 FC Runtime skill 验证，不能复用旧 Langfuse trace 声称新效果通过。
- 集成注意：本工作包基于 `582cba1f73`。若目标分支已有 `employee-progress/report_progress` 提示，应保留进度工具的语义并按本次短合同收敛，不机械覆盖其他会话的改动。分别发布服务器 claim 文案和包含 execenv/Daemon 的候选 Runtime，不能以只升级服务器证明稳定引导已移除。
- 合并后验证：Direct 成功/失败各只有一份 Host 终态；岗位及技能不调用 reply/final 重复发送；例行任务由 Host 发开始/结束；显式文件和其他目标消息保留且核验送达；进度工具只报告非终态进展。按受影响的现有 Eval/原反例跑，已有 E2E 保留检查点，只重验受此配置/部署影响的步骤。版本穿窗记录实际生效时间，不重置全套测试或把混版结果算通过。
- 分支交付（2026-10-04）：用户要求直接整合并推送内部 Code `feat/tag-multitenant`。已将本任务单独提交 rebase 到目标 `5357e67fb416ed4793a500f5f5f9f11a8f0f4064`，基础文档提交已在目标中而自动去重，保留目标新增的 scene routine webhook 分支。源码推送不等于服务器或 Runtime 发布；后续仍由“发布和验收”核对实际运行。
