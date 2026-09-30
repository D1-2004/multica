package clicompat

import (
	"bytes"
	"encoding/json"
	"strings"
)

// dws projects messages in a subprocess its host can kill at a deadline. In
// a host process the same projection runs uninterruptibly, and its cost
// follows message content any group member controls: chatmsg decodes JSON
// found in message text, collects resource ids with a pass per id, and
// re-projects every forwarded level (quadratic time in resource ids,
// quadratic memory in forward depth). A message over these limits is
// therefore not projected by chatmsg; boundedMessageItem keeps what a
// reader needs instead. Real messages stay far below them.
const (
	maxProjectedNodes     = 4000      // JSON values, text-embedded JSON included
	maxProjectedDepth     = 24        // nesting, text-embedded JSON included
	maxProjectedResources = 64        // mediaId/fileId mentions
	maxEmbeddedJSONBytes  = 256 << 10 // text longer than this is not decoded
	maxBoundedStringRunes = 64 << 10  // strings kept by boundedMessageItem
)

type projectionBudget struct {
	nodes, resources int
}

// withinProjectionBudget reports whether chatmsg may project item.
func withinProjectionBudget(item map[string]any) bool {
	b := &projectionBudget{}
	return b.walk(item, 0)
}

func (b *projectionBudget) walk(v any, depth int) bool {
	b.nodes++
	if b.nodes > maxProjectedNodes || depth > maxProjectedDepth {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			if resourceMention(k) > 0 {
				b.resources++
			}
			if !b.walk(value, depth+1) {
				return false
			}
		}
	case []any:
		for _, value := range x {
			if !b.walk(value, depth+1) {
				return false
			}
		}
	case string:
		b.resources += resourceMention(x)
		t := strings.TrimSpace(x)
		if len(t) >= 2 && len(t) <= maxEmbeddedJSONBytes && (t[0] == '{' || t[0] == '[') {
			dec := json.NewDecoder(bytes.NewReader([]byte(t)))
			dec.UseNumber()
			var inner any
			if dec.Decode(&inner) == nil && !b.walk(inner, depth+1) {
				return false
			}
		}
	}
	return b.resources <= maxProjectedResources
}

// resourceMention counts mediaId/fileId spellings (any case, _ or -).
func resourceMention(s string) int {
	if len(s) < 6 {
		return 0
	}
	n := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(s))
	return strings.Count(n, "mediaid") + strings.Count(n, "fileid")
}

// boundedMessageItem is a message projected without chatmsg: its scalar
// fields as they came, with the identity and text fields a list reader
// uses, and projectionSkipped naming why the full projection was skipped.
func boundedMessageItem(item map[string]any, context map[string]any) map[string]any {
	out := make(map[string]any, len(item)+8)
	for _, source := range []map[string]any{context, item} {
		for k, v := range source {
			switch x := v.(type) {
			case string:
				if r := []rune(x); len(r) > maxBoundedStringRunes {
					x = string(r[:maxBoundedStringRunes])
				}
				out[k] = x
			case json.Number, bool, nil, float64:
				out[k] = x
			}
		}
	}
	first := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := out[k]; ok && v != nil && v != "" {
				return v
			}
		}
		return nil
	}
	if id := first("openMessageId", "openMsgId", "messageId", "msgId"); id != nil {
		out["messageId"] = id
		if _, ok := out["openMessageId"]; !ok {
			out["openMessageId"] = id
		}
	}
	if _, ok := out["conversationId"]; !ok {
		if cid := first("openConversationId", "openconversationId", "openCid"); cid != nil {
			out["conversationId"] = cid
		}
	}
	if _, ok := out["senderId"]; !ok {
		if sid := first("senderOpenDingTalkId", "senderOpenDingtalkId", "senderUserId", "senderStaffId"); sid != nil {
			out["senderId"] = sid
		}
	}
	if _, ok := out["sender"]; !ok {
		out["sender"] = first("senderOpenDingTalkId", "senderOpenDingtalkId")
	}
	if _, ok := out["createTime"]; !ok {
		out["createTime"] = nil
	}
	content, _ := out["content"].(string)
	if _, ok := out["content"]; !ok {
		out["content"] = nil
	}
	out["text"] = content
	out["projectionSkipped"] = "complexity_limit"
	return out
}
