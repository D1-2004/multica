package inboundcoord

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
)

// SceneWindowMaxItems is how many distinct deliverables one Decide window
// may start or continue. A third real ask stays pending for the next window.
const SceneWindowMaxItems = 2

// MessageMention preserves trusted channel mention targets; empty UID does not
// prove who an open-id target is. Never infer identities from a display name.
type MessageMention struct {
	UID            string `json:"uid,omitempty"`
	OpenDingTalkID string `json:"open_dingtalk_id,omitempty"`
}

// WindowUtterance is one inbound line in the current scene window.
type WindowUtterance struct {
	Sender            string
	Mentions          []MessageMention
	Text              string
	EvidenceID        string
	Timestamp         time.Time
	SenderID          string
	ReplyToContent    string
	ReplyToSenderID   string
	ReplyToEvidenceID string
}

// WindowItem is one deliverable the window decided to handle.
type WindowItem struct {
	Reply      string   `json:"reply,omitempty"`
	IssueID    string   `json:"issue_id,omitempty"`
	SourceRefs []string `json:"source_refs,omitempty"`
	Basis      string   `json:"basis,omitempty"`
	Content    string   `json:"content,omitempty"`
	ActionKey  string   `json:"action_key,omitempty"`
	Delegator  string
	Purpose    string
	Intent     string
	LookInto   string
}

func windowUtterances(turn Turn) []WindowUtterance {
	if len(turn.Utterances) > 0 {
		out := make([]WindowUtterance, 0, len(turn.Utterances))
		for _, u := range turn.Utterances {
			if strings.TrimSpace(u.Text) == "" {
				continue
			}
			// Preserve per-message evidence. The turn's current sender may be
			// another person in a collected window, so it is not a fallback.
			out = append(out, u)
		}
		if len(out) > 0 {
			return out
		}
	}
	text := strings.TrimSpace(turn.Message)
	if text == "" {
		return nil
	}
	return []WindowUtterance{{
		Sender: turn.SenderName, Text: turn.Message, EvidenceID: turn.EvidenceID,
		Timestamp: turn.MessageTimestamp, SenderID: turn.PersonID,
	}}
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

func stripInboundDisplay(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "在钉钉会话中的消息："); i >= 0 {
		s = strings.TrimSpace(s[i+len("在钉钉会话中的消息："):])
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "钉钉会话消息："))
	return s
}

// ForWindowItem limits the executor handoff to the selected deliverable. An old
// persisted checkpoint has no per-item reply, so its saved acknowledgement stays.
func (d Decision) ForWindowItem(item WindowItem) Decision {
	d.Purpose, d.Intent, d.LookInto = item.Purpose, item.Intent, item.LookInto
	d.Items = []WindowItem{item}
	if item.Reply != "" {
		d.UserText = item.Reply
	}
	d.CoordinationActions = nil
	d.NonWorkRefs = nil
	return d
}

// mentionRelation compares trusted identifiers, not names or message text.
// Other-only mentions are not a veto: the same line can also invite the
// employee by name; that distinction belongs to the shared group policy.
func mentionRelation(turn Turn, u WindowUtterance) string {
	if u.Mentions == nil {
		return "unknown"
	}
	if len(u.Mentions) == 0 {
		return "none"
	}
	unknown := false
	for _, m := range u.Mentions {
		if util.DingTalkMentionMatchesUID(m.UID, m.OpenDingTalkID, turn.DWSUID) {
			return "includes_employee"
		}
		if m.UID == "" || turn.DWSUID == "" {
			unknown = true
			continue
		}
	}
	if unknown {
		return "unknown"
	}
	return "other_only"
}
