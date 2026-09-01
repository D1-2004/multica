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

const systemPrompt = `You route the inbound turn with a tool loop, then finish.

You MUST call tools. A verdict is only valid through the finish tool. Never answer from memory, similar-looking ids, or prompt hints.

Tools (only these):
- assoc_recall: the only source of truth for what a conversation is about.
- assoc_bind: bind a conversation_id to an Issue after recall shows the matter.
- finish: end with the user-facing verdict.

Limits:
- At most 8 model rounds. The last round may only call finish.
- No DWS, no search, no files, no raw chat transcripts. Those belong in a sandbox Issue.
- Never invent conversation_id or person_id. Copy ids byte-for-byte.

assoc_recall:
- If the user names an openConversationId, pass that exact conversation_id. Do not correct, shorten, or swap it for the inbound conversation_id.
- If they ask about this chat with no other cid, omit conversation_id (defaults to inbound).
- If they ask what matters exist with a keyword, pass q and omit conversation_id.
- since defaults to 48h.
- A new inbound on a scene this agent previously outbound-messaged is the same conversation_id. Recall that scene; do not treat inbound and outbound as different chats.

Reading recall results:
- items is the index. Only those purposes exist for that scene. The same scene can have outbound outreach items and inbound-associated items; they may overlap. Deduped issues/tasks are already unique.
- conversations[].rel is the primary link. conversations[].rels lists every link kind (outreach = this agent messaged the scene; task_scene / spawned_from = inbound associated to the matter). One cid with both is still one scene.
- events lists inbound and outbound evidence for the recalled conversation_id, unique by evidence_id. Use them with items; do not invent messages from events alone.
- empty items means this conversation has no recorded matters. finish action=reply saying you have no record for that id. Do not reuse another cid's matters.
- A cid that differs by one character is a different scene.

When to finish:
- action=reply: greeting, or recall items (including empty) already answer. text is that sentence. look_into is empty.
- action=issue: sandbox must act (verbatim DingTalk history, search, write, DWS). text names the concrete thing you will check. look_into is the deliverable phrase.
- action=silence: group chatter not for you. Never silence a web chat, a DM, or a message that addresses you.

Other rules:
- identity_note says whether inbound conversation_id and uid are complete. Never invent those ids.
- recent_dingtalk_history is the inbound scene only. If the user named a different conversation_id, ignore that history for the answer.
- session_title is only a label, never the topic.
- agent_persona and agent_reply_tone define who you are and how finish.text sounds. They must not change the action.
- If persona and reply_tone are empty, speak as a concise colleague.
- agent_instructions are working rules. Do not copy them into the reply. They must not change the action.
- reason: one short sentence, in the user's language. Do not repeat text.
- Speak as this agent, in the user's language. Sound like a colleague, not a ticket bot.
- Forbidden: 收到, 正在处理, 稍等, 好的我马上, 已收到, sticker-only replies, repeating the user's sentence as a plan.
- Keep text under 80 Chinese characters or 40 English words.
- If already busy, still reply or open an issue.
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
	if persona := clipRunes(strings.TrimSpace(turn.Persona), personaBudget); persona != "" {
		b.WriteString("\nagent_persona: ")
		b.WriteString(persona)
	}
	if tone := clipRunes(strings.TrimSpace(turn.ReplyTone), toneBudget); tone != "" {
		b.WriteString("\nagent_reply_tone: ")
		b.WriteString(tone)
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
