// Typed event forms, ported from dingtalk-workspace-cli
// internal/event/personal/output.go (Copyright 2026 Alibaba Group, Apache
// License 2.0, http://www.apache.org/licenses/LICENSE-2.0). The structs,
// their JSON fields, the wire field each is read from, the required fields,
// the strict operOpenDingtalkId spelling and the conservative
// group-lifecycle and card-action payloads follow its ProjectOutput.
// Changed in this port:
//   - the input is an Event (Key, ID, SubscriptionID, OccurredAt and the
//     decoded frame Data) instead of dws's transport.Event; the frame's
//     eventBornTime is not kept, so timestamp falls back to OccurredAt;
//   - the payload and its body may be JSON-encoded strings, as decodeEvent
//     accepts them;
//   - a payload that does not fit its key is an error and no value, where
//     dws returns the raw envelope or a stub beside the error;
//   - shared fields are embedded structs (EventHeader, Approval, Todo),
//     which marshal to the same flat JSON;
//   - schema generation and ProjectTransportOutput are not ported.

package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws"
)

var (
	// ErrNoTypedForm: the event is malformed or its key has no typed form;
	// Body and Data are all there is.
	ErrNoTypedForm = errors.New("event has no typed form")
	// ErrMalformedPayload: the key has a typed form but the payload does not
	// fit it.
	ErrMalformedPayload = errors.New("malformed event payload")
)

// Typed returns the business-facing form dws prints for the event's key: a
// *MessageEvent, *ReadEvent, *RecallEvent, *ReactionEvent,
// *GroupMemberEvent, *GroupLifecycleEvent, one of the seven *Approval…Event
// types, *VoIPInviteEvent, *TodoCreatedEvent, *TodoUpdatedEvent,
// *TodoDeletedEvent or *CardActionEvent.
//
// A malformed event or a key without a typed form returns ErrNoTypedForm; a
// payload that does not fit its key returns an error wrapping
// ErrMalformedPayload.
func (e Event) Typed() (any, error) {
	project, ok := projectors[e.Key]
	if e.Malformed || !ok {
		return nil, ErrNoTypedForm
	}
	h, payload, err := e.typedHeader()
	var v any
	if err == nil {
		v, err = project(h, payload)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrMalformedPayload, e.Key, err)
	}
	return v, nil
}

// EventHeader is what every typed form carries.
type EventHeader struct {
	Type        string `json:"type"`         // the event key
	EventID     string `json:"event_id"`     // the data's eventId, else Event.ID
	Timestamp   int64  `json:"timestamp"`    // occurredAtMs, Unix ms
	SubscribeID string `json:"subscribe_id"` // the subscription that matched
}

// MessageEvent is a received message (the user_im_message_receive_* keys).
// CreateTime is DingTalk's formatted time; EventTime is Unix ms.
type MessageEvent struct {
	EventHeader
	MessageID            string           `json:"message_id"`
	ConversationID       string           `json:"conversation_id"`
	Sender               string           `json:"sender"`
	SenderOpenDingTalkID string           `json:"sender_open_dingtalk_id"`
	Content              string           `json:"content"`
	CreateTime           string           `json:"create_time"`
	EventTime            int64            `json:"event_time"`
	QuotedMessage        *MessageContext  `json:"quoted_message,omitempty"`
	ForwardMessages      []MessageContext `json:"forward_messages,omitempty"`
}

// MessageContext is a message nested in another: the one a reply quotes, or
// one of a merged forward. Read it here, not from the outer Content, which
// is a localized summary. Sender may be empty or "null" when DingTalk omits
// it; a media message's Content carries its mediaId.
type MessageContext struct {
	MessageID            string `json:"message_id"`
	ConversationID       string `json:"conversation_id"`
	Sender               string `json:"sender"`
	SenderOpenDingTalkID string `json:"sender_open_dingtalk_id"`
	Content              string `json:"content"`
	CreateTime           string `json:"create_time"`
}

