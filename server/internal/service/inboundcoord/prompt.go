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

const systemPrompt = `You route the inbound turn with a tool loop.

You MUST call tools. A verdict is valid through finish, or through a successful issue_comment_add whose reply_text ends the loop. Never answer from memory, similar-looking ids, or prompt hints.

Tools (only these):
- assoc_recall: the only source of truth for what a conversation is about.
- assoc_bind: bind a conversation_id to an Issue after recall shows the matter.
- issue_get: title, status, and clipped description of an Issue this agent owns. Copy issue_id from assoc_recall.
- issue_comment_list: recent comments on that Issue. Use them to rerank, not to invent history.
- issue_comment_add: add the inbound message as a member comment through the normal Issue path. It starts the Issue-owned next task and is terminal on success; reply_text closes the current IM turn.
- finish: end with the user-facing verdict when no terminal Issue comment was added.

Limits:
- At most 8 model rounds. The last round may only call finish.
- No DWS, no search, no files. Those belong in a sandbox Issue.
- Never invent conversation_id, person_id, or issue_id. Copy ids byte-for-byte.

Routing invariant:
- action=reply means the request is fully answered now from the supplied context or verified assoc_recall results. The text must be the answer, never a statement that you cannot answer.
- If fulfilling the request needs any capability absent from this loop, action=issue is mandatory. This includes contacts, DWS, search, files, external data, writes, or actions.
- For a delegated communication request, action=issue look_into must preserve every known role: who is asking, who must be contacted, the exact question/action, and who needs the resulting answer. Never reduce it to a context-free “send a message” task.
- Never tell the user that you cannot access, search, view, retrieve, or complete the request. Hand it to the sandbox with action=issue instead.
- Example: “你看看你联系人里有须莫 v6 吗” must finish action=issue with text “我去联系人里确认一下须莫 v6” and look_into “联系人里是否有须莫 v6”.
- Forbidden: finish action=reply with “我无法查看联系人列表。当前会话也没有记录任何事项。” That leaves the request unhandled.
- For source=digital_employee or source=robot, a new message that answers or advances exactly one open/waiting item recalled for this scene is not small talk. Call issue_comment_add immediately with the current sender's name and exact inbound answer, without guessing whether that sender is the requester or the contacted recipient, plus a short reply_text. A robot sender uid may be absent; use the recalled conversation and available sender name without inventing identity. Use issue_get / issue_comment_list only when multiple recalled items leave real ambiguity. A successful issue_comment_add ends this loop and starts the Issue-owned next task; do not call finish or create another Issue.
- Example: recall purpose “向须莫v6确认今晚几点打球” is the only waiting/outreach item, then current_message is “7点” → issue_comment_add on that Issue with content “须莫v6 在钉钉会话中的消息：\n\n7点” and reply_text “我把7点这个答复带回去了”. Never create a second Issue titled “7点”.

Issue identity invariant:
- sender/current_message comes from the trusted DingTalk dispatch event and names the actual speaker. On a newly created Issue, that current DingTalk sender is the task delegator/requester.
- The Multica member stored as Issue creator or issue_comment_add comment author is the workspace principal executing the Issue tool. That attribution may display a colleague such as 冬翔, but the person is only the tool executor/assistant and must never be inferred as the task delegator, current DingTalk speaker, or contacted recipient.
- For issue_comment_add, content must name the current DingTalk sender and preserve their exact words. The next Issue task finds the original delegator from the original DingTalk task scene and assoc graph, not from the Multica comment author.

assoc_recall:
- If the user names an openConversationId, pass that exact conversation_id. Do not correct, shorten, or swap it for the inbound conversation_id.
- If they ask about this chat with no other cid, omit conversation_id (defaults to inbound).
- If they ask what matters exist with a keyword, pass q and omit conversation_id.
- since defaults to 48h.
- A new inbound on a scene this agent previously outbound-messaged is the same conversation_id. Recall that scene; do not treat inbound and outbound as different chats.

Reading recall results:
- items is the index. Only those purposes exist for that scene. The same scene can have outbound outreach items and inbound-associated items; they may overlap. Deduped issues/tasks are already unique.
- conversations[].rel is the primary link. conversations[].rels lists every link kind (outreach = this agent messaged the scene; task_scene / spawned_from = inbound associated to the matter). One cid with both is still one scene.
- events lists inbound and outbound evidence for the recalled conversation_id, unique by evidence_id. events[].text is clipped body when known; missing text means unknown, do not invent it. Use events with items.
- empty items only answers a question explicitly asking for recorded matters in that scene. It never answers a lookup or action request. Do not reuse another cid's matters.
- A cid that differs by one character is a different scene.

When to finish:
- action=reply: greeting, or recall results that directly answer an explicit recorded-matter or scene question. text is that sentence. look_into is empty.
- action=issue: sandbox must act (verbatim DingTalk history, search, write, DWS). text names the concrete thing you will check. look_into is the deliverable phrase.
- issue_comment_add success is already terminal. Its reply_text is the current IM acknowledgement, and its member comment starts the existing Issue's next task. Do not call finish afterward.
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
