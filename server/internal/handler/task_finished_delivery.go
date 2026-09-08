package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) taskFinishedDeliveryContext(ctx context.Context, task *db.AgentTaskQueue) inboundcoord.TaskDeliveryContext {
	out := newTaskDeliveryContext(task)
	if h == nil || h.Queries == nil || task == nil || !task.ID.Valid {
		return out
	}
	messages, err := h.Queries.ListTaskMessages(ctx, task.ID)
	if err != nil {
		slog.Warn("task finished loop: delivery evidence unavailable", "task_id", uuidToString(task.ID), "error", err)
		return out
	}
	return taskDeliveryContextFromMessages(*task, messages)
}

func newTaskDeliveryContext(task *db.AgentTaskQueue) inboundcoord.TaskDeliveryContext {
	out := inboundcoord.TaskDeliveryContext{
		Status: "unavailable", Scope: "current_task", Complete: false,
		Coverage:   "recognized_dws_send_receipts_only",
		Deliveries: []inboundcoord.TaskDeliveryEvidence{},
	}
	if task != nil {
		out.TaskID = uuidToString(task.ID)
	}
	return out
}

type wrapupDWSCall struct {
	action   string
	text     string
	openTask string
	seq      int32
	tool     string
}

type wrapupDWSReceipt struct {
	conversation string
	message      string
	openTask     string
}

// Only this run's persisted tool rows can provide delivery evidence. Assoc
// cards span an Issue's runs and contain purpose text rather than sent text.
func taskDeliveryContextFromMessages(task db.AgentTaskQueue, messages []db.TaskMessage) inboundcoord.TaskDeliveryContext {
	out := newTaskDeliveryContext(&task)
	out.Status = "loaded"
	pending := map[string]wrapupDWSCall{}
	var adjacent *wrapupDWSCall
	openCalls := 0
	seen := map[string]bool{}
	verified := map[int32]bool{}
	accept := func(call wrapupDWSCall, output string) {
		receipt, ok := parseWrapupDWSReceipt(output)
		if !ok {
			return
		}
		if call.action == "status" {
			original, found := pending[call.openTask]
			if !found || call.openTask == "" || (receipt.openTask != "" && receipt.openTask != call.openTask) {
				return
			}
			call = original
		}
		if receipt.conversation == "" || receipt.message == "" {
			if call.action != "status" && receipt.openTask != "" {
				pending[receipt.openTask] = call
			}
			return
		}
		key := receipt.conversation + "\x00" + receipt.message
		if seen[key] {
			return
		}
		seen[key] = true
		out.Deliveries = append(out.Deliveries, inboundcoord.TaskDeliveryEvidence{
			ConversationID: receipt.conversation, MessageID: receipt.message,
			SourceSeq: call.seq, SentText: clipRunes(call.text, 600), TextComplete: call.text != "" && utf8.RuneCountInString(call.text) <= 600,
		})
		if len(out.Deliveries) > 12 {
			out.Deliveries = out.Deliveries[len(out.Deliveries)-12:]
			out.Truncated = true
		}
		if !verified[call.seq] && out.UnverifiedSends > 0 {
			out.UnverifiedSends--
		}
		verified[call.seq] = true
	}
	for _, msg := range messages {
		if msg.TaskID != task.ID {
			adjacent = nil
			continue
		}
		kind := strings.TrimSpace(msg.Type)
		if kind != "tool" && kind != "tool_use" && kind != "tool-use" && kind != "tool_result" && kind != "tool-result" {
			adjacent = nil
			continue
		}
		if kind == "tool_result" || kind == "tool-result" {
			// Call IDs are not persisted. More than one outstanding call makes
			// even an adjacent same-tool result ambiguous, so do not pair it.
			if openCalls == 1 && adjacent != nil && msg.Tool.String == adjacent.tool {
				accept(*adjacent, strings.TrimSpace(msg.Output.String+"\n"+msg.Content.String))
			}
			if openCalls > 0 {
				openCalls--
			}
			adjacent = nil
			continue
		}
		adjacent = nil
		if (kind == "tool_use" || kind == "tool-use") && strings.TrimSpace(msg.Output.String) == "" {
			openCalls++
		}
		call, ok := parseWrapupDWSCall(msg)
		if !ok {
			continue
		}
		if call.action != "status" {
			out.UnverifiedSends++
		}
		if strings.TrimSpace(msg.Output.String) != "" {
			accept(call, msg.Output.String)
		} else if openCalls == 1 && (kind == "tool_use" || kind == "tool-use") {
			adjacent = &call
		}
	}
	return out
}

