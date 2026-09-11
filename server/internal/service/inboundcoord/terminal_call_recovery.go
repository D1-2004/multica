package inboundcoord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/langfuse"
)

// Only existing action names can be recovered as a finish proposal. These are
// not new tools or permissions; the normal finish validator still owns scope,
// current-stage restrictions, source coverage, review and durable admission.
func isRecoverableTerminalAction(name string) bool {
	return oneOf(name, "start_work", "continue_work", "clarify", "report_status", "acknowledge", "describe_capabilities", "report_memory", "decline", "ignore", "report_result")
}

func recoverTerminalActionCall(original functionCall) (functionCall, bool, error) {
	if !isRecoverableTerminalAction(original.Name) {
		return original, false, nil
	}
	fields, err := decodeTerminalActionObject(original.Arguments)
	if err != nil {
		return original, true, hintWrap("invalid coordination action arguments", "Use one finish({actions:[...]}) call with a JSON action object; preserve the supplied request and do not guess missing fields.", err)
	}
	if raw, exists := fields["kind"]; exists {
		var kind string
		if json.Unmarshal(raw, &kind) != nil || kind != original.Name {
			return original, true, hintErr("coordination action kind conflicts with its call name", "Choose the intended action explicitly inside finish.actions; Host cannot choose between conflicting kinds.")
		}
	} else {
		fields["kind"], _ = json.Marshal(original.Name)
	}
	if raw, exists := fields["source_refs"]; exists && strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) {
		var encoded string
		var refs []any
		if json.Unmarshal(raw, &encoded) != nil || !strings.HasPrefix(strings.TrimSpace(encoded), "[") || json.Unmarshal([]byte(encoded), &refs) != nil {
			return original, true, hintErr("source_refs is not an encoded JSON array", "Use source_refs as a JSON array of exact current-window references, for example [\"u1\"]. Do not use CSV or double-encoded strings.")
		}
		for _, ref := range refs {
			if _, ok := ref.(string); !ok {
				return original, true, hintErr("source_refs contains a non-string value", "Keep the exact current-window reference strings; Host does not infer or repair IDs.")
			}
		}
		fields["source_refs"] = json.RawMessage(encoded)
	}
	encoded, err := json.Marshal(map[string]any{"actions": []map[string]json.RawMessage{fields}})
	if err != nil {
		return original, true, err
	}
	return functionCall{ID: original.ID, Name: toolFinish, Arguments: string(encoded)}, true, nil
}

// Reject duplicate fields rather than silently picking a kind/source value.
// RawMessage preserves unknown fields for the existing strict finish validator.
func decodeTerminalActionObject(raw string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("action arguments must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("action field name must be a string")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate action field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("action arguments must contain one JSON object")
	}
	return fields, nil
}

func recordTerminalCallRecovery(lt *langfuse.Trace, turn Turn, round int, original, normalized functionCall, err error) {
	input := map[string]any{"tool": original.Name, "arguments": original.Arguments}
	output := map[string]any{"normalized": err == nil, "candidate_only": true}
	if err == nil {
		output["tool"], output["arguments"] = normalized.Name, normalized.Arguments
	}
	if lt != nil {
		lt.Event(langfuse.ObservationOptions{Type: langfuse.TypeTool, Name: "coordinator.terminal_call_recovery", Input: input,
			Metadata: map[string]any{"origin": "host_protocol_recovery", "routing_round": round + 1, "tool_call_id": original.ID}},
			langfuse.EndOptions{Output: output, Err: err})
	}
	slog.Info("inbound coordinator terminal call recovery", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_terminal_call_recovery", "round", round+1, "origin", "host_protocol_recovery",
		"original_tool", original.Name, "original_arguments", clipRunes(original.Arguments, llmLogToolBudget),
		"normalized_tool", normalized.Name, "normalized_arguments", clipRunes(normalized.Arguments, llmLogToolBudget),
		"normalized", err == nil, "candidate_only", true)...)
}

// A typed Host prerequisite remains identifiable when the diagnostic text adds
// an action index or changes wording. Review-generated text cannot forge it.
type historyPrerequisiteHintError struct{ *toolHintError }

func historyPrerequisiteHint(message, hint string) error {
	return &historyPrerequisiteHintError{toolHintError: &toolHintError{msg: message, hint: hint}}
}

func isHistoryPrerequisiteError(err error) bool {
	var prerequisite *historyPrerequisiteHintError
	return errors.As(err, &prerequisite)
}

func historyPrerequisiteResolvedFeedback(turn Turn, unresolvedReview string) string {
	if unresolvedReview != "" {
		return unresolvedReview
	}
	encoded, _ := json.Marshal(map[string]any{
		"tool": toolContextRead, "history_status": turn.HistoryStatus, "read_prerequisite_satisfied": true,
		"instruction": "The scoped history read has completed; do not repeat the read to satisfy the previous prerequisite. Re-evaluate the current utterance against the returned status and evidence. Empty or unavailable history does not prove absence of dialogue. Loaded history does not prove a matching original question, consent, or authorization to continue work; all finish checks still apply.",
	})
	return string(encoded)
}
