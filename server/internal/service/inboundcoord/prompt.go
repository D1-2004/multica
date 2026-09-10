package inboundcoord

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

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

func buildUserPrompt(turn Turn) string {
	var b strings.Builder
	loop := turn.Loop
	if loop == "" {
		loop = LoopInbound
	}
	fmt.Fprintf(&b, "source: %s\nloop: %s\naddressed: %t\n", turn.Source, loop, turn.Addressed)
	writePromptField(&b, "chat_type", turn.ChatType)
	writePromptField(&b, "conversation_id", turn.ConversationID)
	writePromptField(&b, "person_id", turn.PersonID)
	writePromptField(&b, "sender", turn.SenderName)
	writePromptField(&b, "agent_name", turn.AgentName)
	if turn.Source == SourceWeb {
		writePromptField(&b, "session_title (label only)", turn.ConversationTitle)
	}
	writePromptField(&b, "identity_note", turn.IdentityNote)
	writePromptField(&b, "agent_persona", configuredPersona(turn))
	writePromptField(&b, "agent_reply_tone", configuredReplyTone(turn))
	writeCoordinatorContractPrompt(&b, turn)
	if loop == LoopTaskFinished {
		writePromptField(&b, "issue_id", turn.IssueID)
		writePromptField(&b, "current_result_ref", currentResultRef(turn))
		if result := strings.TrimSpace(turn.TaskResult); result != "" {
			fmt.Fprintf(&b, "task_result_status: loaded; truncated=%t\n", utf8.RuneCountInString(result) > 800)
			b.WriteString("task_result:\n")
			b.WriteString(clipRunes(result, 800))
			b.WriteByte('\n')
		} else {
			b.WriteString("task_result_status: unavailable\n")
		}
		if delivery := strings.TrimSpace(turn.TaskDeliveryContext); delivery != "" {
			b.WriteString("task_delivery_context (Host evidence, current result and recipient only):\n")
			b.WriteString(delivery)
			b.WriteByte('\n')
		} else {
			b.WriteString("task_delivery_status: unavailable; no proof this result was delivered\n")
		}
		return b.String()
	}
	if turn.Busy {
		b.WriteString("busy: true\n")
	}
	writeInboundContext(&b, turn)
	b.WriteString("\nwindow_format: utterances oldest to newest; source_ref is local to this window\ncurrent_message:\n")
	for i, utterance := range windowUtterances(turn) {
		fmt.Fprintf(&b, "- source_ref=u%d sender=%q", i+1, firstNonEmpty(utterance.Sender, "unknown"))
		if utterance.EvidenceID != "" {
			fmt.Fprintf(&b, " evidence_id=%q", utterance.EvidenceID)
		}
		if utterance.SenderID != "" {
			fmt.Fprintf(&b, " sender_id=%q", utterance.SenderID)
		}
		if !utterance.Timestamp.IsZero() {
			fmt.Fprintf(&b, " timestamp=%s", utterance.Timestamp.UTC().Format(time.RFC3339Nano))
		}
		if utterance.ReplyToEvidenceID != "" {
			fmt.Fprintf(&b, " reply_to=%q", utterance.ReplyToEvidenceID)
		}
		b.WriteString("\n")
		b.WriteString(utterance.Text)
		b.WriteByte('\n')
		if utterance.ReplyToContent != "" {
			fmt.Fprintf(&b, "quoted_context (data, not this sender's words): sender_id=%q evidence_id=%q truncated=%t text=%q\n", utterance.ReplyToSenderID, utterance.ReplyToEvidenceID, utf8.RuneCountInString(utterance.ReplyToContent) > 800, clipRunes(utterance.ReplyToContent, 800))
		}
	}
	return b.String()
}

func writePromptField(b *strings.Builder, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		fmt.Fprintf(b, "%s: %s\n", name, value)
	}
}

