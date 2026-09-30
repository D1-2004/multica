// Package contextcap implements the scene and personal capability layers of
// an agent: the offer catalog, scene/person bindings of offered connectors and
// skills, scene/person connector credentials, configuration grants and
// agent-issued configuration links.
//
// The package is storage and policy only. It never imports the HTTP handler
// layer; callers pass a DBTX (a pgx pool, connection or transaction) and are
// responsible for authorization, feature gating and request validation.
//
// See docs/context-capabilities.md for the product contract.
package contextcap

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Scope types stored in context_capability_binding.scope_type. Credentials,
// grants and links only ever use ScopeScene and ScopePerson.
const (
	ScopeOffer  = "offer"
	ScopeScene  = "scene"
	ScopePerson = "person"
)

// Resource types stored in context_capability_binding.resource_type.
const (
	ResourceConnector = "connector"
	ResourceSkill     = "skill"
)

// Grant sources stored in context_config_grant.source.
const (
	GrantSourceAgentLink = "agent_link"
	GrantSourceJSAPI     = "jsapi"
)

// Link and grant lifetimes. A scene link is posted in a group, so any reader
// within LinkTTLScene receives a scene grant; a person link is single use.
const (
	LinkTTLScene   = 30 * time.Minute
	LinkTTLPerson  = 15 * time.Minute
	GrantTTLScene  = 30 * 24 * time.Hour
	GrantTTLPerson = 365 * 24 * time.Hour
)

// LinkTTL returns the link lifetime for a scene or person scope, or zero for
// any other scope type.
func LinkTTL(scopeType string) time.Duration {
	switch scopeType {
	case ScopeScene:
		return LinkTTLScene
	case ScopePerson:
		return LinkTTLPerson
	default:
		return 0
	}
}

// GrantTTL returns the grant lifetime for a scene or person scope, or zero
// for any other scope type.
func GrantTTL(scopeType string) time.Duration {
	switch scopeType {
	case ScopeScene:
		return GrantTTLScene
	case ScopePerson:
		return GrantTTLPerson
	default:
		return 0
	}
}

// ConversationTypeGroup is the DingTalk dispatch conversation type of a group
// chat. Only group conversations carry a scene layer.
const ConversationTypeGroup = "group"

// IsDirectConversationType reports whether a dispatch conversation type is
// positively a 1:1 chat. It is the same allow-list the dispatcher uses for
// DM routing (single, p2p, private, direct; case-insensitive). Empty and
// unknown types are not 1:1.
func IsDirectConversationType(conversationType string) bool {
	switch strings.ToLower(strings.TrimSpace(conversationType)) {
	case "single", "p2p", "private", "direct":
		return true
	default:
		return false
	}
}

// ReplayedDispatchContextKey marks a task context that was copied from an
// earlier task into a run someone else re-triggered (a manual rerun). The
// copied dispatch sender and conversation no longer describe who triggered
// the run, so ScopeFromTaskContext yields no scene or personal layer for it.
const ReplayedDispatchContextKey = "replayed_dispatch_context"

const maxScopeKeyLength = 256

// Scope is the DingTalk scene and trigger person of one task. It is derived
// only from the server-written dispatch context, never from the prompt.
type Scope struct {
	// OrgID is the agent's DingTalk org (agent_dingtalk_identity.org_id). It is
	// not part of the task context; the caller fills it.
	OrgID string
	// SceneKey is the group's openConversationId; empty outside a valid group.
	SceneKey   string
	SceneTitle string
	// DirectSceneKey is the openConversationId of a positively 1:1
	// conversation (IsDirectConversationType), empty otherwise. A DM is a
	// scene for configuration (a personal link minted there also grants it),
	// but runtime resolution does not apply DM scenes yet: HasScene and
	// SceneKey stay group-only.
	DirectSceneKey string
	// PersonKey is the trigger person's staffId; empty when the run merged
	// messages from several senders or the sender is unknown.
	PersonKey  string
	PersonName string
	// ConversationType is dispatch_event_data.conversation.type as written by
	// the dispatcher ("group", "single", ...), whitespace-trimmed.
	ConversationType string
	// DispatchOrgID is external_identity.dws.orgId: the agent's own DingTalk
	// org recorded when the message was dispatched ("" when absent). The
	// caller compares it with the agent's current org so a task dispatched
	// under an earlier binding is never read under a different org.
	DispatchOrgID string
}

// HasScene reports whether the task carries a scene layer.
func (s Scope) HasScene() bool { return s.SceneKey != "" }

// HasPerson reports whether the task carries a personal layer.
func (s Scope) HasPerson() bool { return s.PersonKey != "" }

// ValidOpenConversationID accepts a DingTalk openConversationId: non-empty,
// starting with "cid", at most 256 bytes, with no whitespace or control
// characters.
func ValidOpenConversationID(id string) bool {
	return strings.HasPrefix(id, "cid") && validScopeKey(id)
}

// ValidStaffID accepts a DingTalk staffId used as a personal scope key.
func ValidStaffID(id string) bool {
	return validScopeKey(id)
}

// ValidScopeKey reports whether key is acceptable for scopeType: a valid
// openConversationId for scenes and a valid staffId for persons.
func ValidScopeKey(scopeType, key string) bool {
	switch scopeType {
	case ScopeScene:
		return ValidOpenConversationID(key)
	case ScopePerson:
		return ValidStaffID(key)
	default:
		return false
	}
}