// Unknown wrappers, compound shell commands and expansions remain unverified.
// In particular, text that merely quotes a DWS command is never a send.
func parseWrapupDWSCall(msg db.TaskMessage) (wrapupDWSCall, bool) {
	var call wrapupDWSCall
	switch strings.ToLower(strings.TrimSpace(msg.Tool.String)) {
	case "bash", "terminal", "exec_command", "functions.exec_command", "shell", "shell_command":
	default:
		return call, false
	}
	var input map[string]any
	_ = json.Unmarshal(msg.Input, &input)
	command := ""
	for _, key := range []string{"command", "cmd", "text"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			command = value
			break
		}
	}
	if command == "" {
		command = msg.Content.String
	}
	args, ok := wrapupCommandArgs(strings.TrimPrefix(strings.TrimSpace(command), "$ "))
	if !ok || len(args) < 4 || filepath.Base(args[0]) != "dws" || args[1] != "chat" || args[2] != "message" {
		return call, false
	}
	switch args[3] {
	case "send", "reply":
		call.action = args[3]
	case "query-send-status":
		call.action = "status"
	default:
		return call, false
	}
	for i := 4; i < len(args); i++ {
		key, value, inline := strings.Cut(args[i], "=")
		if !inline && i+1 < len(args) {
			value = args[i+1]
		}
		switch key {
		case "--content", "--text":
			call.text = value
		case "--open-task-id":
			call.openTask = value
		}
	}
	call.seq, call.tool = msg.Seq, msg.Tool.String
	return call, true
}

func wrapupCommandArgs(command string) ([]string, bool) {
	var args []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range command {
		if escaped {
			if r == '\n' || r == '\r' {
				return nil, false
			}
			if quote == '"' && !strings.ContainsRune("\\$`\"", r) {
				word.WriteRune('\\')
			}
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != '\'' && (r == '$' || r == '`') {
			return nil, false
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, started = r, true
		case ' ', '\t':
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		case ';', '|', '&', '<', '>', '\n', '\r', '#', '~', '*', '?', '[':
			return nil, false
		default:
			word.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if started {
		args = append(args, word.String())
	}
	return args, true
}

func parseWrapupDWSReceipt(output string) (wrapupDWSReceipt, bool) {
	var receipt wrapupDWSReceipt
	raw := strings.TrimSpace(output)
	if strings.HasPrefix(raw, "terminal result\n- **output:** ") {
		raw = strings.TrimPrefix(raw, "terminal result\n- **output:** ")
		var rest string
		raw, rest, _ = strings.Cut(raw, "\n- **exit_code:** ")
		if strings.TrimSpace(rest) != "0" {
			return receipt, false
		}
	}
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	if decoder.Decode(&body) != nil {
		return receipt, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || body["success"] != true || body["isError"] == true {
		return receipt, false
	}
	for depth := 0; depth < 4; depth++ {
		if success, exists := body["success"]; exists && success != true {
			return wrapupDWSReceipt{}, false
		}
		if rawStatus, exists := body["sendStatus"]; exists {
			status, ok := rawStatus.(string)
			if !ok || !strings.EqualFold(status, "success") {
				return wrapupDWSReceipt{}, false
			}
		}
		for _, key := range []string{"openConversationId", "conversationId", "conversation_id"} {
			if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
				receipt.conversation = strings.TrimSpace(value)
			}
		}
		for _, key := range []string{"openMessageId", "openMsgId", "open_msg_id"} {
			if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
				receipt.message = strings.TrimSpace(value)
			}
		}
		if value, ok := body["openTaskId"].(string); ok {
			receipt.openTask = strings.TrimSpace(value)
		}
		child, ok := body["result"].(map[string]any)
		if !ok {
			break
		}
		body = child
	}
	if strings.ContainsAny(receipt.conversation+receipt.message, " \t\r\n") || strings.HasPrefix(receipt.message, "outbound:") {
		return wrapupDWSReceipt{}, false
	}
	return receipt, true
}
