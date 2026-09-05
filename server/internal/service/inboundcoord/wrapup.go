package inboundcoord

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/assoc"
)

// TaskFinishedAlreadyToldScene reports whether this sandbox run already
// sent IM on the inbound conversation. Wrap-up must stay silent then.
//
// Event.TaskID is the assoc graph task node, not agent_task_queue.id.
// ListEventsByScene already scoped the rows to this cid and to
// since=task.StartedAt, so any outbound on this scene is "already told".
func TaskFinishedAlreadyToldScene(events []assoc.Event, sceneCID, taskID string) bool {
	cid := assoc.NormalizeConversationID(sceneCID)
	if cid == "" {
		return false
	}
	for _, ev := range events {
		if ev.Direction != assoc.DirOutbound {
			continue
		}
		if assoc.NormalizeConversationID(ev.SceneKey) != cid {
			continue
		}
		return true
	}
	return false
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
