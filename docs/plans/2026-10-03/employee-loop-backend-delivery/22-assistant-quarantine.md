# 历史祖先失败的局部隔离

M16 当波 LF 29b6037d2ee7432baae9515315d040b4 的实际输入是 history unavailable，且无 transcript observation/metadata。源码链条是 RecentConversation 任一祖先 sentinel → recentConversationSnapshot 停止 DWS read → buildInput 整段 unavailable。尚无生产底层 error/具体 ancestor ID，不能将未知源或 SQL 错误臆定为已证；以退休 record + 实际 /reset-memory 零模型 NULL snapshot 格式建立真实 DB 反例。

修复目标：退休/未知/不可闭合 assistant 自身及精确引用的后继整条剔除，新/独立真人材料与正常受信 DWS 转录保持可用；不把未知快照宽松放行，不匹配正文 value。记录静态 reason/count，不记录正文。数据库错误、取消、全图集合或深度超限仍整体 fail closed；原审计与旧冻结字节不动。只改 history/closure 与必要反例，主代理负责发布与原 M5 复验。

## 状态

spec/Plan 先更新，真实 DB 原反例与核心局部隔离进行中。

根因范围已确定：actual LF 29b... 确实在历史读取前端降级，生产具体 ancestor ID/底层 error 因旧实现未记录仍未知。真实 DB 复现退休记录 + /reset-memory 零模型 NULL snapshot + 后继引用：基线全 history 返回 errReplyAncestorUnavailable，候选只剔除未知助理/后继，保留新真人原话与同值 provider 转录；这证明根源码链，不把缺失日志冒充生产已确认的具体异常。新增 event employee_history_assistant_quarantined 只记录scope/job ID与静态reason，快照和LF记原因/剔除数量，不含正文。

独立 review 补充的 invalid-pair 对抗也纳入：不可信ref中的ActionID/MessageID不能授权全局擦除。只隔离坏节点及其后继；只有同scope、目标会话、实际delivered且关联源job已核对的assistant才把实际provider ID传给transcript过滤。独立真人被伪造为ref.MessageID时仍保留。

验证已完成：独立本地DB multica_quarantine_20261004，发布源码 dfa79d4eb6 的只读overlay真实reset/新真人原反例 FAIL，候选 PASS；包含invalid-pair、新人同值材料、跨窗/旧快照/权限及硬cap、SQL/取消路径共26项top-level无skip通过。unknown source仍隔离；64/2000硬cap专用errTranscriptEvidenceBound不被sentinel catch吞掉。原件和旧字节未改；仅为明确独立来源的测试fixture添加显式empty Memory保存格式，未放宽未知快照。handler受影响编译与diff/gofmt通过。证据 CODEX-CLOSEOUT-20261004-ASSISTANT-QUARANTINE/baseline.log、candidate.log。未新增模型或发IM/线上写入，生产具体未知源仍待新静态reason日志定位；原M5复验由root裁定。
