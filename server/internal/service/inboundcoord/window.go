package inboundcoord

import (
	"strings"
)

// SceneWindowMaxItems is how many distinct deliverables one Decide window
// may start or continue. A third real ask stays pending for the next window.
const SceneWindowMaxItems = 2

// WindowUtterance is one addressed inbound line in the current scene window.
type WindowUtterance struct {
	Sender string
	Text   string
}

// WindowItem is one deliverable the window decided to handle.
type WindowItem struct {
	Delegator string
	Purpose   string
	Intent    string
	LookInto  string
}

func windowUtterances(turn Turn) []WindowUtterance {
	if len(turn.Utterances) > 0 {
		out := make([]WindowUtterance, 0, len(turn.Utterances))
		for _, u := range turn.Utterances {
			text := strings.TrimSpace(u.Text)
			if text == "" {
				continue
			}
			sender := strings.TrimSpace(u.Sender)
			if sender == "" {
				sender = strings.TrimSpace(turn.SenderName)
			}
			out = append(out, WindowUtterance{Sender: sender, Text: text})
		}
		if len(out) > 0 {
			return out
		}
	}
	text := strings.TrimSpace(turn.Message)
	if text == "" {
		return nil
	}
	return []WindowUtterance{{Sender: strings.TrimSpace(turn.SenderName), Text: text}}
}

func windowSenderSet(turn Turn) map[string]struct{} {
	out := map[string]struct{}{}
	if name := strings.TrimSpace(turn.SenderName); name != "" {
		out[name] = struct{}{}
	}
	for _, u := range windowUtterances(turn) {
		if u.Sender != "" {
			out[u.Sender] = struct{}{}
		}
	}
	return out
}

func validWindowDelegator(turn Turn, delegator string) bool {
	delegator = strings.TrimSpace(delegator)
	if delegator == "" {
		return false
	}
	senders := windowSenderSet(turn)
	delete(senders, "")
	if len(senders) == 0 {
		return true
	}
	_, ok := senders[delegator]
	return ok
}

// AllWindowAck is true when every line is thanks / OK / "don't reply".
func AllWindowAck(turn Turn) bool {
	utterances := windowUtterances(turn)
	if len(utterances) == 0 {
		return allAckText(turn.Message)
	}
	for _, u := range utterances {
		if !allAckText(u.Text) {
			return false
		}
	}
	return true
}

func allAckText(raw string) bool {
	s := stripInboundDisplay(raw)
	found := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !isAckOrStopReply(line) {
			return false
		}
		found = true
	}
	return found
}

func stripInboundDisplay(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "在钉钉会话中的消息："); i >= 0 {
		s = strings.TrimSpace(s[i+len("在钉钉会话中的消息："):])
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "钉钉会话消息："))
	return s
}

// KeepWorkUtterances drops thanks / OK lines from a mixed window so a real
// ask is not silenced because later ACKs landed on the same job.
func KeepWorkUtterances(turn Turn) Turn {
	utterances := windowUtterances(turn)
	if len(utterances) == 0 {
		return turn
	}
	work := make([]WindowUtterance, 0, len(utterances))
	for _, u := range utterances {
		if allAckText(u.Text) {
			continue
		}
		work = append(work, u)
	}
	if len(work) == 0 || len(work) == len(utterances) {
		return turn
	}
	turn.Utterances = work
	parts := make([]string, 0, len(work))
	for _, u := range work {
		parts = append(parts, u.Text)
	}
	turn.Message = strings.Join(parts, "\n")
	return turn
}

func isAckOrStopReply(raw string) bool {
	s := stripMentionsAndSpace(raw)
	if s == "" {
		return false
	}
	switch s {
	case "好的", "好", "嗯", "行", "收到", "谢谢", "谢谢你", "辛苦了", "没事",
		"ok", "OK", "Okay", "okay",
		"不用回复了", "不用回了", "先忙你的", "不用回复":
		return true
	default:
		return false
	}
}

func stripMentionsAndSpace(raw string) string {
	fields := strings.Fields(stripMentionTokens(strings.TrimSpace(raw)))
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") && len(fields[0]) > 1 {
		fields = fields[1:]
	}
	joined := strings.Join(fields, "")
	return strings.Trim(joined, "。！？!?.~…，,、 ")
}
