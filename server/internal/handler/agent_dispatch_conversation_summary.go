package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dispatchEventTypeConversationSummary is the Router channel event type for the
// hourly group-conversation digest. Such a dispatch carries no message list: the
// agent reads the conversation back with `dws chat` and acts only if warranted
// (silent detection, act on demand). It never triggers server-managed reply
// delivery, because managedDingTalkResponse is scoped to message.created.
const dispatchEventTypeConversationSummary = "conversation.summary"

// ConversationSummaryEventData 承载群会话增量汇总（Router channel/conversation.summary
// 契约）。Router 不存消息内容，只给出会话定位、分桶时间范围与各项计数；agent 用注入的
// DWS 身份执行 dws chat 按 conversationCid + 时间范围拉取该时段记录，静默检测是否有需要
// 关注的内容并按需行动。字段与 router ConversationSummaryDispatchData.conversationSummary
// 段一一对应。
type ConversationSummaryEventData struct {
	SummaryID                   string          `json:"summaryId,omitempty"`
	SourceID                    string          `json:"sourceId,omitempty"`
	AgentID                     string          `json:"agentId,omitempty"`
	ConversationCid             string          `json:"conversationCid,omitempty"`
	TimeBucket                  string          `json:"timeBucket,omitempty"`
	BucketStart                 string          `json:"bucketStart,omitempty"`
	BucketEnd                   string          `json:"bucketEnd,omitempty"`
	MessageCount                int             `json:"messageCount,omitempty"`
	MentionCount                int             `json:"mentionCount,omitempty"`
	FollowedSenderMessageCount  int             `json:"followedSenderMessageCount,omitempty"`
	TopConversationMessageCount int             `json:"topConversationMessageCount,omitempty"`
	Attributes                  json.RawMessage `json:"attributes,omitempty"`
	FirstMessageAt              string          `json:"firstMessageAt,omitempty"`
	LastMessageAt               string          `json:"lastMessageAt,omitempty"`
}

// validateConversationSummary 校验群会话增量汇总事件。汇总是数字员工专属的静默巡检：
// 必须有 summaryId 与会话定位，但不携带消息列表；surface 允许 issue/chat/auto（服务端
// 托管回复只对 message.created 生效，汇总不会被自动投递到群），outbound 允许
// none/dws/robot_sdk；必须带 DWS 身份，否则 agent 无法执行 dws chat 拉取会话记录。
func (c DispatchCommand) validateConversationSummary() error {
	if c.Source.Type != "digital_employee" {
		return errors.New("conversation.summary source must be digital_employee")
	}
	summary := c.Event.Data.ConversationSummary
	if summary == nil ||
		strings.TrimSpace(summary.SummaryID) == "" ||
		strings.TrimSpace(c.Event.Data.Conversation.OpenConversationID) == "" {
		return errors.New("conversation.summary requires conversationSummary.summaryId and conversation.openConversationId")
	}
	if c.Surface.Type != protocol.DispatchSurfaceTypeIssue &&
		c.Surface.Type != protocol.DispatchSurfaceTypeChat &&
		c.Surface.Type != protocol.DispatchSurfaceTypeAuto {
		return errors.New("conversation.summary surface.type must be issue, chat, or auto")
	}
	if c.Outbound.Mode != protocol.DispatchOutboundModeNone &&
		c.Outbound.Mode != protocol.DispatchOutboundModeDWS &&
		c.Outbound.Mode != protocol.DispatchOutboundModeRobotSDK {
		return errors.New("conversation.summary outbound.mode must be none, dws or robot_sdk")
	}
	if strings.TrimSpace(c.ExternalIdentity.ContextToken) == "" && c.ExternalIdentity.DWS == nil {
		return errors.New("conversation.summary externalIdentity.contextToken or externalIdentity.dws is required")
	}
	return nil
}

// buildConversationSummaryPrompt 渲染群会话汇总的可见内容（进入 issue/comment 正文）。
// 只陈述分桶事实与计数，不携带 cid 或运行时指令——那些经 Router 的 contextPrompt 由
// claim 投影注入，可见内容与私有指令严格分离。
func buildConversationSummaryPrompt(c DispatchCommand) DispatchPrompt {
	return DispatchPrompt{
		DisplayContent: buildConversationSummaryDisplay(c),
	}
}

func buildConversationSummaryDisplay(c DispatchCommand) string {
	title := strings.TrimSpace(c.Event.Data.Conversation.Title)
	if title == "" {
		title = "群聊"
	}
	s := c.Event.Data.ConversationSummary
	if s == nil {
		return "钉钉群会话增量汇总（" + title + "）。\n"
	}
	var b strings.Builder
	b.WriteString("钉钉群会话增量汇总（" + title + "）：\n\n")
	if window := conversationSummaryWindow(s); window != "" {
		b.WriteString("- 时间范围：" + window + "\n")
	}
	fmt.Fprintf(&b, "- 新消息：%d 条\n", s.MessageCount)
	fmt.Fprintf(&b, "- 其中 @你：%d 条\n", s.MentionCount)
	fmt.Fprintf(&b, "- 特别关注人发送：%d 条\n", s.FollowedSenderMessageCount)
	fmt.Fprintf(&b, "- 置顶会话消息：%d 条\n", s.TopConversationMessageCount)
	b.WriteString("\n请按你的既定职责静默核查该时段会话，仅在确有需要时按需行动。")
	return b.String() + "\n"
}

func conversationSummaryWindow(s *ConversationSummaryEventData) string {
	start := strings.TrimSpace(s.BucketStart)
	end := strings.TrimSpace(s.BucketEnd)
	switch {
	case start != "" && end != "":
		return start + " ~ " + end
	case start != "":
		return start
	default:
		return strings.TrimSpace(s.TimeBucket)
	}
}

// dispatchConversationSummaryTitle 生成汇总 issue 标题：<群名> 会话汇总｜<时间桶>。
func dispatchConversationSummaryTitle(c DispatchCommand) string {
	title := normalizeDispatchTitleFragment(c.Event.Data.Conversation.Title)
	if title == "" || title == "钉钉群聊" {
		title = "群聊"
	}
	bucket := ""
	if s := c.Event.Data.ConversationSummary; s != nil {
		bucket = strings.TrimSpace(s.TimeBucket)
	}
	if bucket == "" {
		return title + " 会话汇总"
	}
	return title + " 会话汇总｜" + bucket
}
