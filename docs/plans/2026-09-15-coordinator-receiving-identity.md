# 钉钉接收身份与终结检查修复

## 范围与原因

基于 aone/develop `ab43969b7e` 创建 `codex/fix-coordinator-receiving-identity`，只发布预发。
正式 oa测试群 2026-09-15 18:42 回合 `ba5f8899-ad45-44df-bb10-8d428136209a`：employee_uid=6899376218，mentions 只有同值 open_dingtalk_id，mention_relation=unknown，绑定接收显示名为 VOC决策助理，正文 @必应(毕颖)。关键词 Host 检查将通过审核的 ignore 强制 revise 为工作，而工作审核又因对象误识别要求忽略，5轮失败。
19:02–19:08 回合发生静默，完整 Langfuse 与 SLS 原文只保留在本机临时证据中，不提交业务全文。

## 实施

1. 核对入站 ID 协议，修正可信逐条 @ 接收身份投影，避免旧名称否定稳定身份；冲突/未知 ID 不猜。
2. 删除 finish 后按“帮我/查一下”等关键词强制工作分流，保留独立语义审核、来源/工作目标及权限协议校验。
3. 同步 COORD.F01/F04/F13/F19 合同、来源映射与最小对照。
4. 做受影响 Host 验证和结构检查；若条件允许补真实模型回放。提交本会话改动并通过 Aone pipeline 66 部署预发，核对版本与健康。

## 用户追加范围

必须修复BUG 1；发给数字员工的消息，无论判断失败还是处理受阻，都要有可见兜底回复。复用持久回执/outbox并核验幂等，不以本地聊天文字代替投递。明确找别人的群消息仍不认领、不执行。口吻自然，不外露系统reason，也不虚报成功。

## 验收与遗留

- 待验证：旧昵称且明确 @ 员工的闲聊可协调回复、工作可派发。
- 待验证：仅指向他人的同文工作请求可忽略，不被关键词强制执行。
- 待验证：缺失/冲突 ID、多消息合窗不串对象。
- 不将 Host 单测或健康检查当作真实 IM 送达；正式环境不发布。

## 实施结果（部署前）

- @ 兼容只限缺少显式uid、openDingTalkId为十进制且与接收uid完全相等；显式uid优先，冲突不认领，opaque open ID不当uid。
- 群参与策略明确逐条可信@优先于绑定旧显示名；不从消息文本修改绑定身份。
- 删除finish审核后的关键词工作分流；独立语义审核及目标一致性检查保留。
- 正常审核后的单聊/可信@静默改成无执行权限的Host回复并检查点保存；错误停止兜底文案自然、不含reason。
- 终态失败把固定回复和failed状态写入同一completion outbox；主回调唯一发文字，合窗附属回调不重复发。managed response_action和legacy shouldReply均消费该文字，内部错误仅诊断。
- 已通过 inboundcoord 全包、身份投影定向测试、handler/service/completion worker定向测试（含真实本地DB失败outbox幂等）；policy结构检查通过。
- 本地managed response_action集成验证受缺失response_route/response_action/sandbox_send_receipt表阻塞；未手工修改预发库。没有本机模型凭证，尚未真实模型回放。真实IM送达尚未验收，不能把上述单测等同线上行为通过。