// ReadEvent is a message being read (user_im_message_read_o2o/_group).
type ReadEvent struct {
	EventHeader
	MessageID            string `json:"message_id"`
	ConversationID       string `json:"conversation_id"`
	Reader               string `json:"reader"`
	ReaderOpenDingTalkID string `json:"reader_open_dingtalk_id"`
	Sender               string `json:"sender"`
	SenderOpenDingTalkID string `json:"sender_open_dingtalk_id"`
	ReadTime             string `json:"read_time"`
	EventTime            int64  `json:"event_time"`
}

// RecallEvent is a message being recalled (user_im_message_recall_o2o/_group).
type RecallEvent struct {
	EventHeader
	MessageID              string `json:"message_id"`
	ConversationID         string `json:"conversation_id"`
	Recaller               string `json:"recaller"`
	RecallerOpenDingTalkID string `json:"recaller_open_dingtalk_id"`
	Sender                 string `json:"sender"`
	SenderOpenDingTalkID   string `json:"sender_open_dingtalk_id"`
	RecallTime             string `json:"recall_time"`
	EventTime              int64  `json:"event_time"`
}

// ReactionEvent is an emoji reaction on a message
// (user_im_message_reaction_o2o/_group).
type ReactionEvent struct {
	EventHeader
	MessageID              string `json:"message_id"`
	ConversationID         string `json:"conversation_id"`
	Operator               string `json:"operator"`
	OperatorOpenDingTalkID string `json:"operator_open_dingtalk_id"`
	ReactionName           string `json:"reaction_name"`
	ReactionText           string `json:"reaction_text"`
	OperationType          string `json:"operation_type"`
	OperationTime          string `json:"operation_time"`
	Sender                 string `json:"sender"`
	SenderOpenDingTalkID   string `json:"sender_open_dingtalk_id"`
	EventTime              int64  `json:"event_time"`
}

// GroupMemberEvent is members joining (user_im_group_member_added) or
// leaving (user_im_group_member_exited) a group. The operator is empty for a
// system change or a member leaving on their own.
type GroupMemberEvent struct {
	EventHeader
	ConversationID         string        `json:"conversation_id"`
	Operator               string        `json:"operator"`
	OperatorOpenDingTalkID string        `json:"operator_open_dingtalk_id"`
	Members                []GroupMember `json:"members"`
	EventTime              int64         `json:"event_time"`
}

// GroupMember is one member who joined or left.
type GroupMember struct {
	Nick           string `json:"nick"`
	OpenDingTalkID string `json:"open_dingtalk_id"`
}

// GroupLifecycleEvent is a group being updated or disbanded
// (user_im_group_updated/_disbanded). Without stable payload samples yet,
// the payload is kept whole, numbers as json.Number, minus the transport
// fields at its top level (uid, corpid, clientId, filterSubId, bizid, orgId,
// sourceId).
type GroupLifecycleEvent struct {
	EventHeader
	Payload map[string]any `json:"payload"`
}

// CardActionEvent is an interactive card callback
// (user_card_action_triggered). The payload is kept whole like
// GroupLifecycleEvent's, so fields the card business adds survive;
// payload.body.actionData.context holds the structured answers.
type CardActionEvent struct {
	EventHeader
	Payload map[string]any `json:"payload"`
}

