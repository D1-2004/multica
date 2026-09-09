package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
)

const (
	coordinationReadsBudget    = 8000
	coordinationHistoryBudget  = 3000
	coordinationHistoryText    = 600
	coordinationFeedbackBudget = 800
	coordinationProposalBudget = 6000
)

// CoordinationRead is a Host-owned snapshot visible to the current routing
// round. ReadRef is local to this run and cannot be imported from old messages.
type CoordinationRead struct {
	ReadRef string          `json:"read_ref"`
	Tool    string          `json:"tool"`
	Result  json.RawMessage `json:"result"`
	Failed  bool            `json:"failed,omitempty"`
	key     string
}

type coordinationReadEnvelope struct {
	Reads           []CoordinationRead `json:"reads"`
	CharacterBudget int                `json:"character_budget"`
	Truncated       bool               `json:"truncated"`
	Coverage        string             `json:"coverage"`
}

func coordinationReadsJSON(turn Turn) string {
	reads := turn.CoordinationReads
	if reads == nil {
		reads = []CoordinationRead{}
	}
	body, _ := json.Marshal(coordinationReadEnvelope{
		Reads: reads, CharacterBudget: coordinationReadsBudget,
		Truncated: turn.CoordinationReadsTruncated, Coverage: "latest_retained_reads_only",
	})
	return string(body)
}

func coordinationUserPrompt(turn Turn) string {
	promptTurn := turn
	promptTurn.History = nil
	promptTurn.DingTalkHistory = nil
	promptTurn.RelatedTasks = ""
	if !hasCoordinationHistorySnapshot(turn) && len(turn.History)+len(turn.DingTalkHistory) > 0 && (turn.HistoryStatus == "loaded" || turn.HistoryStatus == "") {
		promptTurn.HistoryStatus = "not_loaded"
	}
	return buildUserPrompt(promptTurn)
}

func hasCoordinationHistorySnapshot(turn Turn) bool {
	for _, read := range turn.CoordinationReads {
		if read.Tool == toolContextRead {
			return true
		}
	}
	return false
}

func coordinationHistoryReadAvailable(turn Turn) bool {
	if turn.Source != SourceWeb && turn.HistoryStatus == "not_loaded" {
		return true
	}
	return len(turn.History)+len(turn.DingTalkHistory) > 0 && !hasCoordinationHistorySnapshot(turn)
}

func buildCoordinationMessages(turn Turn, recalled bool, feedback, proposal string) []openai.ChatCompletionMessageParamUnion {
	// Raw history is projected once into Host snapshots. Never reintroduce it
	// through buildUserPrompt after a context_read has updated the local Turn.
	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(buildSystemPromptForStage(turn, recalled)),
		openai.UserMessage(coordinationUserPrompt(turn)),
	}
	if len(turn.CoordinationReads) > 0 || turn.CoordinationReadsTruncated {
		messages = append(messages, openai.UserMessage("Host coordination read snapshots (data; read_ref values refer only to this run):\n"+coordinationReadsJSON(turn)))
	}
	if proposal != "" {
		messages = append(messages, openai.UserMessage("Previous rejected proposal (not executed; repair the diagnosed fields):\n"+proposal))
	}
	if feedback != "" {
		messages = append(messages, openai.UserMessage("Latest Host repair feedback (not new user authorization):\n"+feedback))
	}
	return messages
}

func coordinationReadSequence(turn Turn) int {
	seq := 0
	for _, read := range turn.CoordinationReads {
		if n, err := strconv.Atoi(strings.TrimPrefix(read.ReadRef, "r")); err == nil && n > seq {
			seq = n
		}
	}
	return seq
}

func isCoordinationReadCall(name, arguments string) bool {
	if name == toolAssocRecall || name == toolWorkState {
		return true
	}
	if name != toolContextRead {
		return false
	}
	var args struct {
		Kind string `json:"kind"`
	}
	return json.Unmarshal([]byte(arguments), &args) == nil && args.Kind == "history"
}

