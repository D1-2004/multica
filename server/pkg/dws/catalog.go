package dws

import "strings"

// EventDefinition describes one personal event key as DWS publishes it.
// The list is generated from dingtalk-workspace-cli's registry
// (tools/eventcatalog), so a key DWS adds arrives by regenerating.
type EventDefinition struct {
	Key         string `json:"eventKey"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// Category is im, oa, voip, todo or card.
	Category string `json:"category"`
	// RuleType is the subscription rule: at, all, singleChat, sender or group.
	RuleType string `json:"ruleType"`
	// Status is enabled or pending; a pending key cannot be subscribed yet.
	Status string `json:"status"`
	Public bool   `json:"public"`
}

// Event statuses.
const (
	EventStatusEnabled = "enabled"
	EventStatusPending = "pending"
)

// Scopes a subscription names, derived from the rule type and category.
const (
	// ScopeNone: the rule takes no parameters (at, all).
	ScopeNone = "none"
	// ScopeGroup: one conversation (ConversationID).
	ScopeGroup = "group"
	// ScopeTarget: one person (TargetOpenDingTalkID or TargetUserID).
	ScopeTarget = "target"
	// ScopeRoles: todo role types (RoleTypes; default all three).
	ScopeRoles = "roles"
	// ScopeUnknown: a rule type this SDK does not know yet. Such a key is
	// refused, like dws's "schema pending", until the SDK learns the rule.
	ScopeUnknown = "unknown"
)

// Scope is what a subscription to this key must name.
func (d EventDefinition) Scope() string {
	switch {
	case d.Category == "todo":
		return ScopeRoles
	case d.RuleType == "group":
		return ScopeGroup
	case d.RuleType == "singleChat" || d.RuleType == "sender":
		return ScopeTarget
	case d.RuleType == "at" || d.RuleType == "all":
		return ScopeNone
	default:
		return ScopeUnknown
	}
}

// SupportsFilter reports whether the key carries a message body that
// Filter and Keywords can match: the message receive events.
func (d EventDefinition) SupportsFilter() bool {
	return strings.HasPrefix(d.Key, "user_im_message_receive_")
}

// Available reports whether the key can be subscribed now: published,
// enabled, and with a rule this SDK knows.
func (d EventDefinition) Available() bool {
	return d.Public && d.Status == EventStatusEnabled && d.Scope() != ScopeUnknown
}

// EventCatalog lists every personal event key DWS publishes, in DWS's order.
func EventCatalog() []EventDefinition {
	return append([]EventDefinition(nil), eventCatalog...)
}

// LookupEvent returns the definition of key.
func LookupEvent(key string) (EventDefinition, bool) {
	for _, d := range eventCatalog {
		if d.Key == key {
			return d, true
		}
	}
	return EventDefinition{}, false
}

// EventKeys lists every key that can be subscribed now, in DWS's order.
func EventKeys() []string {
	keys := make([]string, 0, len(eventCatalog))
	for _, d := range eventCatalog {
		if d.Available() {
			keys = append(keys, d.Key)
		}
	}
	return keys
}
