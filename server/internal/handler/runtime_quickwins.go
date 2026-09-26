package handler

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var dingTalkCommandLocator = regexp.MustCompile(`^[A-Za-z0-9_+/=.-]{1,512}$`)

// This is a task-side projection of already-authoritative delivery facts. It
// neither grants sending authority nor changes the Coordinator's routing.
func taskDingTalkReplyCommand(raw []byte, taskID string) string {
	stored, ok := parsePersistedDispatchContext(raw)
	if !ok || stored.Source.Platform != "dingtalk" || stored.Domain != "channel" || stored.Outbound.Mode != protocol.DispatchOutboundModeDWS {
		return ""
	}
	message, conversation := dingTalkOriginFromStored(stored)
	if message == "" {
		message = firstDispatchMessageOpenMsgID(stored.EventData)
	}
	if conversation == "" {
		conversation = stored.EventData.Conversation.OpenConversationID
	}
	for _, id := range []string{message, conversation, taskID} {
		if !dingTalkCommandLocator.MatchString(id) {
			return ""
		}
	}
	return fmt.Sprintf("When this task requires a reply to its source, set reply_text to the authorized result and use the prepared command below. Keep the injected DWS identity; this target does not authorize unrelated messages. Verify the send receipt.\n```sh\ndws chat +messages-reply --group '%s' --message-id '%s' --content \"$reply_text\" --idempotency-key 'multica-task-%s' --yes --format json\n```", conversation, message, taskID)
}

// Small complete sets fit in one claim response. Large sets keep the existing
// per-skill cache/progressive download path; partial mixed delivery would lose
// inline bundles when an older daemon rebuilds Skills from SkillRefs.
func inlineSmallSkillSet(skills []service.AgentSkillData) bool {
	if len(skills) == 0 || len(skills) > 8 {
		return false
	}
	var size int64
	for _, skill := range skills {
		size += int64(len(skill.Content))
		for _, file := range skill.Files {
			size += int64(len(file.Content))
		}
		if size > 128*1024 {
			return false
		}
	}
	return true
}

func appendTaskReplyCommand(instruction, command string) string {
	if command == "" {
		return instruction
	}
	return strings.TrimSpace(instruction + "\n\n" + command)
}
