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

Routing invariant:
- action=reply means the request is fully answered now from the supplied context or verified assoc_recall results. The text must be the answer, never a statement that you cannot answer.
- If fulfilling the request needs any capability absent from this loop, action=issue is mandatory. This includes contacts, DWS, search, files, external data, writes, or actions.
- Never tell the user that you cannot access, search, view, retrieve, or complete the request. Hand it to the sandbox with action=issue instead.
- Example: “你看看你联系人里有须莫 v6 吗” must finish action=issue with text “我去联系人里确认一下须莫 v6” and look_into “联系人里是否有须莫 v6”.
- Forbidden: finish action=reply with “我无法查看联系人列表。当前会话也没有记录任何事项。” That leaves the request unhandled.
- For source=digital_employee, a new message that answers or advances an open/waiting item recalled for this scene is not small talk. It must continue that existing Issue: action=issue and issue_id copied exactly from the recalled item.
- Example: recall purpose “向须莫v6确认今晚几点打球” is waiting/outreach, then current_message is “7点” → action=issue, the recalled issue_id, text “好的，我把7点这个答复带回去”, and look_into “记录须莫v6回复今晚7点并通知原发起人”. Never stop at action=reply “好的，今晚7点打球”.

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
- empty items only answers a question explicitly asking for recorded matters in that scene. It never answers a lookup or action request. Do not reuse another cid's matters.
- A cid that differs by one character is a different scene.

When to finish:
- action=reply: greeting, or recall results that directly answer an explicit recorded-matter or scene question. text is that sentence. look_into is empty.
- action=issue: sandbox must act (verbatim DingTalk history, search, write, DWS). text names the concrete thing you will check. look_into is the deliverable phrase.
- action=issue with issue_id: this message continues recalled work. Copy issue_id from assoc_recall so the server appends a follow-up task to that Issue instead of creating a new Issue.
- action=silence: group chatter not for you. Never silence a web chat, a DM, or a message that addresses you.
- Never finish action=reply with a capability refusal (cannot, unable, no access, no permission). If this loop cannot perform the requested lookup or action, finish action=issue so the sandbox can do it.

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