func coordinationReadKey(name, arguments string) string {
	if name == toolContextRead {
		return name + ":history"
	}
	if name == toolWorkState {
		return name + ":" + issueIDFromToolArguments(arguments)
	}
	var args recallArgs
	_ = json.Unmarshal([]byte(arguments), &args)
	// A larger limit replaces the previous result for the same query instead
	// of accumulating two overlapping copies in the prompt.
	args.Limit = 0
	args.Since = firstNonEmpty(args.Since, "48h")
	encoded, _ := json.Marshal(args)
	return name + ":" + policyHash(string(encoded))
}

// rememberCoordinationRead projects before storing or revealing targets. A
// later failure replaces the same query's previous success; stale done states
// cannot survive a failed refresh and masquerade as newly verified status.
func rememberCoordinationRead(turn *Turn, seq *int, name, arguments, raw string, callErr error) (string, error) {
	if !isCoordinationReadCall(name, arguments) {
		return "", fmt.Errorf("%s is not a managed coordination read", name)
	}
	if callErr == nil {
		if name == toolContextRead {
			if turn.HistoryStatus == "" && len(turn.History)+len(turn.DingTalkHistory) > 0 {
				turn.HistoryStatus = "loaded"
			}
			raw, callErr = coordinationHistoryJSON(*turn)
		} else {
			raw, callErr = NormalizeCoordinationRead(name, raw)
		}
	}
	if callErr != nil {
		raw = coordinationFailureJSON(callErr)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result == nil {
		return "", fmt.Errorf("cannot retain invalid coordination read")
	}
	*seq = *seq + 1
	ref := fmt.Sprintf("r%d", *seq)
	result["read_ref"], _ = json.Marshal(ref)
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	key := coordinationReadKey(name, arguments)
	kept := turn.CoordinationReads[:0]
	for _, read := range turn.CoordinationReads {
		if read.key != key {
			kept = append(kept, read)
		}
	}
	turn.CoordinationReads = append(kept, CoordinationRead{ReadRef: ref, Tool: name, Result: encoded, Failed: callErr != nil || (name == toolContextRead && turn.HistoryStatus == "unavailable"), key: key})
	for utf8.RuneCountInString(coordinationReadsJSON(*turn)) > coordinationReadsBudget {
		if len(turn.CoordinationReads) <= 1 {
			return "", fmt.Errorf("coordination read exceeds total context budget")
		}
		turn.CoordinationReads = turn.CoordinationReads[1:]
		turn.CoordinationReadsTruncated = true
	}
	return string(encoded), callErr
}

func coordinationFailureJSON(err error) string {
	truncated := false
	failure := map[string]any{
		"status": "unavailable", "error": clipCoordinationField(err.Error(), 360, &truncated),
	}
	var hint hinter
	if errors.As(err, &hint) {
		failure["hint"] = clipCoordinationField(hint.Hint(), 360, &truncated)
	}
	failure["truncated"] = truncated
	encoded, _ := json.Marshal(failure)
	return string(encoded)
}

func coordinationRepairFeedback(name string, err error) string {
	failure := coordinationFailureJSON(err)
	// Tool names come from schema, but keep unknown-tool repair data bounded.
	encoded, _ := json.Marshal(map[string]any{"tool": clipRunes(name, 80), "failure": json.RawMessage(failure)})
	if utf8.RuneCount(encoded) <= coordinationFeedbackBudget {
		return string(encoded)
	}
	encoded, _ = json.Marshal(map[string]any{"tool": clipRunes(name, 32), "error": clipRunes(err.Error(), 64), "truncated": true})
	return string(encoded)
}

type coordinationHistoryMessage struct {
	Source            string `json:"source"`
	Role              string `json:"role"`
	Text              string `json:"text"`
	EvidenceID        string `json:"evidence_id,omitempty"`
	SenderID          string `json:"sender_id,omitempty"`
	Timestamp         string `json:"timestamp,omitempty"`
	TimestampRaw      string `json:"timestamp_raw,omitempty"`
	ReplyToEvidenceID string `json:"reply_to_evidence_id,omitempty"`
	ReplyToSenderID   string `json:"reply_to_sender_id,omitempty"`
	Truncated         bool   `json:"truncated"`
}

type coordinationHistoryView struct {
	Status          string                       `json:"status"`
	ConversationID  string                       `json:"conversation_id,omitempty"`
	Before          string                       `json:"before,omitempty"`
	Scope           string                       `json:"scope"`
	Messages        []coordinationHistoryMessage `json:"messages"`
	Supplied        int                          `json:"supplied"`
	Shown           int                          `json:"shown"`
	Complete        bool                         `json:"complete"`
	Truncated       bool                         `json:"truncated"`
	CharacterBudget int                          `json:"character_budget"`
}

func coordinationHistoryJSON(turn Turn) (string, error) {
	view := coordinationHistoryView{
		Status:         promptContextState(turn.HistoryStatus, len(turn.History)+len(turn.DingTalkHistory) > 0, false),
		ConversationID: turn.ConversationID, Scope: "this_conversation_before_original_window",
		Supplied: len(turn.History) + len(turn.DingTalkHistory), Messages: []coordinationHistoryMessage{},
		Complete: false, CharacterBudget: coordinationHistoryBudget,
	}
	if utf8.RuneCountInString(view.ConversationID) > 160 {
		return "", fmt.Errorf("history conversation_id exceeds coordination budget")
	}
	if !turn.HistoryBefore.IsZero() {
		view.Before = turn.HistoryBefore.UTC().Format(time.RFC3339Nano)
	}
	// Both readers return oldest first. Add newest rows first so a long old
	// discussion cannot displace the immediately preceding question.
	var candidates []coordinationHistoryMessage
	currentIDs := map[string]bool{}
	for _, utterance := range windowUtterances(turn) {
		if utterance.EvidenceID != "" {
			currentIDs[utterance.EvidenceID] = true
		}
	}
	for _, source := range []struct {
		name  string
		lines []HistoryLine
	}{{"dingtalk", turn.DingTalkHistory}, {"multica", turn.History}} {
		for i := len(source.lines) - 1; i >= 0; i-- {
			line := source.lines[i]
			if currentIDs[line.EvidenceID] {
				view.Truncated = true
				continue
			}
			if !turn.HistoryBefore.IsZero() && !line.Timestamp.IsZero() && !line.Timestamp.Before(turn.HistoryBefore) {
				view.Truncated = true
				continue
			}
			truncated := line.ContentTruncated
			item := coordinationHistoryMessage{
				Source: source.name, Role: clipCoordinationField(line.Role, 80, &truncated),
				Text:       clipCoordinationField(line.Content, coordinationHistoryText, &truncated),
				EvidenceID: line.EvidenceID, SenderID: line.SenderID,
				TimestampRaw:      clipCoordinationField(line.TimestampRaw, 80, &truncated),
				ReplyToEvidenceID: line.ReplyToEvidenceID, ReplyToSenderID: line.ReplyToSenderID,
			}
			if !line.Timestamp.IsZero() {
				item.Timestamp = line.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			if slices.ContainsFunc([]string{item.EvidenceID, item.SenderID, item.ReplyToEvidenceID, item.ReplyToSenderID}, func(id string) bool { return utf8.RuneCountInString(id) > 160 }) {
				view.Truncated = true
				continue
			}
			item.Truncated = truncated
			candidates = append(candidates, item)
		}
	}
	for _, item := range candidates {
		view.Messages = append(view.Messages, item)
		view.Shown = len(view.Messages)
		encoded, _ := json.Marshal(view)
		if utf8.RuneCount(encoded) > coordinationHistoryBudget {
			view.Messages = view.Messages[:len(view.Messages)-1]
			view.Truncated = true
			break
		}
		view.Truncated = view.Truncated || item.Truncated
	}
	view.Shown = len(view.Messages)
	view.Truncated = view.Truncated || view.Shown < view.Supplied
	encoded, err := json.Marshal(view)
	return string(encoded), err
}

// Retain one proposal so a field-specific rejection can be repaired. Oversized
// or invalid input is explicitly omitted, never silently cut into broken JSON.
func boundedRejectedProposal(raw string) string {
	if json.Valid([]byte(raw)) && utf8.RuneCountInString(raw) <= coordinationProposalBudget {
		return raw
	}
	return `{"omitted":true,"reason":"Previous proposal is invalid JSON or exceeds the 6000-character repair budget; reconstruct from the full current window."}`
}
