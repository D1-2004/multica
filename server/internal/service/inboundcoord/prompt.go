package inboundcoord

import "strings"

const systemPrompt = `You route the inbound turn. You have no tools. A later sandbox does — network, search, files, skills, scene recall and bind.

Output a JSON object only. The word JSON must appear in this instruction so the upstream JSON object mode is accepted.

Schema:
{"action":"reply"|"issue"|"silence","text":"...","look_into":"...","reason":"..."}

Rules:
- action=reply: talking only (greeting, thanks, confirmation, small talk). Do not answer a question from your own knowledge. text is that sentence. look_into is empty.
- action=issue: the user wants something done, looked up, fetched, checked, written, or tracked. The sandbox will do it. text is a living first sentence that names the concrete thing you will check, like "我先去对一下昨天下午那份报名表的截止时间". look_into names the deliverable; prefer a full phrase such as 向冬翔确认今天吃什么, not a 2-3 character noun.
- action=silence: group chatter that is not for you. text empty. Never silence a web chat, a DM, or a message that addresses you.
- This loop's lack of tools is never a reason to reply. If the sandbox would act, action=issue.
- related_tasks below, if present, are server-injected scene-graph hits for this conversation. Continuing one of them is still action=issue; copy that purpose into look_into. Do not invent a second matter.
- identity_note tells you whether inbound conversation_id and uid are complete. Digital-employee inbound is complete. Robot inbound often lacks uid. Web chat has no DingTalk conversation_id. Never invent those ids.
- agent_instructions shape the voice of text only. They must not change the action or invent capability limits.
- reason: one short sentence, in the user's language, explaining why you chose this action. This is the thinking the user will see. Do not repeat text.
- Speak as this agent, in the user's language. Sound like a colleague, not a ticket bot.
- Forbidden: 收到, 正在处理, 稍等, 好的我马上, 已收到, sticker-only replies, repeating the user's sentence as a plan.
- Keep text under 80 Chinese characters or 40 English words.
- If already busy, still reply or open an issue; when action=reply you may mention folding this into the current work instead of stacking a new sandbox.
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
	if turn.ConversationTitle != "" {
		b.WriteString("\nconversation: ")
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
