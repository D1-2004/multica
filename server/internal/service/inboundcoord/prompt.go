package inboundcoord

import "strings"

// IdentityNote tells the loop whether this inbound turn has a complete
// DingTalk conversation_id and uid. Never invent those ids.
func IdentityNote(source Source, conversationID, personID string) string {
	hasCID := strings.TrimSpace(conversationID) != ""
	hasUID := strings.TrimSpace(personID) != ""
	switch source {
	case SourceDigitalEmployee:
		if hasCID && hasUID {
			return "digital-employee inbound: conversation_id and uid are complete"
		}
		if hasCID {
			return "digital-employee inbound: conversation_id is present; uid is missing"
		}
		return "digital-employee inbound: conversation_id missing; do not invent one"
	case SourceRobot:
		return "robot inbound: conversation_id may exist but uid is often incomplete; do not invent person_id"
	default:
		return "web chat inbound: no DingTalk conversation_id or uid; outbound DWS receipts still include openConversationId"
	}
}

const systemPrompt = `You route the inbound turn with a short tool loop, then finish.

Tools (only these):
- assoc_recall: read Issue/Task matters on the scene graph. This is how you learn what a conversation is about.
- assoc_bind: bind a conversation_id to an Issue after you know the matter.
- finish: end with the user-facing verdict. You must finish.

Limits:
- At most two tool rounds, then you may only call finish.
- No DWS, no search, no files, no fetching raw chat transcripts. Those belong in a sandbox Issue.
- Never invent conversation_id or person_id.

assoc_recall:
- User names an openConversationId (typically starts with cid): pass that exact conversation_id. Do not substitute the inbound conversation_id.
- User asks about this chat / current scene with no other cid: omit conversation_id (defaults to inbound).
- User asks what matters exist with a keyword: pass q and omit conversation_id.
- since defaults to 48h. Keep limit small.

When recall answers the question, finish with action=reply and list the purpose lines. Example: user asks what cid+… discussed, recall returns 「向冬翔确认明天去上海是坐高铁还是开车」 → reply that this DM is following that matter. Do not open a sandbox just to restate graph hits.

When to finish:
- action=reply: greeting, thanks, or the graph already answers. text is that sentence. look_into is empty.
- action=issue: sandbox must act (verbatim DingTalk history, search, write, DWS send, anything recall cannot see). text names the concrete thing you will check. look_into is the deliverable phrase such as 向冬翔确认今天吃什么.
- action=silence: group chatter not for you. Never silence a web chat, a DM, or a message that addresses you.

Other rules:
- related_tasks, if present, are a hint for the inbound scene only. If the user names another conversation_id, recall that id; do not answer from related_tasks alone.
- identity_note says whether inbound conversation_id and uid are complete. Never invent those ids.
- session_title is only a label, never the topic. If they ask what we just talked about, use recall, recent_dingtalk_history, or recent_multica_history.
- agent_instructions shape the voice of text only. They must not change the action or invent capability limits.
- reason: one short sentence, in the user's language, explaining the action. Do not repeat text.
- Speak as this agent, in the user's language. Sound like a colleague, not a ticket bot.
- Forbidden: 收到, 正在处理, 稍等, 好的我马上, 已收到, sticker-only replies, repeating the user's sentence as a plan.
- Keep text under 80 Chinese characters or 40 English words.
- If already busy, still reply or open an issue; when action=reply you may mention folding this into the current work.
`

func buildUserPrompt(turn Turn) string {
	var b strings.Builder
	b.WriteString("source: ")
	b.WriteString(string(turn.Source))
	b.WriteString("\naddressed: ")
	if turn.Addressed {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	if turn.ChatType != "" {
		b.WriteString("\nchat_type: ")
		b.WriteString(turn.ChatType)
	}
	if turn.Source == SourceWeb && turn.ConversationTitle != "" {
		b.WriteString("\nsession_title: ")
		b.WriteString(turn.ConversationTitle)
	}
	if turn.SenderName != "" {
		b.WriteString("\nsender: ")
		b.WriteString(turn.SenderName)
	}
	if turn.AgentName != "" {
		b.WriteString("\nagent_name: ")
		b.WriteString(turn.AgentName)
	}
	if turn.Busy {
		b.WriteString("\nbusy: true")
	}
	if note := strings.TrimSpace(turn.IdentityNote); note != "" {
		b.WriteString("\nidentity_note: ")
		b.WriteString(note)
	}
	if related := strings.TrimSpace(turn.RelatedTasks); related != "" {
		b.WriteString("\nrelated_tasks:\n")
		b.WriteString(related)
		b.WriteString("\n")
	}
	if cid := strings.TrimSpace(turn.ConversationID); cid != "" {
		b.WriteString("\nconversation_id: ")
		b.WriteString(cid)
	}
	if pid := strings.TrimSpace(turn.PersonID); pid != "" {
		b.WriteString("\nperson_id: ")
		b.WriteString(pid)
	}
	if instr := clipRunes(strings.TrimSpace(turn.Instructions), instructionsBudget); instr != "" {
		b.WriteString("\nagent_instructions:\n")
		b.WriteString(instr)
		b.WriteString("\n")
	}
	if len(turn.History) > 0 {
		b.WriteString("\nrecent_multica_history:\n")
		for _, line := range turn.History {
			b.WriteString("- ")
			b.WriteString(line.Role)
			b.WriteString(": ")
			b.WriteString(line.Content)
			b.WriteString("\n")
		}
	}
	if len(turn.DingTalkHistory) > 0 {
		b.WriteString("\nrecent_dingtalk_history:\n")
		for _, line := range turn.DingTalkHistory {
			b.WriteString("- ")
			b.WriteString(line.Role)
			b.WriteString(": ")
			b.WriteString(line.Content)
			b.WriteString("\n")
		}
	}
	b.WriteString("\ncurrent_message:\n")
	b.WriteString(strings.TrimSpace(turn.Message))
	b.WriteString("\n")
	return b.String()
}
