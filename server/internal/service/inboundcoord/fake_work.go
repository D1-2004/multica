package inboundcoord

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const hintFakeWorkReply = "action=reply cannot claim 已记录/已保存/我先查/稍后给你. This loop does not write or search. finish action=issue with items so the sandbox (work-report-operator, knowledge-query, …) does the work. Spoken text names the work, such as 我去把今天的日报记上."

var fakeWorkReplyClaim = regexp.MustCompile(`已记录|已保存|已写入|已记入|记进日报|记入日报|先查一下|稍后给你更具体|今天会重点跟进|建议从这几个方向入手`)

func requireNoFakeWorkReply(turn Turn, finishRaw string) error {
	if turn.Loop == LoopTaskFinished {
		return nil
	}
	var parsed struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(finishRaw)), &parsed)
	if Action(strings.TrimSpace(parsed.Action)) != ActionReply {
		return nil
	}
	text := strings.TrimSpace(parsed.Text)
	if fakeWorkReplyClaim.MatchString(text) {
		return hintErr("action=reply claimed a sandbox effect this loop cannot perform", hintFakeWorkReply)
	}
	if hasSkillNamed(turn, "work-report-operator", "日报") && looksLikeSelfReportSubmit(turn.Message) && !replyAsksForReportBody(text) {
		return hintErr("current_message is a self daily-report submit; finish action=issue for work-report-operator", hintFakeWorkReply)
	}
	if hasSkillNamed(turn, "knowledge-query") && looksLikeKnowledgeLookup(turn.Message) && !replyIsShortClarification(text) {
		return hintErr("current_message needs knowledge-query; finish action=issue, do not answer from the coordinator", hintFakeWorkReply)
	}
	return nil
}

func hasSkillNamed(turn Turn, needles ...string) bool {
	for _, skill := range turn.Skills {
		blob := strings.ToLower(strings.TrimSpace(skill.Name) + " " + strings.TrimSpace(skill.Description))
		for _, needle := range needles {
			if needle = strings.ToLower(strings.TrimSpace(needle)); needle != "" && strings.Contains(blob, needle) {
				return true
			}
		}
	}
	return false
}

func looksLikeSelfReportSubmit(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "今天主要工作") || strings.Contains(msg, "今日主要工作") || strings.Contains(msg, "日报日期") {
		return true
	}
	if strings.Contains(msg, "日报") && !strings.Contains(msg, "如何") && !strings.Contains(msg, "怎么") {
		return true
	}
	return false
}

func replyAsksForReportBody(text string) bool {
	return strings.Contains(text, "正文") || strings.Contains(text, "注明日") || strings.Contains(text, "发过来")
}

func looksLikeKnowledgeLookup(msg string) bool {
	return strings.Contains(msg, "如何解决") || strings.Contains(msg, "是否应对") || strings.Contains(msg, "正式口径")
}

func replyIsShortClarification(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > 40 {
		return false
	}
	return strings.Contains(text, "？") || strings.Contains(text, "?")
}
