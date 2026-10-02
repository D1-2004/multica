// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import openai "github.com/openai/openai-go/v3"

// SessionStore holds one wake's history. The Host persists durable conversation
// and task state; the kernel neither opens JSONL files nor infers a scene key.
type SessionStore struct{ entries []SessionEntry }

// Append records an entry in the session, preserving native call correlation.
func (s *SessionStore) Append(entry SessionEntry) { s.entries = append(s.entries, entry) }

// GetHistory returns a snapshot in insertion order.
func (s *SessionStore) GetHistory() []SessionEntry { return append([]SessionEntry(nil), s.entries...) }

// entriesToMessages converts session entries into native LLM messages. The role
// switch is ported from BotLoop; tool results now use real tool_call_id values.
func entriesToMessages(entries []SessionEntry) []openai.ChatCompletionMessageParamUnion {
	var msgs []openai.ChatCompletionMessageParamUnion
	for _, e := range entries {
		switch e.Type {
		case "user":
			msgs = append(msgs, openai.UserMessage(e.Content))
		case "assistant":
			if e.Message != nil {
				msgs = append(msgs, e.Message.ToParam())
			} else {
				msgs = append(msgs, openai.AssistantMessage(e.Content))
			}
		case "system":
			msgs = append(msgs, openai.SystemMessage(e.Content))
		case "tool_result":
			msgs = append(msgs, openai.ToolMessage(e.Content, e.NativeToolCallID))
		}
	}
	return msgs
}
