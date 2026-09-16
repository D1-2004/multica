package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const toolConversationReplies = "render_conversation_replies"
const conversationReplyModel = "qwen3.8-max"
const conversationReplyTimeout = 12 * time.Second
const conversationReplyTokenBudget = 2048
const conversationHistoryBudget = 1800

type conversationReplySource struct {
	SourceRef        string `json:"source_ref"`
	Text             string `json:"text"`
	SenderID         string `json:"sender_id,omitempty"`
	ResponseRequired bool   `json:"response_required"`
}

type conversationReplyAction struct {
	ActionRef string                    `json:"action_ref"`
	Sources   []conversationReplySource `json:"sources"`
}

type conversationReplyHistory struct {
	Author    string `json:"author"`
	SenderID  string `json:"sender_id,omitempty"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp,omitempty"`
	Truncated bool   `json:"truncated"`
}

// A narrow response input deliberately excludes work records, scene memory,
// job instructions, persona identities and the candidate's unreviewed reply.
func conversationReplyInput(turn Turn, decision Decision) ([]conversationReplyAction, []conversationReplyHistory) {
	utterances := windowUtterances(turn)
	var actions []conversationReplyAction
	for i, action := range decision.CoordinationActions {
		if action.Kind != "acknowledge" || action.AckKind != "conversation" {
			continue
		}
		entry := conversationReplyAction{ActionRef: fmt.Sprintf("a%d", i+1)}
		for _, ref := range action.SourceRefs {
			for j, u := range utterances {
				if ref == fmt.Sprintf("u%d", j+1) {
					entry.Sources = append(entry.Sources, conversationReplySource{SourceRef: ref, Text: u.Text, SenderID: u.SenderID, ResponseRequired: sourceResponseRequired(turn, u)})
				}
			}
		}
		actions = append(actions, entry)
	}
	senders := map[string]bool{}
	for _, u := range utterances {
		if u.SenderID != "" {
			senders[u.SenderID] = true
		}
	}
	lines := append([]HistoryLine(nil), turn.DingTalkHistory...)
	if len(lines) == 0 {
		lines = append(lines, turn.History...)
	}
	// Preserve provider order when timestamps are unknown; otherwise use newest
	// dialogue first. Identity is never inferred from Role or a display name.
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Timestamp.IsZero() {
			return false
		}
		if lines[j].Timestamp.IsZero() {
			return true
		}
		return lines[i].Timestamp.After(lines[j].Timestamp)
	})
	remaining := conversationHistoryBudget
	var history []conversationReplyHistory
	for _, line := range lines {
		if len(history) >= 6 || remaining <= 0 {
			break
		}
		if strings.TrimSpace(line.Content) == "" {
			continue
		}
		author := "unknown"
		if line.SenderID != "" && turn.DWSUID != "" && line.SenderID == turn.DWSUID {
			author = "known_employee"
		} else if line.SenderID != "" && senders[line.SenderID] {
			author = "known_sender"
		}
		// The response writer cannot resolve opaque cross-domain identities.
		// Keep the complete history in routing/review, but do not let unowned
		// dialogue become this employee's self-description or activity record.
		if author == "unknown" {
			continue
		}
		text := line.Content
		if utf8.RuneCountInString(text) > remaining {
			text = string([]rune(text)[:remaining])
		}
		remaining -= utf8.RuneCountInString(text)
		stamp := line.TimestampRaw
		if !line.Timestamp.IsZero() {
			stamp = line.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		history = append(history, conversationReplyHistory{Author: author, SenderID: line.SenderID, Text: text, Timestamp: stamp, Truncated: line.ContentTruncated || utf8.RuneCountInString(line.Content) > utf8.RuneCountInString(text)})
	}
	return actions, history
}

