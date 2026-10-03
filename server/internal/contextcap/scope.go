// Package contextcap implements the context layers of an agent: its tenants
// (AgentTenants), the offer catalog, org/scene/person bindings of offered
// connectors and skills, org/scene/person connector credentials, prompt
// components and custom MCP servers, the merge of those layers into one
// effective context (MergeContext), configuration grants and agent-issued
// configuration links.
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

	"github.com/google/uuid"
)

// Scope types stored in context_capability_binding.scope_type. ScopeOrg is
// the enterprise (tenant) level: its scope key is the org id itself. Grants
// and links only ever use ScopeScene and ScopePerson.
const (
	ScopeOffer  = "offer"
	ScopeOrg    = "org"
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

// Scope is the DingTalk tenant org, scene and trigger person of one task. It
// is derived only from the server-written dispatch context, never from the
// prompt.
type Scope struct {
	// Dispatched reports that the task carries a DingTalk dispatch context
	// (dispatch_event_data, not a replay). Only such a task has context
	// layers; whether they apply depends on its org being a tenant.
	Dispatched bool
	// OrgID is the tenant org the task's layers live under: the dispatch org
	// (DispatchOrgID), else the agent's DingTalk identity org. It is not set
	// by ScopeFromTaskContext; the caller fills it only after checking that
	// the org is one of the agent's tenants (AgentTenants), and leaves the
	// whole Scope zero otherwise. It stays "" for an agent with neither a
	// DingTalk identity nor a recorded dispatch org: its scene and person
	// layers live under org "" and it has no org layer.
	OrgID string
	// SceneID is the task's Agent work scene (agent_scene.id, the SceneRef
	// the dispatch resolved; docs/agent-scene.md), a group or a 1:1 chat;
	// empty when the dispatch had none. The scene layer is keyed by it.
	SceneID    string
	SceneTitle string
	// PersonKey is the trigger person's key (TriggerPersonKey): the sender's
	// staffId, else "odt:" + their openDingTalkId; empty when the run merged
	// messages from several senders or the sender is unknown.
	PersonKey  string
	PersonName string
	// ConversationType is dispatch_event_data.conversation.type as written by
	// the dispatcher ("group", "single", ...), whitespace-trimmed.
	ConversationType string
	// DispatchOrgID is external_identity.dws.orgId: the agent's own DingTalk
	// org recorded when the message was dispatched ("" when absent). The
	// caller resolves OrgID from it, so a staffId or openConversationId is
	// only ever read under the org it was dispatched in.
	DispatchOrgID string
}

// HasOrg reports whether the task carries the org (tenant) layer: a
// dispatched task whose OrgID the caller resolved to a tenant.
func (s Scope) HasOrg() bool { return s.Dispatched && s.OrgID != "" }

// HasScene reports whether the task carries a scene layer.
func (s Scope) HasScene() bool { return s.SceneID != "" }

// HasPerson reports whether the task carries a personal layer.
func (s Scope) HasPerson() bool { return s.PersonKey != "" }

// HasLayers reports whether an org, scene or personal layer applies.
func (s Scope) HasLayers() bool { return s.HasOrg() || s.HasScene() || s.HasPerson() }

// Selection returns the layers of the scope for LoadLayers and
// LayerCredentials.
func (s Scope) Selection() LayerSelection {
	return LayerSelection{OrgID: s.OrgID, Org: s.HasOrg(), SceneID: s.SceneID, PersonKey: s.PersonKey}
}

// ValidOpenConversationID accepts a DingTalk openConversationId: non-empty,
// starting with "cid", at most 256 bytes, with no whitespace or control
// characters.
func ValidOpenConversationID(id string) bool {
	return strings.HasPrefix(id, "cid") && validScopeKey(id)
}

// ValidStaffID accepts a personal scope key: a DingTalk staffId or, for a
// sender a dispatch names only by openDingTalkId, "odt:" + that id
// (TriggerPersonKey).
func ValidStaffID(id string) bool {
	return validScopeKey(id)
}

// PersonKeyOpenDingTalkPrefix marks a personal scope key that is an
// openDingTalkId, so it never equals a staffId.
const PersonKeyOpenDingTalkPrefix = "odt:"

// TriggerPersonKey is the personal scope key of a message sender: the
// sender's staffId when the dispatch carries one (Router deliveries), else
// "odt:" + the sender's openDingTalkId. A DWS native subscription event
// names its sender only by openDingTalkId; that id is relative to the
// receiving DingTalk account (another account sees another id for the same
// person) and stays the same in that account's 1:1 chats and groups, so it
// keys the person's capabilities with this agent. A staffId that carries the
// prefix is refused rather than confused with one. "" when neither is a
// valid key.
func TriggerPersonKey(staffID, openDingTalkID string) string {
	if staffID = strings.TrimSpace(staffID); staffID != "" {
		if ValidStaffID(staffID) && !strings.HasPrefix(staffID, PersonKeyOpenDingTalkPrefix) {
			return staffID
		}
		return ""
	}
	openDingTalkID = strings.TrimSpace(openDingTalkID)
	if openDingTalkID == "" || strings.EqualFold(openDingTalkID, "null") {
		return ""
	}
	if key := PersonKeyOpenDingTalkPrefix + openDingTalkID; ValidStaffID(key) {
		return key
	}
	return ""
}

// ValidOrgID accepts a DingTalk org id used as an org scope key or as the org
// of a scene or person scope: non-empty, at most 256 bytes, no whitespace or
// control characters. It is looser than ValidTenantOrgID, which a new tenant
// must pass, because an agent's identity org is a tenant as recorded.
func ValidOrgID(id string) bool {
	return validScopeKey(id)
}

// ValidSceneID accepts a scene scope key: an agent_scene id in canonical
// lowercase UUID form (docs/agent-scene.md).
func ValidSceneID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// ValidScopeKey reports whether key is acceptable for scopeType: a valid org
// id for the org scope, a scene_id for scenes and a valid staffId for
// persons.
func ValidScopeKey(scopeType, key string) bool {
	switch scopeType {
	case ScopeOrg:
		return ValidOrgID(key)
	case ScopeScene:
		return ValidSceneID(key)
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
	// AgentScene is the dispatch's SceneRef (protocol.AgentSceneContextKey).
	AgentScene *struct {
		SceneID string `json:"scene_id"`
	} `json:"agent_scene"`
	// SceneRoutine is the Host-frozen binding of a scene routine run
	// (protocol.SceneRoutineContextKey). Such a run has no dispatch event:
	// it is scoped by AgentScene and the routine's tenant org.
	SceneRoutine *struct {
		TenantOrgID string `json:"tenant_org_id"`
		Kind        string `json:"kind"`
	} `json:"scene_routine"`
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
//   - SceneID is the dispatch's SceneRef (agent_scene.scene_id), a group or a
//     1:1 chat; the caller checks it is the agent's scene in the task's org.
//   - PersonKey is the sender's TriggerPersonKey (sender.staffId, else
//     "odt:" + the sender's openDingTalkId), only when the run positively
//     comes from that one person (singleTriggerPerson, singleTriggerOpenID),
//     so one person's credentials never serve a merged run that contains
//     someone else's message.
//
// Dispatched is set for every context with dispatch_event_data. A context
// marked with ReplayedDispatchContextKey (a manual rerun) yields the zero
// Scope: the copied sender did not trigger the new run. OrgID is never set
// here; the caller resolves the tenant org. Malformed or empty context
// yields the zero Scope. A2A-origin tasks must be excluded by the caller
// (service.IsA2ATaskOrigin).
func ScopeFromTaskContext(raw []byte) Scope {
	if len(raw) == 0 {
		return Scope{}
	}
	var envelope taskContextEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Replayed {
		return Scope{}
	}
	if envelope.EventData == nil {
		return routineScope(envelope)
	}
	data := envelope.EventData
	conversation := data.Conversation
	scope := Scope{
		Dispatched:       true,
		ConversationType: strings.TrimSpace(conversation.Type),
		DispatchOrgID:    strings.TrimSpace(envelope.ExternalIdentity.DWS.OrgID),
	}

	if envelope.AgentScene != nil && ValidSceneID(strings.TrimSpace(envelope.AgentScene.SceneID)) {
		scope.SceneID = strings.TrimSpace(envelope.AgentScene.SceneID)
		scope.SceneTitle = firstNonEmpty(
			conversation.Title,
			conversation.ConversationTitle,
			conversation.ConversationName,
			conversation.Name,
			conversation.SnakeTitle,
		)
	}

	coalesced := len(envelope.FollowUpCommentIDs) > 1
	if staffID := strings.TrimSpace(data.Sender.StaffID); staffID != "" {
		if key := TriggerPersonKey(staffID, ""); key != "" && singleTriggerPerson(staffID, data.Sender, data.Messages, coalesced) {
			scope.PersonKey = key
			scope.PersonName = strings.TrimSpace(data.Sender.DisplayName)
		}
	} else if openID := senderOpenDingTalkID(data.Sender); openID != "" {
		if key := TriggerPersonKey("", openID); key != "" && singleTriggerOpenID(openID, data.Sender, data.Messages, coalesced) {
			scope.PersonKey = key
			scope.PersonName = strings.TrimSpace(data.Sender.DisplayName)
		}
	}
	return scope
}

// senderOpenDingTalkID is the sender's openDingTalkId, "" when the two
// fields that carry it disagree.
func senderOpenDingTalkID(sender taskContextSender) string {
	openID := strings.TrimSpace(sender.OpenDingTalkID)
	other := strings.TrimSpace(sender.SenderOpenDingTalkID)
	switch {
	case openID == "":
		return other
	case other != "" && other != openID:
		return ""
	}
	return openID
}

// routineScope is the scope of a scene routine run: the routine's scene and
// its tenant org as the dispatch org. A routine never carries a person, in a
// group or a 1:1 chat, whoever created it: it runs with the scene's
// capabilities only (a person_staff_id left in an older run context is
// ignored). Anything incomplete yields the zero Scope (no layers), never a
// guessed scene.
func routineScope(envelope taskContextEnvelope) Scope {
	routine := envelope.SceneRoutine
	if routine == nil || envelope.AgentScene == nil {
		return Scope{}
	}
	sceneID := strings.TrimSpace(envelope.AgentScene.SceneID)
	orgID := strings.TrimSpace(routine.TenantOrgID)
	if !ValidSceneID(sceneID) || orgID == "" {
		return Scope{}
	}
	scope := Scope{Dispatched: true, SceneID: sceneID, DispatchOrgID: orgID}
	switch strings.TrimSpace(routine.Kind) {
	case SceneKindGroup:
		scope.ConversationType = ConversationTypeGroup
	case SceneKindDM:
		scope.ConversationType = "single"
	default:
		return Scope{}
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
//   - A sender with an openDingTalkId and no uid (DWS native) additionally
//     needs every message stamped with that openDingTalkId.
func singleTriggerPerson(staffID string, sender taskContextSender, messages []taskContextMessage, coalesced bool) bool {
	senderUID := strings.TrimSpace(sender.UID)
	senderOpenID := firstNonEmpty(sender.OpenDingTalkID, sender.SenderOpenDingTalkID)
	requireStaffID := coalesced || len(messages) > 1
	// A sender named by openDingTalkId without a uid (a DWS native
	// subscription event, whose staffId the server looked up) is proved only
	// by messages stamped with that id: a work item cut from a merged window
	// keeps the window's sender, and an anonymous line must not inherit it.
	requireOpenID := senderUID == "" && senderOpenID != ""
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
		openID := strings.TrimSpace(message.SenderOpenDingTalkID)
		if openID != "" && senderOpenID != "" && openID != senderOpenID {
			return false
		}
		if requireOpenID && openID == "" {
			return false
		}
	}
	return true
}

// singleTriggerOpenID is singleTriggerPerson for a sender named only by
// openDingTalkID (no staffId anywhere). It is stricter: every message must
// carry senderOpenDingTalkId == openDingTalkID, one alone included, and none
// may carry a staffId or another uid. A work item cut from a merged window
// keeps the window's data-level sender, so a message whose own sender is
// unknown (a group event without an openDingTalkId) proves nothing.
func singleTriggerOpenID(openDingTalkID string, sender taskContextSender, messages []taskContextMessage, coalesced bool) bool {
	senderUID := strings.TrimSpace(sender.UID)
	if coalesced && len(messages) == 0 {
		return false
	}
	for _, message := range messages {
		if strings.TrimSpace(message.SenderStaffID) != "" {
			return false
		}
		if strings.TrimSpace(message.SenderOpenDingTalkID) != openDingTalkID {
			return false
		}
		if uid := strings.TrimSpace(message.SenderUID); uid != "" && senderUID != "" && uid != senderUID {
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
