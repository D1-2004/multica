package assoc

import "strings"

const (
	IntentAsk     = "ask"
	IntentConfirm = "confirm"
	IntentNotify  = "notify"
	IntentLookup  = "lookup"
	IntentWait    = "wait"
	IntentOther   = "other"
)

// NormalizeIntent accepts the closed intent set the coordinator bind tool writes.
// Empty stays empty so recall can still show cards that were never classified.
func NormalizeIntent(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "":
		return "", true
	case IntentAsk, IntentConfirm, IntentNotify, IntentLookup, IntentWait, IntentOther:
		return strings.ToLower(trimmed), true
	default:
		// Legacy values such as calendar.book remain stored as written.
		return trimmed, true
	}
}

func CoordinatorIntent(raw string) (string, bool) {
	normalized, ok := NormalizeIntent(raw)
	if !ok || normalized == "" {
		return "", false
	}
	switch normalized {
	case IntentAsk, IntentConfirm, IntentNotify, IntentLookup, IntentWait, IntentOther:
		return normalized, true
	default:
		return "", false
	}
}

func IntentLabel(intent string) string {
	switch intent {
	case IntentAsk:
		return "向某人询问一件事"
	case IntentConfirm:
		return "确认时间或选择"
	case IntentNotify:
		return "把结果通知原发起人"
	case IntentLookup:
		return "查找人或记录"
	case IntentWait:
		return "等待对方回复"
	case IntentOther:
		return "其他事项"
	default:
		return ""
	}
}