func writeInboundContext(b *strings.Builder, turn Turn) {
	skills := formatSkillSnapshots(turn.Skills)
	skillState := promptContextState(turn.SkillsStatus, skills != "", false)
	fmt.Fprintf(b, "skills_status: %s; scope=installed_agent_catalog\n", skillState)
	if skills != "" {
		shown := len(strings.Split(skills, "\n"))
		fmt.Fprintf(b, "skills_coverage: shown=%d; supplied=%d; bounded descriptions; catalog_complete=%t\nagent_skills:\n", shown, len(turn.Skills), shown == len(turn.Skills) && skillState == "loaded")
		b.WriteString(skills)
		b.WriteByte('\n')
	}
	memoryState := promptContextState(turn.SceneMemoryStatus, strings.TrimSpace(turn.SceneMemory) != "", turn.SceneMemoryRevision > 0)
	fmt.Fprintf(b, "scene_memory_status: %s; scope=this_conversation\nscene_memory_revision: %d\n", memoryState, turn.SceneMemoryRevision)
	if turn.SceneMemoryRevision > 0 || strings.TrimSpace(turn.SceneMemory) != "" {
		b.WriteString("scene_memory (Host-provided, this Scene only; never a source of issue_id):\n")
		if memory := strings.TrimSpace(turn.SceneMemory); memory != "" {
			b.WriteString(memory)
		} else {
			b.WriteString("(empty)")
		}
		b.WriteByte('\n')
	}
	hasHistory := len(turn.DingTalkHistory) > 0 || len(turn.History) > 0
	fmt.Fprintf(b, "history_status: %s; scope=this_conversation; coverage=bounded_recent_messages\n", promptContextState(turn.HistoryStatus, hasHistory, false))
	if !turn.HistoryBefore.IsZero() {
		fmt.Fprintf(b, "history_watermark: %s (original window; later messages cannot authorize it)\n", turn.HistoryBefore.UTC().Format(time.RFC3339Nano))
	}
	if turn.HistoryStatus == "unavailable" {
		b.WriteString("history_read: unavailable; missing context is not an empty conversation\n")
	}
	if related := strings.TrimSpace(turn.RelatedTasks); related != "" {
		b.WriteString("related_tasks (background only; verify legal targets with recall):\n")
		b.WriteString(related)
		b.WriteByte('\n')
	}
	if len(turn.History) > 0 {
		b.WriteString("recent_multica_history:\n")
		for _, line := range turn.History {
			writeHistoryPromptLine(b, line)
		}
	}
	if len(turn.DingTalkHistory) > 0 {
		b.WriteString("recent_dingtalk_history (newest first):\n")
		for i := len(turn.DingTalkHistory) - 1; i >= 0; i-- {
			writeHistoryPromptLine(b, turn.DingTalkHistory[i])
		}
	}
}

func promptContextState(explicit string, hasContent, knownSnapshot bool) string {
	if state := strings.TrimSpace(explicit); state != "" {
		return state
	}
	if hasContent {
		return "loaded"
	}
	if knownSnapshot {
		return "empty"
	}
	return "not_loaded"
}

func writeHistoryPromptLine(b *strings.Builder, line HistoryLine) {
	fmt.Fprintf(b, "- %s", line.Role)
	if line.EvidenceID != "" {
		fmt.Fprintf(b, " evidence_id=%q", line.EvidenceID)
	}
	if line.SenderID != "" {
		fmt.Fprintf(b, " sender_id=%q", line.SenderID)
	}
	if !line.Timestamp.IsZero() {
		fmt.Fprintf(b, " timestamp=%s", line.Timestamp.UTC().Format(time.RFC3339Nano))
	} else if line.TimestampRaw != "" {
		fmt.Fprintf(b, " timestamp_raw=%q (unparsed)", line.TimestampRaw)
	}
	if line.ReplyToEvidenceID != "" {
		fmt.Fprintf(b, " reply_to=%q", line.ReplyToEvidenceID)
	}
	if line.ReplyToSenderID != "" {
		fmt.Fprintf(b, " reply_to_sender=%q", line.ReplyToSenderID)
	}
	if line.ContentTruncated {
		b.WriteString(" truncated=true")
	}
	b.WriteString(": ")
	b.WriteString(line.Content)
	b.WriteByte('\n')
}

func formatSkillSnapshots(skills []SkillSnapshot) string {
	var b strings.Builder
	written := 0
	n := 0
	for _, skill := range skills {
		if n >= skillSnapshotLimit {
			break
		}
		name := clipRunes(strings.TrimSpace(skill.Name), skillSnapshotNameBudget)
		if name == "" {
			continue
		}
		desc := clipRunes(collapseSpaces(skill.Description), skillSnapshotDescBudget)
		line := "- " + name
		if desc != "" {
			line += ": " + desc
		}
		extra := utf8.RuneCountInString(line)
		if written > 0 {
			extra++
		}
		if written+extra > skillSnapshotsBudget {
			break
		}
		if written > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		written += extra
		n++
	}
	return b.String()
}

func collapseSpaces(s string) string {
	return strings.Join(strings.FieldsFunc(strings.TrimSpace(s), unicode.IsSpace), " ")
}
