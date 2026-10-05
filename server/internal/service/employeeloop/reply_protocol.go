package employeeloop

import (
	"errors"
	"regexp"
	"strings"
)

var replyXMLTag = regexp.MustCompile(`<\s*/?\s*([a-zA-Z_][a-zA-Z0-9_.:-]*)(?:\s|/?>|$)`)
var replySourceLocator = regexp.MustCompile(`(?i)\bsource_ref\s*["']?\s*[=:]\s*["']?\s*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/`)

// DingTalk's captured openMsgIds encode 16 bytes as 22 base64 characters plus
// padding. Match a complete locator, not a business UUID/msgdocs/path prefix.
var replyMessageLocator = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/msg[a-z0-9+/]{22}==(?:$|[^a-z0-9+/=])`)

// ValidateReplyProtocol separates public text from the configured native tool
// protocol. It never parses text into executable calls or rewrites a reply.
// Business HTML/JSON and comparisons remain text; registered tool tags and
// internal receipt/message locators are reserved even inside Markdown examples.
func ValidateReplyProtocol(text string, tools []Tool) error {
	if replySourceLocator.MatchString(text) || replyMessageLocator.MatchString(text) {
		return errors.New("public reply contains an internal source locator; use native tool_calls")
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[strings.ToLower(tool.Name)] = true
	}
	for _, tag := range replyXMLTag.FindAllStringSubmatch(text, -1) {
		if names[strings.ToLower(tag[1])] {
			return errors.New("public reply contains registered tool XML; use native tool_calls")
		}
	}
	return nil
}

func (l *Loop) validatePublicToolText(calls []ToolCall) error {
	for _, call := range calls {
		// Execution prompts, quotes and source_ref arguments remain data. Only
		// the public reply and first-feedback fields are checked here.
		if reply, ok := call.Arguments["reply"].(string); ok {
			if err := ValidateReplyProtocol(reply, l.config.Tools); err != nil {
				return err
			}
		}
		if call.Name == FirstFeedbackToolName {
			if text, ok := call.Arguments["text"].(string); ok {
				if err := ValidateReplyProtocol(text, l.config.Tools); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