// renderConversationReplies changes only selected conversation replies. The
// caller must still run finish_check and persist the resulting plan before
// sending; this helper provides no independent authority or effect path.
func (c *Coordinator) renderConversationReplies(ctx context.Context, turn Turn, decision *Decision, round int) error {
	if decision == nil || turn.Loop == LoopTaskFinished {
		return nil
	}
	actions, history := conversationReplyInput(turn, *decision)
	if len(actions) == 0 {
		return nil
	}
	refs := make([]string, len(actions))
	for i, action := range actions {
		if len(action.Sources) == 0 {
			return fmt.Errorf("conversation reply has no current sources")
		}
		refs[i] = action.ActionRef
	}
	background, _ := json.Marshal(map[string]any{"history": history, "history_scope": "Partial identity-filtered earlier dialogue, never the current request. Only stable-ID matches to this employee or current speakers are included; omitted or empty history does not prove an exchange never happened.", "reply_tone": configuredReplyTone(turn), "reply_author": map[string]any{"kind": "digital_employee", "account_owner_biography": "not_supplied", "personal_life_or_habits": "not_supplied", "everyday_advice": "offer options to the user; not claims of personal experience"}, "receiving_identity": map[string]any{"this_employee_is_current_recipient": true, "stable_uid_available": strings.TrimSpace(turn.DWSUID) != "", "activity_and_execution_facts_available": false}})
	current, _ := json.Marshal(map[string]any{"current_actions": actions, "scope": "Reply only to these current sources. Earlier history questions are not new requests. Each current action must be answered now."})
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(buildSystemPrompt(Turn{Loop: LoopConversationReply})), openai.UserMessage(string(background)), openai.UserMessage(string(current))}
	messageBytes, _ := json.Marshal(messages)
	inputHash := policyHash(string(messageBytes))
	tool := openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{Name: toolConversationReplies, Description: openai.String("Return only the requested conversation replies. This tool performs no work or delivery."), Parameters: shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"replies"}, "properties": map[string]any{"replies": map[string]any{"type": "array", "minItems": len(refs), "maxItems": len(refs), "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action_ref", "reply"}, "properties": map[string]any{"action_ref": stringEnum(refs), "reply": map[string]any{"type": "string", "minLength": 1, "maxLength": 600}}}}}}})
	readCtx, cancel := context.WithTimeout(ctx, conversationReplyTimeout)
	defer cancel()
	var generation *langfuse.Observation
	if trace := langfuse.TraceFromContext(ctx); trace != nil {
		manifest := policyManifest(Turn{Loop: LoopConversationReply})
		generation = trace.StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeGeneration, Name: fmt.Sprintf("coordinator.conversation_reply.%d", round+1), Model: conversationReplyModel, Input: messages, ModelParameters: map[string]any{"max_completion_tokens": conversationReplyTokenBudget, "timeout_ms": conversationReplyTimeout.Milliseconds(), "temperature": 0, "reasoning_effort": "low", "enable_thinking": true, "tool_choice": "auto"}, Metadata: map[string]any{"action_count": len(refs), "history_count": len(history), "action_refs": refs, "policy_version": manifest.PolicyVersion, "assembly_version": manifest.AssemblyVersion, "prompt_hash": manifest.PromptHash, "input_hash": inputHash, "modules": manifest.Modules}})
	}
	started := time.Now()
	completion, err := c.completeWithModelLimit(readCtx, conversationReplyModel, messages, []openai.ChatCompletionToolUnionParam{tool}, conversationReplyTokenBudget, 0, shared.ReasoningEffortLow)
	endRoundGeneration(generation, completion, err)
	if err != nil {
		return fmt.Errorf("conversation reply unavailable: %w", err)
	}
	replies, err := parseConversationReplies(completion, refs)
	if err != nil {
		return err
	}
	for i := range decision.CoordinationActions {
		if reply, ok := replies[fmt.Sprintf("a%d", i+1)]; ok {
			decision.CoordinationActions[i].Reply = reply
		}
	}
	decision.UserText = ComposeDecisionReplies(decision.CoordinationActions)
	slog.Info("inbound coordinator conversation reply rendered", append(coordinatorLogIndex(turn), "event", "inbound_coordinator_conversation_reply", "action_count", len(refs), "input_hash", inputHash, "elapsed_ms", time.Since(started).Milliseconds())...)
	return nil
}

func parseConversationReplies(completion *openai.ChatCompletion, refs []string) (map[string]string, error) {
	if completion == nil || len(completion.Choices) != 1 || completion.Choices[0].FinishReason == "length" {
		return nil, fmt.Errorf("conversation reply requires one complete result")
	}
	calls := functionToolCalls(completion.Choices[0].Message)
	if len(calls) != 1 || calls[0].Name != toolConversationReplies {
		return nil, fmt.Errorf("conversation reply requires its response tool")
	}
	var wire struct {
		Replies []struct {
			ActionRef string `json:"action_ref"`
			Reply     string `json:"reply"`
		} `json:"replies"`
	}
	d := json.NewDecoder(strings.NewReader(calls[0].Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil {
		return nil, fmt.Errorf("invalid conversation reply structure")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF || len(wire.Replies) != len(refs) {
		return nil, fmt.Errorf("conversation reply must cover exact action refs")
	}
	expected := map[string]bool{}
	for _, ref := range refs {
		expected[ref] = true
	}
	out := map[string]string{}
	for _, reply := range wire.Replies {
		text := strings.TrimSpace(reply.Reply)
		if !expected[reply.ActionRef] || out[reply.ActionRef] != "" || text == "" || utf8.RuneCountInString(text) > 600 {
			return nil, fmt.Errorf("conversation reply has an invalid action ref or text")
		}
		out[reply.ActionRef] = text
	}
	return out, nil
}