// Approval is the approval instance an approval event is about. StaffID,
// ActivityID, CorpID and BusinessID are omitted when DingTalk leaves them
// empty; times are Unix ms.
type Approval struct {
	ProcessInstanceID string `json:"process_instance_id"`
	ProcessCode       string `json:"process_code"`
	StaffID           string `json:"staff_id,omitempty"`
	ActivityID        string `json:"activity_id,omitempty"`
	CorpID            string `json:"corp_id,omitempty"`
	BusinessID        string `json:"business_id,omitempty"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	CreateTime        int64  `json:"create_time"`
}

// ApprovalTaskCreatedEvent is an approval task reaching the user
// (user_oa_approval_task_created).
type ApprovalTaskCreatedEvent struct {
	EventHeader
	Approval
	TaskID    string `json:"task_id"`
	EventTime int64  `json:"event_time"`
}

// ApprovalTaskFinishedEvent is an approval task being handled
// (user_oa_approval_task_finished); Result is DingTalk's value.
type ApprovalTaskFinishedEvent struct {
	EventHeader
	Approval
	TaskID     string `json:"task_id"`
	Result     string `json:"result"`
	FinishTime int64  `json:"finish_time"`
	EventTime  int64  `json:"event_time"`
}

// ApprovalTaskRedirectedEvent is an approval task being handed to someone
// else (user_oa_approval_task_redirected); TaskID, Status, CreateTime and
// FinishTime describe the original task.
type ApprovalTaskRedirectedEvent struct {
	EventHeader
	Approval
	TaskID     string `json:"task_id"`
	Result     string `json:"result"`
	FinishTime int64  `json:"finish_time"`
	EventTime  int64  `json:"event_time"`
}

// ApprovalInstanceStartedEvent is an approval instance being started
// (user_oa_approval_instance_started).
type ApprovalInstanceStartedEvent struct {
	EventHeader
	Approval
	EventTime int64 `json:"event_time"`
}

// ApprovalInstanceCCEvent is the user being copied on an approval instance
// (user_oa_approval_instance_cc); CCTime is omitted when DingTalk has none.
type ApprovalInstanceCCEvent struct {
	EventHeader
	Approval
	CCTime    *int64 `json:"cc_time,omitempty"`
	EventTime int64  `json:"event_time"`
}

// ApprovalInstanceTerminatedEvent is an approval instance being terminated
// (user_oa_approval_instance_terminated).
type ApprovalInstanceTerminatedEvent struct {
	EventHeader
	Approval
	FinishTime int64 `json:"finish_time"`
	EventTime  int64 `json:"event_time"`
}

// ApprovalInstanceFinishedEvent is an approval instance completing
// (user_oa_approval_instance_finished); Result is DingTalk's value.
type ApprovalInstanceFinishedEvent struct {
	EventHeader
	Approval
	Result     string `json:"result"`
	FinishTime int64  `json:"finish_time"`
	EventTime  int64  `json:"event_time"`
}

// VoIPInviteEvent is the user being called (user_voip_call_receive_invite).
// BizID is the business event's id, stable across retries, so dedupe on it;
// EventID is the transport's. TargetUID is the invited user.
//
// It never carries the room code, the credential that joins the call: like
// dws, the projection does not read it. Event.Body and Event.Data still do.
type VoIPInviteEvent struct {
	EventHeader
	BizID        string `json:"biz_id"`
	CorpID       string `json:"corp_id"`
	OrgID        int64  `json:"org_id"`
	TargetUID    int64  `json:"target_uid"`
	CallID       string `json:"call_id"`
	CallerUID    string `json:"caller_uid"`
	CallerCorpID string `json:"caller_corp_id"`
	CalleeUID    string `json:"callee_uid"`
	CalleeCorpID string `json:"callee_corp_id"`
	CallType     string `json:"call_type"`
	RoomID       string `json:"room_id"`
	CreateTime   int64  `json:"create_time"`
	EventTime    int64  `json:"event_time"`
}

// Todo is the todo task a create or update event describes. IDs are
// staffIds; StatusStage is 0 not started, 1 in progress, 2 done, 3 ended
// abnormally; times are Unix ms, dates nil when unset.
type Todo struct {
	TaskID          string   `json:"task_id"`
	Subject         string   `json:"subject"`
	CreatorID       string   `json:"creator_id"`
	ExecutorIDs     []string `json:"executor_ids"`
	ParticipantIDs  []string `json:"participant_ids"`
	Priority        int64    `json:"priority"`
	StatusStage     int64    `json:"status_stage"`
	PlanStartDate   *int64   `json:"plan_start_date,omitempty"`
	PlanFinishDate  *int64   `json:"plan_finish_date,omitempty"`
	StartDate       *int64   `json:"start_date,omitempty"`
	FinishDate      *int64   `json:"finish_date,omitempty"`
	Description     string   `json:"description"`
	Source          string   `json:"source"`
	SourceID        string   `json:"source_id"`
	BizTag          string   `json:"biz_tag"`
	ParentID        *string  `json:"parent_id,omitempty"`
	IsMultiExecutor bool     `json:"is_multi_executor"`
	SceneType       string   `json:"scene_type"`
	CreateTime      int64    `json:"create_time"`
}

// TodoCreatedEvent is a todo being created (user_todo_task_create).
type TodoCreatedEvent struct {
	EventHeader
	Todo
}

// TodoUpdatedEvent is a todo being updated (user_todo_task_update);
// StatusStage is the new stage, OldStatusStage the one before.
type TodoUpdatedEvent struct {
	EventHeader
	Todo
	OldStatusStage int64 `json:"old_status_stage"`
	UpdateTime     int64 `json:"update_time"`
}

// TodoDeletedEvent is a todo being deleted (user_todo_task_delete).
type TodoDeletedEvent struct {
	EventHeader
	TaskID     string `json:"task_id"`
	Subject    string `json:"subject"`
	CreatorID  string `json:"creator_id"`
	CreateTime int64  `json:"create_time"`
	DeleteTime int64  `json:"delete_time"`
}

type projector func(h EventHeader, payload json.RawMessage) (any, error)

var projectors = map[string]projector{
	dws.EventIMAt:              projectMessage,
	dws.EventIMSingleChat:      projectMessage,
	dws.EventIMGroup:           projectMessage,
	dws.EventIMFromUser:        projectMessage,
	dws.EventIMAllSingleChats:  projectMessage,
	dws.EventIMAllGroups:       projectMessage,
	dws.EventIMReadSingleChat:  projectRead,
	dws.EventIMReadGroup:       projectRead,
	dws.EventIMRecallSingle:    projectRecall,
	dws.EventIMRecallGroup:     projectRecall,
	dws.EventIMReactionSingle:  projectReaction,
	dws.EventIMReactionGroup:   projectReaction,
	dws.EventGroupMemberAdded:  projectGroupMember,
	dws.EventGroupMemberExited: projectGroupMember,
	dws.EventGroupUpdated:      projectGroupLifecycle,
	dws.EventGroupDisbanded:    projectGroupLifecycle,
	dws.EventApprovalTaskNew:   projectApproval,
	dws.EventApprovalTaskDone:  projectApproval,
	dws.EventApprovalTaskMoved: projectApproval,
	dws.EventApprovalStarted:   projectApproval,
	dws.EventApprovalCC:        projectApproval,
	dws.EventApprovalStopped:   projectApproval,
	dws.EventApprovalFinished:  projectApproval,
	dws.EventVoIPInvite:        projectVoIPInvite,
	dws.EventTodoCreated:       projectTodo,
	dws.EventTodoUpdated:       projectTodo,
	dws.EventTodoDeleted:       projectTodo,
	dws.EventCardAction:        projectCardAction,
}

// typedHeader reads the frame data like dws: the event id and timestamp
// from the data first, the subscription from the frame headers first.
func (e Event) typedHeader() (EventHeader, json.RawMessage, error) {
	data := unwrapJSON(e.Data)
	if data == nil {
		return EventHeader{}, nil, errors.New("event data is missing")
	}
	var d struct {
		EventID      string          `json:"eventId"`
		SubID        string          `json:"subId"`
		OccurredAtMs int64           `json:"occurredAtMs"`
		Payload      json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return EventHeader{}, nil, err
	}
	h := EventHeader{
		Type:        e.Key,
		EventID:     firstNonEmpty(d.EventID, e.ID),
		Timestamp:   d.OccurredAtMs,
		SubscribeID: firstNonEmpty(e.SubscriptionID, d.SubID),
	}
	if h.Timestamp == 0 && !e.OccurredAt.IsZero() {
		h.Timestamp = e.OccurredAt.UnixMilli()
	}
	return h, unwrapJSON(d.Payload), nil
}

// Wire shapes that map one to one onto a typed struct share its field names
// and types, so they convert (MessageContext(w)) instead of being copied.

type wireMessage struct {
	MessageID            string `json:"openMessageId"`
	ConversationID       string `json:"openConversationId"`
	Sender               string `json:"sender"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
	Content              string `json:"content"`
	CreateTime           string `json:"createTime"`
}