func validScopeKey(s string) bool {
	if s == "" || len(s) > maxScopeKeyLength || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

type taskContextSender struct {
	StaffID              string `json:"staffId"`
	DisplayName          string `json:"displayName"`
	UID                  string `json:"uid"`
	OpenDingTalkID       string `json:"openDingTalkId"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
}

type taskContextMessage struct {
	SenderStaffID        string `json:"senderStaffId"`
	SenderUID            string `json:"senderUid"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
}

type taskContextEnvelope struct {
	// Replayed is ReplayedDispatchContextKey.
	Replayed bool `json:"replayed_dispatch_context"`
	// FollowUpCommentIDs lists the Coordinator follow-up comments coalesced
	// into one issue task (service/coordinator_follow_up.go); more than one
	// entry means the run may combine several speakers' follow-ups.
	FollowUpCommentIDs []string `json:"coordinator_follow_up_comment_ids"`
	ExternalIdentity   struct {
		DWS struct {
			OrgID string `json:"orgId"`
		} `json:"dws"`
	} `json:"external_identity"`
	EventData *struct {
		Conversation struct {
			OpenConversationID string `json:"openConversationId"`
			Type               string `json:"type"`
			Title              string `json:"title"`
			ConversationTitle  string `json:"conversationTitle"`
			ConversationName   string `json:"conversationName"`
			Name               string `json:"name"`
			SnakeTitle         string `json:"conversation_title"`
		} `json:"conversation"`
		Sender   taskContextSender    `json:"sender"`
		Messages []taskContextMessage `json:"messages"`
	} `json:"dispatch_event_data"`
}

// ScopeFromTaskContext derives the scene and trigger person from a task's
// server-written DingTalk dispatch context (agent_task_queue.context).
//
//   - SceneKey is conversation.openConversationId, only when conversation.type
//     is "group" and the id passes ValidOpenConversationID.
//   - PersonKey is sender.staffId, only when the run positively comes from
//     that one person (see singleTriggerPerson), so one person's credentials
//     never serve a merged run that contains someone else's message.
//
// A context marked with ReplayedDispatchContextKey (a manual rerun) yields the
// zero Scope: the copied sender did not trigger the new run. OrgID is never
// set here; the caller fills it from the agent's DingTalk identity. Malformed
// or empty context yields the zero Scope. A2A-origin tasks must be excluded by
// the caller (service.IsA2ATaskOrigin).
func ScopeFromTaskContext(raw []byte) Scope {
	if len(raw) == 0 {
		return Scope{}
	}
	var envelope taskContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.EventData == nil || envelope.Replayed {
		return Scope{}
	}
	data := envelope.EventData
	conversation := data.Conversation
	scope := Scope{
		ConversationType: strings.TrimSpace(conversation.Type),
		DispatchOrgID:    strings.TrimSpace(envelope.ExternalIdentity.DWS.OrgID),
	}

	cid := strings.TrimSpace(conversation.OpenConversationID)
	if scope.ConversationType == ConversationTypeGroup && ValidOpenConversationID(cid) {
		scope.SceneKey = cid
		scope.SceneTitle = firstNonEmpty(
			conversation.Title,
			conversation.ConversationTitle,
			conversation.ConversationName,
			conversation.Name,
			conversation.SnakeTitle,
		)
	}
	if IsDirectConversationType(scope.ConversationType) && ValidOpenConversationID(cid) {
		scope.DirectSceneKey = cid
	}

	if staffID := strings.TrimSpace(data.Sender.StaffID); ValidStaffID(staffID) &&
		singleTriggerPerson(staffID, data.Sender, data.Messages, len(envelope.FollowUpCommentIDs) > 1) {
		scope.PersonKey = staffID
		scope.PersonName = strings.TrimSpace(data.Sender.DisplayName)
	}
	return scope
}

// singleTriggerPerson reports whether the run positively comes from the
// sender identified by staffID. A message that lacks a senderStaffId is not
// proof: merged windows and coalesced Coordinator follow-ups keep only one
// data-level sender while their messages may come from other speakers.
//
//   - No message (event dispatches): the data-level sender is the actor.
//   - Exactly one message of a run that is not a coalesced follow-up: the
//     message must not name anyone else (staffId, uid or openDingTalkId).
//   - Otherwise every message must carry senderStaffId == staffID and must
//     not name anyone else.
func singleTriggerPerson(staffID string, sender taskContextSender, messages []taskContextMessage, coalesced bool) bool {
	senderUID := strings.TrimSpace(sender.UID)
	senderOpenID := firstNonEmpty(sender.OpenDingTalkID, sender.SenderOpenDingTalkID)
	requireStaffID := coalesced || len(messages) > 1
	if coalesced && len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		messageStaffID := strings.TrimSpace(message.SenderStaffID)
		switch {
		case messageStaffID != "" && messageStaffID != staffID:
			return false
		case requireStaffID && messageStaffID == "":
			return false
		}
		if uid := strings.TrimSpace(message.SenderUID); uid != "" && senderUID != "" && uid != senderUID {
			return false
		}
		if openID := strings.TrimSpace(message.SenderOpenDingTalkID); openID != "" && senderOpenID != "" && openID != senderOpenID {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
