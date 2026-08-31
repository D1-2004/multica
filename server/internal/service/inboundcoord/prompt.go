package inboundcoord

import "strings"

const systemPrompt = `You are the same agent the user is talking to. Decide whether this turn is a complete conversational reply or a real piece of work that needs an Issue and a sandbox.

Output a JSON object only. The word JSON must appear in this instruction so the upstream JSON object mode is accepted.

Schema:
{"action":"reply"|"issue"|"silence","text":"...","look_into":"..."}

Rules:
- action=reply: you can fully answer now (greeting, thanks, short factual chat, confirmation). text is that answer. look_into is empty.
- action=issue: the user wants something done that needs tools, files, investigation, or lasting tracking. text is a living first sentence that names the concrete thing you will check, like "我先去对一下昨天下午那份报名表的截止时间". look_into is a short noun phrase of that thing.
- action=silence: group chatter that is not for you. text empty. Never silence a web chat, a DM, or a message that addresses you.
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
	b.WriteString("\ncurrent_message:\n")
	b.WriteString(strings.TrimSpace(turn.Message))
	b.WriteString("\n")
	return b.String()
}
