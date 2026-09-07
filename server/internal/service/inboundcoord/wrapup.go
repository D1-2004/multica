package inboundcoord

import (
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
)

// TaskFinishedAlreadyToldScene reports whether THIS issue already sent IM on
// the inbound conversation. Wrap-up must stay silent then.
//
// Event.TaskID is the assoc graph task node. matterTaskIDs are that Issue's
// assoc tasks (plus the agent_task_queue id). A different Issue's outbound
// on the same cid must not suppress this wrap-up.
func TaskFinishedAlreadyToldScene(events []assoc.Event, sceneCID string, matterTaskIDs []string) bool {
	cid := assoc.NormalizeConversationID(sceneCID)
	if cid == "" || len(matterTaskIDs) == 0 {
		return false
	}
	owned := make(map[string]struct{}, len(matterTaskIDs))
	for _, id := range matterTaskIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			owned[id] = struct{}{}
		}
	}
	if len(owned) == 0 {
		return false
	}
	for _, ev := range events {
		if ev.Direction != assoc.DirOutbound {
			continue
		}
		if assoc.NormalizeConversationID(ev.SceneKey) != cid {
			continue
		}
		if _, ok := owned[strings.TrimSpace(ev.TaskID)]; ok {
			return true
		}
	}
	return false
}

// WrapupAlreadyToldSince is when wrap-up should start looking for outbound
// on this cid. Follow-up comment tasks start after the previous sandbox
// already spoke; using StartedAt would miss that outbound and ping again.
func WrapupAlreadyToldSince(taskCreatedAt, issueCreatedAt time.Time) time.Time {
	since := taskCreatedAt
	if !issueCreatedAt.IsZero() && (since.IsZero() || issueCreatedAt.Before(since)) {
		since = issueCreatedAt
	}
	if since.IsZero() {
		return time.Now().Add(-2 * time.Hour)
	}
	return since
}

// TaskFinishedWrapupRedundant is a Host filter for wrap-up speech that only
// points at a message already in this chat, or invents a DM to the delegator.
func TaskFinishedWrapupRedundant(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	for _, needle := range []string{
		"查收",
		"已发到群里",
		"已经发到群",
		"已在群里",
		"发到群里了",
		"结果已发送",
		"结果已发",
		"请看群",
		"私信你",
		"私信向你",
		"已私信",
		"已问",
		"已确认",
		"已补充告知",
		"等他回",
		"等他回复",
	} {
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}

// FilterTaskFinishedWrapup turns a redundant wrap-up reply into silence.
func FilterTaskFinishedWrapup(decision Decision) Decision {
	if decision.Action != ActionReply {
		return decision
	}
	if !TaskFinishedWrapupRedundant(decision.UserText) {
		return decision
	}
	decision.Action = ActionSilence
	decision.UserText = ""
	if strings.TrimSpace(decision.Reason) == "" {
		decision.Reason = "redundant_wrapup"
	}
	return decision
}
