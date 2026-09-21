package userdecision

import (
	"github.com/multica-ai/multica/server/pkg/redact"
	"strings"
)

// ExportValue removes transport credentials and unrelated contact/device data;
// the authoritative snapshot itself remains unchanged for permissioned replay.
func ExportValue(v any) any {
	return exportValue(v, 0)
}
func exportValue(v any, depth int) any {
	if depth > 32 {
		return "[REDACTED DEPTH LIMIT]"
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, value := range x {
			lower := strings.ToLower(strings.ReplaceAll(k, "_", ""))
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "authcode") || lower == "authorization" || lower == "email" || lower == "mobile" || lower == "phone" || lower == "operatoruseragent" || lower == "avatarurl" {
				continue
			}
			out[k] = exportValue(value, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = exportValue(item, depth+1)
		}
		return out
	case string:
		return redact.Text(x)
	default:
		return v
	}
}