type wireApproval struct {
	ProcessInstanceID string `json:"processInstanceId"`
	ProcessCode       string `json:"processCode"`
	StaffID           string `json:"staffId"`
	ActivityID        string `json:"activityId"`
	CorpID            string `json:"corpId"`
	BusinessID        string `json:"businessId"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	CreateTime        int64  `json:"createTime"`
}

type wireTodo struct {
	TaskID          string   `json:"taskId"`
	Subject         string   `json:"subject"`
	CreatorID       string   `json:"creatorId"`
	ExecutorIDs     []string `json:"executorIds"`
	ParticipantIDs  []string `json:"participantIds"`
	Priority        int64    `json:"priority"`
	StatusStage     int64    `json:"statusStage"`
	PlanStartDate   *int64   `json:"planStartDate"`
	PlanFinishDate  *int64   `json:"planFinishDate"`
	StartDate       *int64   `json:"startDate"`
	FinishDate      *int64   `json:"finishDate"`
	Description     string   `json:"description"`
	Source          string   `json:"source"`
	SourceID        string   `json:"sourceId"`
	BizTag          string   `json:"bizTag"`
	ParentID        *string  `json:"parentId"`
	IsMultiExecutor bool     `json:"isMultiExecutor"`
	SceneType       string   `json:"sceneType"`
	CreateTime      int64    `json:"createTime"`
}

// eventTime is the business time most payloads carry beside the body.
type eventTime struct {
	EventTime int64 `json:"event_time"`
}

func projectMessage(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		wireMessage
		QuotedMessage   *wireMessage  `json:"quotedMessage"`
		ForwardMessages []wireMessage `json:"forwardMessages"`
	}
	if _, err := decodeRequired(payload, &top, &b); err != nil {
		return nil, err
	}
	ev := &MessageEvent{
		EventHeader:          h,
		MessageID:            b.MessageID,
		ConversationID:       b.ConversationID,
		Sender:               b.Sender,
		SenderOpenDingTalkID: b.SenderOpenDingTalkID,
		Content:              b.Content,
		CreateTime:           b.CreateTime,
		EventTime:            top.EventTime,
	}
	if b.QuotedMessage != nil {
		quoted := MessageContext(*b.QuotedMessage)
		ev.QuotedMessage = &quoted
	}
	for _, m := range b.ForwardMessages {
		ev.ForwardMessages = append(ev.ForwardMessages, MessageContext(m))
	}
	return ev, nil
}

func projectRead(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		MessageID            string `json:"openMessageId"`
		ConversationID       string `json:"openConversationId"`
		Reader               string `json:"reader"`
		ReaderOpenDingTalkID string `json:"readerOpenDingTalkId"`
		Sender               string `json:"sender"`
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		ReadTime             string `json:"msgReadTime"`
	}
	if _, err := decodeRequired(payload, &top, &b); err != nil {
		return nil, err
	}
	return &ReadEvent{
		EventHeader:          h,
		MessageID:            b.MessageID,
		ConversationID:       b.ConversationID,
		Reader:               b.Reader,
		ReaderOpenDingTalkID: b.ReaderOpenDingTalkID,
		Sender:               b.Sender,
		SenderOpenDingTalkID: b.SenderOpenDingTalkID,
		ReadTime:             b.ReadTime,
		EventTime:            top.EventTime,
	}, nil
}

func projectRecall(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		MessageID              string `json:"openMessageId"`
		ConversationID         string `json:"openConversationId"`
		Recaller               string `json:"recaller"`
		RecallerOpenDingTalkID string `json:"recallerOpenDingTalkId"`
		Sender                 string `json:"sender"`
		SenderOpenDingTalkID   string `json:"senderOpenDingTalkId"`
		RecallTime             string `json:"msgRecallTime"`
	}
	if _, err := decodeRequired(payload, &top, &b); err != nil {
		return nil, err
	}
	return &RecallEvent{
		EventHeader:            h,
		MessageID:              b.MessageID,
		ConversationID:         b.ConversationID,
		Recaller:               b.Recaller,
		RecallerOpenDingTalkID: b.RecallerOpenDingTalkID,
		Sender:                 b.Sender,
		SenderOpenDingTalkID:   b.SenderOpenDingTalkID,
		RecallTime:             b.RecallTime,
		EventTime:              top.EventTime,
	}, nil
}

func projectReaction(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		MessageID            string `json:"openSourceMessageId"`
		ConversationID       string `json:"openConversationId"`
		Operator             string `json:"oper"`
		ReactionName         string `json:"emotionName"`
		ReactionText         string `json:"emotionText"`
		OperationType        string `json:"operateType"`
		OperationTime        string `json:"operateTime"`
		Sender               string `json:"sender"`
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
	}
	fields, err := decodeRequired(payload, &top, &b)
	if err != nil {
		return nil, err
	}
	operatorID, err := exactString(fields, "operOpenDingtalkId")
	if err != nil {
		return nil, err
	}
	return &ReactionEvent{
		EventHeader:            h,
		MessageID:              b.MessageID,
		ConversationID:         b.ConversationID,
		Operator:               b.Operator,
		OperatorOpenDingTalkID: operatorID,
		ReactionName:           b.ReactionName,
		ReactionText:           b.ReactionText,
		OperationType:          b.OperationType,
		OperationTime:          b.OperationTime,
		Sender:                 b.Sender,
		SenderOpenDingTalkID:   b.SenderOpenDingTalkID,
		EventTime:              top.EventTime,
	}, nil
}

func projectGroupMember(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		ConversationID string `json:"openConversationId"`
		Operator       string `json:"operNick"`
		Members        []struct {
			Nick           string `json:"nick"`
			OpenDingTalkID string `json:"openDingTalkId"`
		} `json:"members"`
	}
	fields, err := decodeRequired(payload, &top, &b)
	if err != nil {
		return nil, err
	}
	operatorID, err := exactString(fields, "operOpenDingtalkId")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(b.ConversationID) == "" {
		return nil, errors.New("openConversationId is required")
	}
	if len(b.Members) == 0 {
		return nil, errors.New("members is required")
	}
	ev := &GroupMemberEvent{
		EventHeader:            h,
		ConversationID:         b.ConversationID,
		Operator:               b.Operator,
		OperatorOpenDingTalkID: operatorID,
		Members:                make([]GroupMember, 0, len(b.Members)),
		EventTime:              top.EventTime,
	}
	for _, m := range b.Members {
		ev.Members = append(ev.Members, GroupMember(m))
	}
	return ev, nil
}

func projectGroupLifecycle(h EventHeader, payload json.RawMessage) (any, error) {
	p, err := decodeConservative(payload)
	if err != nil {
		return nil, err
	}
	return &GroupLifecycleEvent{EventHeader: h, Payload: p}, nil
}

func projectCardAction(h EventHeader, payload json.RawMessage) (any, error) {
	p, err := decodeConservative(payload)
	if err != nil {
		return nil, err
	}
	return &CardActionEvent{EventHeader: h, Payload: p}, nil
}

func projectApproval(h EventHeader, payload json.RawMessage) (any, error) {
	var top eventTime
	var b struct {
		wireApproval
		TaskID     string `json:"taskId"`
		Result     string `json:"result"`
		FinishTime int64  `json:"finishTime"`
		CCTime     *int64 `json:"ccTime"`
	}
	if _, err := decodeRequired(payload, &top, &b); err != nil {
		return nil, err
	}
	if strings.TrimSpace(b.ProcessInstanceID) == "" {
		return nil, errors.New("processInstanceId is required")
	}
	a := Approval(b.wireApproval)
	switch h.Type {
	case dws.EventApprovalTaskNew, dws.EventApprovalTaskDone, dws.EventApprovalTaskMoved:
		if strings.TrimSpace(b.TaskID) == "" {
			return nil, errors.New("taskId is required")
		}
	}
	switch h.Type {
	case dws.EventApprovalTaskNew:
		return &ApprovalTaskCreatedEvent{EventHeader: h, Approval: a, TaskID: b.TaskID, EventTime: top.EventTime}, nil
	case dws.EventApprovalTaskDone:
		return &ApprovalTaskFinishedEvent{EventHeader: h, Approval: a, TaskID: b.TaskID,
			Result: b.Result, FinishTime: b.FinishTime, EventTime: top.EventTime}, nil
	case dws.EventApprovalTaskMoved:
		return &ApprovalTaskRedirectedEvent{EventHeader: h, Approval: a, TaskID: b.TaskID,
			Result: b.Result, FinishTime: b.FinishTime, EventTime: top.EventTime}, nil
	case dws.EventApprovalStarted:
		return &ApprovalInstanceStartedEvent{EventHeader: h, Approval: a, EventTime: top.EventTime}, nil
	case dws.EventApprovalCC:
		return &ApprovalInstanceCCEvent{EventHeader: h, Approval: a, CCTime: b.CCTime, EventTime: top.EventTime}, nil
	case dws.EventApprovalStopped:
		return &ApprovalInstanceTerminatedEvent{EventHeader: h, Approval: a, FinishTime: b.FinishTime, EventTime: top.EventTime}, nil
	case dws.EventApprovalFinished:
		return &ApprovalInstanceFinishedEvent{EventHeader: h, Approval: a,
			Result: b.Result, FinishTime: b.FinishTime, EventTime: top.EventTime}, nil
	}
	return nil, fmt.Errorf("no approval form for %s", h.Type)
}

func projectVoIPInvite(h EventHeader, payload json.RawMessage) (any, error) {
	var top struct {
		BizID     string `json:"bizid"`
		EventTime int64  `json:"event_time"`
		CorpID    string `json:"corpid"`
		OrgID     int64  `json:"orgId"`
		UID       int64  `json:"uid"`
	}
	var b struct {
		CallID       string  `json:"callId"`
		CallerUID    voipUID `json:"callerUid"`
		CallerCorpID string  `json:"callerCorpId"`
		CalleeUID    voipUID `json:"calleeUid"`
		CalleeCorpID string  `json:"calleeCorpId"`
		CallType     string  `json:"callType"`
		RoomID       string  `json:"roomId"`
		CreateTime   int64   `json:"createTime"`
	}
	if _, err := decodeRequired(payload, &top, &b); err != nil {
		return nil, err
	}
	if strings.TrimSpace(top.BizID) == "" {
		return nil, errors.New("bizid is required")
	}
	return &VoIPInviteEvent{
		EventHeader:  h,
		BizID:        top.BizID,
		CorpID:       top.CorpID,
		OrgID:        top.OrgID,
		TargetUID:    top.UID,
		CallID:       b.CallID,
		CallerUID:    string(b.CallerUID),
		CallerCorpID: b.CallerCorpID,
		CalleeUID:    string(b.CalleeUID),
		CalleeCorpID: b.CalleeCorpID,
		CallType:     b.CallType,
		RoomID:       b.RoomID,
		CreateTime:   b.CreateTime,
		EventTime:    top.EventTime,
	}, nil
}

// voipUID is a VoIP user id: a string, or an integer from providers that
// predate the string form.
type voipUID string

func (id *voipUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*id = voipUID(s)
		return nil
	}
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("VoIP user identifier must be a string or legacy integer: %w", err)
	}
	*id = voipUID(strconv.FormatInt(n, 10))
	return nil
}

func projectTodo(h EventHeader, payload json.RawMessage) (any, error) {
	var b struct {
		wireTodo
		OldStatusStage int64 `json:"oldStatusStage"`
		UpdateTime     int64 `json:"updateTime"`
		DeleteTime     int64 `json:"deleteTime"`
	}
	if _, err := decodeRequired(payload, nil, &b); err != nil {
		return nil, err
	}
	if strings.TrimSpace(b.TaskID) == "" {
		return nil, errors.New("taskId is required")
	}
	switch h.Type {
	case dws.EventTodoCreated:
		return &TodoCreatedEvent{EventHeader: h, Todo: Todo(b.wireTodo)}, nil
	case dws.EventTodoUpdated:
		return &TodoUpdatedEvent{EventHeader: h, Todo: Todo(b.wireTodo),
			OldStatusStage: b.OldStatusStage, UpdateTime: b.UpdateTime}, nil
	case dws.EventTodoDeleted:
		return &TodoDeletedEvent{EventHeader: h, TaskID: b.TaskID, Subject: b.Subject,
			CreatorID: b.CreatorID, CreateTime: b.CreateTime, DeleteTime: b.DeleteTime}, nil
	}
	return nil, fmt.Errorf("no todo form for %s", h.Type)
}

// decodeRequired decodes a payload that must carry a non-empty body object:
// top (when not nil) from the payload, body from its body. It returns the
// body's fields for reads by exact spelling.
func decodeRequired(payload json.RawMessage, top, body any) (map[string]json.RawMessage, error) {
	if payload == nil {
		return nil, errors.New("payload is missing")
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, errors.New("payload is empty")
	}
	raw := unwrapJSON(p["body"])
	if raw == nil {
		return nil, errors.New("payload body is missing")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode payload body: %w", err)
	}
	if len(fields) == 0 {
		return nil, errors.New("payload body is empty")
	}
	if top != nil {
		if err := json.Unmarshal(payload, top); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(raw, body); err != nil {
		return nil, err
	}
	return fields, nil
}

// exactString reads a body field by its exact spelling: encoding/json would
// also match other casings (operOpenDingTalkId), which dws rejects.
func exactString(fields map[string]json.RawMessage, name string) (string, error) {
	var s string
	if raw, ok := fields[name]; ok {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("decode %s: %w", name, err)
		}
	}
	return s, nil
}

// decodeConservative keeps a payload whole, numbers exact, minus the
// transport fields dws strips from its top level. Nested fields, such as a
// business uid inside body, are kept.
func decodeConservative(payload json.RawMessage) (map[string]any, error) {
	if payload == nil {
		return nil, errors.New("payload is missing")
	}
	var p map[string]any
	if err := decodeNumbers(payload, &p); err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, errors.New("payload is empty")
	}
	for k := range p {
		switch strings.ToLower(k) {
		case "uid", "corpid", "clientid", "filtersubid", "bizid", "orgid", "sourceid":
			delete(p, k)
		}
	}
	if s, ok := p["body"].(string); ok {
		var body map[string]any
		if decodeNumbers(unwrapJSON(json.RawMessage(s)), &body) == nil && body != nil {
			p["body"] = body
		}
	}
	return p, nil
}

func decodeNumbers(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return d.Decode(v)
}
