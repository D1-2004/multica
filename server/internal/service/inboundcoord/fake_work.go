package inboundcoord

import (
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
)

const hintFakeWorkReply = "This loop cannot write or search, so action=reply must not claim 已记录/已保存/我先查一下/稍后给你. Two paths: if this is skill work (daily-report submit, knowledge lookup, …), call assoc_recall for this conversation first, then finish action=issue with items so the sandbox runs it (spoken text like 我去把今天的日报记上). If this is only conversation, keep action=reply without those claims. Status questions stay reply."

// Only high-precision false-completion claims. Routing (日报 submit vs 日报 status,
// knowledge lookup vs opinion) belongs in policy, not this Host check.
var fakeWorkReplyClaim = regexp.MustCompile(`已记录|已保存|已写入|已记入|已记下|先查一下|稍后给你更具体`)

var fakeWorkClaimNegation = []string{"尚未", "还没", "没有", "未"}

var errFakeWorkReply = errors.New("action=reply claimed a sandbox effect this loop cannot perform")

func requireNoFakeWorkReply(turn Turn, finishRaw string) error {
	return requireNoFakeWorkReplyOnce(turn, finishRaw, false)
}

func requireNoFakeWorkReplyOnce(turn Turn, finishRaw string, alreadyHinted bool) error {
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
	if text == "" || replyLooksLikeQuestion(text) {
		return nil
	}
	loc := fakeWorkReplyClaim.FindStringIndex(text)
	if loc == nil || claimIsNegated(text, loc[0]) {
		return nil
	}
	if alreadyHinted {
		slog.Info("inbound coordinator fake-work reply accepted after hint",
			append(coordinatorLogIndex(turn),
				"event", "inbound_coordinator_fake_work_reply_pass",
			)...)
		return nil
	}
	return hintWrap("", hintFakeWorkReply, errFakeWorkReply)
}

func isFakeWorkReplyHint(err error) bool {
	return errors.Is(err, errFakeWorkReply)
}

func replyLooksLikeQuestion(text string) bool {
	return strings.Contains(text, "？") || strings.Contains(text, "?")
}

func claimIsNegated(text string, claimStart int) bool {
	if claimStart <= 0 {
		return false
	}
	prefix := []rune(text[:claimStart])
	if len(prefix) > 8 {
		prefix = prefix[len(prefix)-8:]
	}
	s := string(prefix)
	for _, neg := range fakeWorkClaimNegation {
		if strings.HasSuffix(s, neg) {
			return true
		}
	}
	return false
}
