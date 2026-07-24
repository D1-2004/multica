package daemon

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/mattn/go-shellwords"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type dwsReplyTracker struct {
	mu            sync.Mutex
	pending       map[string]string
	resultMessage string
}

func newDWSReplyTracker() *dwsReplyTracker {
	return &dwsReplyTracker{pending: make(map[string]string)}
}

func (t *dwsReplyTracker) Observe(message agent.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch message.Type {
	case agent.MessageToolUse:
		if message.CallID == "" {
			return
		}
		delete(t.pending, message.CallID)
		if text, ok := extractDWSReplyText(message); ok {
			t.pending[message.CallID] = text
		}
	case agent.MessageToolResult:
		if message.CallID == "" {
			return
		}
		text, ok := t.pending[message.CallID]
		if !ok {
			return
		}
		delete(t.pending, message.CallID)

		if !dwsToolResultSucceeded(message.Output) {
			return
		}
		t.resultMessage = text
	}
}

func (t *dwsReplyTracker) ResultMessage() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resultMessage
}

func dwsToolResultSucceeded(output string) bool {
	raw := json.RawMessage(strings.TrimSpace(output))
	if len(raw) == 0 {
		return false
	}

	switch raw[0] {
	case '{':
		if success, found := dwsSuccessObject(raw); found {
			return success
		}
		return dwsSuccessTextWrapper(raw)
	case '[':
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return false
		}
		for _, block := range blocks {
			if dwsSuccessTextWrapper(block) {
				return true
			}
		}
		return false
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return false
		}
		success, found := dwsSuccessObject(json.RawMessage(strings.TrimSpace(text)))
		return found && success
	default:
		return false
	}
}

func dwsSuccessObject(raw json.RawMessage) (bool, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return false, false
	}
	successRaw, ok := object["success"]
	if !ok {
		return false, false
	}
	var success bool
	if err := json.Unmarshal(successRaw, &success); err != nil {
		return false, true
	}
	return success, true
}

func dwsSuccessTextWrapper(raw json.RawMessage) bool {
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil {
		return false
	}
	if typeRaw, ok := block["type"]; ok {
		var blockType string
		if err := json.Unmarshal(typeRaw, &blockType); err != nil || blockType != "text" {
			return false
		}
	}
	textRaw, ok := block["text"]
	if !ok {
		return false
	}
	var text string
	if err := json.Unmarshal(textRaw, &text); err != nil {
		return false
	}
	success, found := dwsSuccessObject(json.RawMessage(strings.TrimSpace(text)))
	return found && success
}

func extractDWSReplyText(message agent.Message) (string, bool) {
	if message.Type != agent.MessageToolUse || message.CallID == "" || !isShellTool(message.Tool) {
		return "", false
	}

	command := ""
	for _, field := range []string{"command", "cmd"} {
		value, ok := message.Input[field]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", false
		}
		command = text
		break
	}
	if command == "" || hasUnsafeShellExpansion(command) {
		return "", false
	}

	parser := shellwords.NewParser()
	parser.ParseEnv = false
	parser.ParseBacktick = false
	words, err := parser.Parse(command)
	if err != nil || parser.Position >= 0 || len(words) < 4 {
		return "", false
	}
	if words[0] != "dws" || words[1] != "chat" || words[2] != "message" || words[3] != "reply" {
		return "", false
	}

	text := ""
	format := ""
	textSeen := false
	formatSeen := false
	for i := 4; i < len(words); i++ {
		switch {
		case words[i] == "--text":
			if textSeen || i+1 >= len(words) {
				return "", false
			}
			textSeen = true
			i++
			text = words[i]
		case strings.HasPrefix(words[i], "--text="):
			if textSeen {
				return "", false
			}
			textSeen = true
			text = strings.TrimPrefix(words[i], "--text=")
		case words[i] == "--format":
			if formatSeen || i+1 >= len(words) {
				return "", false
			}
			formatSeen = true
			i++
			format = words[i]
		case strings.HasPrefix(words[i], "--format="):
			if formatSeen {
				return "", false
			}
			formatSeen = true
			format = strings.TrimPrefix(words[i], "--format=")
		}
	}
	if !textSeen || !formatSeen || strings.TrimSpace(text) == "" || format != "json" {
		return "", false
	}
	return text, true
}

func isShellTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "bash", "shell", "exec_command":
		return true
	default:
		return false
	}
}

func hasUnsafeShellExpansion(command string) bool {
	var singleQuoted bool
	var doubleQuoted bool
	var escaped bool

	for _, r := range command {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && !singleQuoted {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !doubleQuoted {
				singleQuoted = !singleQuoted
			}
		case '"':
			if !singleQuoted {
				doubleQuoted = !doubleQuoted
			}
		case '$', '`':
			if !singleQuoted {
				return true
			}
		case '\n', '\r':
			if !singleQuoted && !doubleQuoted {
				return true
			}
		}
	}
	return false
}
