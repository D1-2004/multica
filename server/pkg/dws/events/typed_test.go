// Fixtures ported from dingtalk-workspace-cli internal/event/personal
// output_test.go, output_todo_test.go and output_card_test.go (Copyright
// 2026 Alibaba Group, Apache License 2.0); each builds the Event through
// decodeEvent instead of a dws transport.Event.

package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// outer is the transport envelope dws's fixtures come with, as frame headers.
func outer(key string) map[string]any {
	return map[string]any{"eventId": "outer-event", "eventType": key, "SUB_ID": "outer-sub"}
}

func decodeFixture(t *testing.T, headers map[string]any, data string) Event {
	t.Helper()
	ev, err := decodeEvent(frame{Type: "EVENT", Headers: headers, Data: data})
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}
	return ev
}

func mustTyped(t *testing.T, headers map[string]any, data string) any {
	t.Helper()
	return mustTypedEvent(t, decodeFixture(t, headers, data))
}

func mustTypedEvent(t *testing.T, ev Event) any {
	t.Helper()
	v, err := ev.Typed()
	if err != nil {
		t.Fatalf("Typed: %v", err)
	}
	return v
}

func wantMalformed(t *testing.T, headers map[string]any, data, detail string) {
	t.Helper()
	v, err := decodeFixture(t, headers, data).Typed()
	if !errors.Is(err, ErrMalformedPayload) || v != nil || !strings.Contains(err.Error(), detail) {
		t.Fatalf("Typed = %#v, %v; want nil and ErrMalformedPayload with %q", v, err, detail)
	}
}

// marshalled fails when the typed form's JSON carries any of absent.
func marshalled(t *testing.T, v any, absent ...string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range absent {
		if strings.Contains(string(raw), s) {
			t.Fatalf("JSON carries %s: %s", s, raw)
		}
	}
	return string(raw)
}

// internalFields are transport fields no flat typed form may carry.
var internalFields = []string{`"payload"`, `"uid"`, `"corpid"`, `"clientId"`, `"filterSubId"`, `"bizid"`}

func messageData(key string) string {
	return fmt.Sprintf(`{
		"eventId":"data-event","eventKey":%q,"occurredAtMs":1783483236995,"subId":"data-sub",
		"payload":{
			"body":{
				"createTime":"2026-07-08 12:00:35","sender":"测试用户甲","openMessageId":"msg-1",
				"senderOpenDingTalkId":"open-user-1","openConversationId":"cid-1","content":"在吗"
			},
			"event_time":1783483235983
		}
	}`, key)
}

func TestTypedMessage(t *testing.T) {
	for _, key := range []string{dws.EventIMAt, dws.EventIMSingleChat, dws.EventIMGroup,
		dws.EventIMFromUser, dws.EventIMAllSingleChats, dws.EventIMAllGroups} {
		ev := decodeFixture(t, outer(key), messageData(key))
		got, err := ev.Typed()
		want := &MessageEvent{
			EventHeader:          EventHeader{Type: key, EventID: "data-event", Timestamp: 1783483236995, SubscribeID: "outer-sub"},
			MessageID:            "msg-1",
			ConversationID:       "cid-1",
			Sender:               "测试用户甲",
			SenderOpenDingTalkID: "open-user-1",
			Content:              "在吗",
			CreateTime:           "2026-07-08 12:00:35",
			EventTime:            1783483235983,
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Typed = %#v, %v", key, got, err)
		}
		marshalled(t, got, "quoted_message", "forward_messages")

		// A host that stored the Event as JSON gets the same typed form back.
		raw, _ := json.Marshal(ev)
		var stored Event
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		if again, err := stored.Typed(); err != nil || !reflect.DeepEqual(again, want) {
			t.Fatalf("%s: stored Typed = %#v, %v", key, again, err)
		}
	}
}

func TestTypedMessageQuotedAndForwarded(t *testing.T) {
	quoted := mustTyped(t, outer(dws.EventIMGroup), `{
		"eventId":"quoted-event","eventKey":"user_im_message_receive_group","occurredAtMs":1784792292580,"subId":"quoted-sub",
		"payload":{
			"body":{
				"createTime":"2026-07-23 15:38:11","sender":"郑御白","openMessageId":"outer-message",
				"senderOpenDingTalkId":"outer-sender-open-id","openConversationId":"target-conversation","content":"引用回复",
				"quotedMessage":{
					"createTime":"2026-07-23 15:35:03","sender":"null","openMessageId":"quoted-message",
					"senderOpenDingTalkId":"quoted-sender-open-id","openConversationId":"source-conversation","content":"被引用的原消息"
				}
			},
			"event_time":1784792291637
		}
	}`).(*MessageEvent)
	wantQuoted := &MessageContext{MessageID: "quoted-message", ConversationID: "source-conversation", Sender: "null",
		SenderOpenDingTalkID: "quoted-sender-open-id", Content: "被引用的原消息", CreateTime: "2026-07-23 15:35:03"}
	if !reflect.DeepEqual(quoted.QuotedMessage, wantQuoted) || quoted.ForwardMessages != nil || quoted.MessageID != "outer-message" {
		t.Fatalf("quoted = %#v", quoted)
	}

	forward := mustTyped(t, outer(dws.EventIMGroup), `{
		"eventId":"forward-event","eventKey":"user_im_message_receive_group","occurredAtMs":1784861030151,"subId":"forward-sub",
		"payload":{
			"body":{
				"createTime":"2026-07-24 10:43:49","sender":"郑御白","openMessageId":"outer-forward-message",
				"senderOpenDingTalkId":"outer-sender-open-id","openConversationId":"target-conversation",
				"content":"Chat history between two users\nUser A:[Image]\nUser A:Forwarded chat record",
				"forwardMessages":[
					{"createTime":"2026-07-24 10:33:31","sender":"null","openMessageId":"image-message",
					 "senderOpenDingTalkId":"image-sender-open-id","openConversationId":"source-conversation",
					 "content":"[图片消息](mediaId=media-1) 注意：如需下载使用dws chat message download-media命令下载"},
					{"createTime":"2026-07-24 10:34:46","sender":"null","openMessageId":"text-message",
					 "openConversationId":"source-conversation","content":"转发聊天记录"}
				]
			},
			"event_time":1784861029265
		}
	}`).(*MessageEvent)
	wantForward := []MessageContext{
		{MessageID: "image-message", ConversationID: "source-conversation", Sender: "null", SenderOpenDingTalkID: "image-sender-open-id",
			Content: "[图片消息](mediaId=media-1) 注意：如需下载使用dws chat message download-media命令下载", CreateTime: "2026-07-24 10:33:31"},
		{MessageID: "text-message", ConversationID: "source-conversation", Sender: "null", Content: "转发聊天记录", CreateTime: "2026-07-24 10:34:46"},
	}
	if !reflect.DeepEqual(forward.ForwardMessages, wantForward) || forward.QuotedMessage != nil {
		t.Fatalf("forward = %#v", forward)
	}
	encoded := marshalled(t, forward, "quoted_message")
	for _, s := range []string{`"forward_messages"`, `"message_id":"image-message"`, `"conversation_id":"source-conversation"`, `mediaId=media-1`} {
		if !strings.Contains(encoded, s) {
			t.Fatalf("JSON lacks %s: %s", s, encoded)
		}
	}
}

func actionData(key, eventID string, occurredAtMs, eventTime int64, subID, body string) string {
	return fmt.Sprintf(`{
		"eventId":%q,"eventKey":%q,"occurredAtMs":%d,"subId":%q,
		"payload":{"bizid":"internal-bizid","body":%s,"clientId":"internal-client","corpid":"internal-corp",
			"event_time":%d,"filterSubId":"internal-filter","uid":100001}
	}`, eventID, key, occurredAtMs, subID, body, eventTime)
}

func readData(key string) string {
	return actionData(key, "read-event", 1784008412182, 1784008411652, "read-sub", `{
		"msgReadTime":"2026-07-14 13:53:31","openConversationId":"read-conversation","openMessageId":"read-message",
		"reader":"测试用户乙","readerOpenDingTalkId":"reader-open-id","sender":"测试用户甲","senderOpenDingTalkId":"sender-open-id"}`)
}

func recallData(key string) string {
	return actionData(key, "recall-event", 1784008592969, 1784008592766, "recall-sub", `{
		"msgRecallTime":"2026-07-14 13:56:32","openConversationId":"recall-conversation","openMessageId":"recall-message",
		"recaller":"测试用户乙","recallerOpenDingTalkId":"recaller-open-id","sender":"测试用户乙","senderOpenDingTalkId":"sender-open-id"}`)
}

func reactionData(key string) string {
	return actionData(key, "reaction-event", 1784008680072, 1784008679217, "reaction-sub", `{
		"emotionName":"微笑","emotionText":"微笑","openConversationId":"reaction-conversation",
		"openSourceMessageId":"reaction-message","oper":"测试用户乙","operOpenDingtalkId":"operator-open-id",
		"operateTime":"2026-07-14 13:57:59","operateType":"add","sender":"测试用户甲","senderOpenDingTalkId":"sender-open-id"}`)
}

func TestTypedReadRecallReaction(t *testing.T) {
	// No envelope subscription: the data's subId is used.
	check := func(key, data string, want any) {
		t.Helper()
		got := mustTyped(t, map[string]any{"eventType": key}, data)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Typed = %#v", key, got)
		}
		marshalled(t, got, internalFields...)
	}
	for _, key := range []string{dws.EventIMReadSingleChat, dws.EventIMReadGroup} {
		check(key, readData(key), &ReadEvent{
			EventHeader: EventHeader{Type: key, EventID: "read-event", Timestamp: 1784008412182, SubscribeID: "read-sub"},
			MessageID:   "read-message", ConversationID: "read-conversation", Reader: "测试用户乙", ReaderOpenDingTalkID: "reader-open-id",
			Sender: "测试用户甲", SenderOpenDingTalkID: "sender-open-id", ReadTime: "2026-07-14 13:53:31", EventTime: 1784008411652})
	}
	for _, key := range []string{dws.EventIMRecallSingle, dws.EventIMRecallGroup} {
		check(key, recallData(key), &RecallEvent{
			EventHeader: EventHeader{Type: key, EventID: "recall-event", Timestamp: 1784008592969, SubscribeID: "recall-sub"},
			MessageID:   "recall-message", ConversationID: "recall-conversation", Recaller: "测试用户乙", RecallerOpenDingTalkID: "recaller-open-id",
			Sender: "测试用户乙", SenderOpenDingTalkID: "sender-open-id", RecallTime: "2026-07-14 13:56:32", EventTime: 1784008592766})
	}
	for _, key := range []string{dws.EventIMReactionSingle, dws.EventIMReactionGroup} {
		check(key, reactionData(key), &ReactionEvent{
			EventHeader: EventHeader{Type: key, EventID: "reaction-event", Timestamp: 1784008680072, SubscribeID: "reaction-sub"},
			MessageID:   "reaction-message", ConversationID: "reaction-conversation", Operator: "测试用户乙", OperatorOpenDingTalkID: "operator-open-id",
			ReactionName: "微笑", ReactionText: "微笑", OperationType: "add", OperationTime: "2026-07-14 13:57:59",
			Sender: "测试用户甲", SenderOpenDingTalkID: "sender-open-id", EventTime: 1784008679217})
	}
}

func groupMemberData(key string) string {
	return fmt.Sprintf(`{
		"eventId":"group-member-event","eventKey":%q,"occurredAtMs":1784782513647,"subId":"group-member-sub",
		"payload":{
			"uid":100001,"clientId":"internal-client","corpid":"internal-corp","bizid":"internal-biz","filterSubId":"internal-filter",
			"body":{
				"operNick":"测试用户甲",
				"members":[{"nick":"测试用户乙","openDingTalkId":"member-open-id-1"},{"nick":"测试用户丙","openDingTalkId":"member-open-id-2"}],
				"operOpenDingtalkId":"operator-open-id",
				"openConversationId":"cid-group-1"
			},
			"event_time":1784782513502
		}
	}`, key)
}

func TestTypedGroupMember(t *testing.T) {
	for _, key := range []string{dws.EventGroupMemberAdded, dws.EventGroupMemberExited} {
		got := mustTyped(t, outer(key), groupMemberData(key))
		want := &GroupMemberEvent{
			EventHeader:            EventHeader{Type: key, EventID: "group-member-event", Timestamp: 1784782513647, SubscribeID: "outer-sub"},
			ConversationID:         "cid-group-1",
			Operator:               "测试用户甲",
			OperatorOpenDingTalkID: "operator-open-id",
			Members:                []GroupMember{{"测试用户乙", "member-open-id-1"}, {"测试用户丙", "member-open-id-2"}},
			EventTime:              1784782513502,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Typed = %#v", key, got)
		}
		marshalled(t, got, internalFields...)
	}

	// A member leaving on their own has no operator.
	data := strings.ReplaceAll(groupMemberData(dws.EventGroupMemberExited), `"operNick":"测试用户甲",`, "")
	data = strings.ReplaceAll(data, `"operOpenDingtalkId":"operator-open-id",`, "")
	if got := mustTyped(t, outer(dws.EventGroupMemberExited), data).(*GroupMemberEvent); got.Operator != "" ||
		got.OperatorOpenDingTalkID != "" || len(got.Members) != 2 {
		t.Fatalf("no operator: %#v", got)
	}

	for _, tt := range []struct{ body, detail string }{
		{`{"members":[{"nick":"测试用户甲","openDingTalkId":"member-1"}]}`, "openConversationId is required"},
		{`{"openConversationId":" ","members":[{"nick":"测试用户甲","openDingTalkId":"member-1"}]}`, "openConversationId is required"},
		{`{"openConversationId":"cid-1"}`, "members is required"},
		{`{"openConversationId":"cid-1","members":[]}`, "members is required"},
		{`{"openConversationId":"cid-1","members":"invalid"}`, "cannot unmarshal"},
		{`{"openConversationId":"cid-1","members":[{}],"operOpenDingtalkId":{"unexpected":true}}`, "decode operOpenDingtalkId"},
	} {
		wantMalformed(t, outer(dws.EventGroupMemberAdded),
			`{"eventKey":"user_im_group_member_added","payload":{"body":`+tt.body+`,"event_time":1}}`, tt.detail)
	}
}

func TestTypedReadsTheOperatorIDByExactSpelling(t *testing.T) {
	for _, legacy := range []string{"operOpenDingtlkId", "operOpenDingTalkId"} {
		reaction := strings.Replace(reactionData(dws.EventIMReactionSingle), "operOpenDingtalkId", legacy, 1)
		if got := mustTyped(t, outer(dws.EventIMReactionSingle), reaction).(*ReactionEvent); got.OperatorOpenDingTalkID != "" {
			t.Fatalf("reaction read %s: %#v", legacy, got)
		}
		member := strings.Replace(groupMemberData(dws.EventGroupMemberAdded), "operOpenDingtalkId", legacy, 1)
		if got := mustTyped(t, outer(dws.EventGroupMemberAdded), member).(*GroupMemberEvent); got.OperatorOpenDingTalkID != "" ||
			got.Members[0].OpenDingTalkID != "member-open-id-1" {
			t.Fatalf("group member read %s: %#v", legacy, got)
		}
	}
}

func TestTypedGroupLifecycle(t *testing.T) {
	for _, key := range []string{dws.EventGroupUpdated, dws.EventGroupDisbanded} {
		got := mustTyped(t, outer(key), fmt.Sprintf(`{
			"eventId":"group-event","eventKey":%q,"occurredAtMs":1784009000000,"subId":"data-sub",
			"payload":{
				"uid":100001,"CORPID":"internal-corp","clientId":"internal-client","filterSubId":"internal-filter",
				"bizid":"internal-biz","orgId":100002,"sourceId":"open","event_time":1784008999000,
				"body":{"openConversationId":"cid-group-1","title":"测试群新标题","operator":{"uid":"business-user-1"}}
			}
		}`, key)).(*GroupLifecycleEvent)
		if got.EventHeader != (EventHeader{Type: key, EventID: "group-event", Timestamp: 1784009000000, SubscribeID: "outer-sub"}) {
			t.Fatalf("header = %#v", got.EventHeader)
		}
		for _, internal := range []string{"uid", "CORPID", "clientId", "filterSubId", "bizid", "orgId", "sourceId"} {
			if _, ok := got.Payload[internal]; ok {
				t.Fatalf("payload kept %s: %#v", internal, got.Payload)
			}
		}
		body := got.Payload["body"].(map[string]any)
		if body["title"] != "测试群新标题" || body["operator"].(map[string]any)["uid"] != "business-user-1" ||
			got.Payload["event_time"] != json.Number("1784008999000") {
			t.Fatalf("payload = %#v", got.Payload)
		}
	}
	for _, tt := range []struct{ payload, detail string }{
		{``, "payload is missing"},
		{`,"payload":null`, "payload is missing"},
		{`,"payload":{}`, "payload is empty"},
		{`,"payload":[]`, "cannot unmarshal array"},
	} {
		wantMalformed(t, outer(dws.EventGroupUpdated), `{"eventKey":"user_im_group_updated"`+tt.payload+`}`, tt.detail)
	}
}

func approvalData(key string) string {
	body := map[string]any{"processInstanceId": "process-instance-1", "createTime": int64(1785229100000),
		"processCode": "PROC-TEST-1", "title": "测试审批"}
	switch key {
	case dws.EventApprovalTaskNew:
		body["taskId"], body["status"] = "approval-task-1", "RUNNING"
	case dws.EventApprovalTaskDone, dws.EventApprovalTaskMoved:
		body["taskId"], body["status"], body["finishTime"] = "approval-task-1", "FINISHED", int64(1785229199000)
		body["result"] = map[string]string{dws.EventApprovalTaskDone: "agree", dws.EventApprovalTaskMoved: "redirect"}[key]
	case dws.EventApprovalStarted, dws.EventApprovalCC:
		body["status"] = "RUNNING"
	case dws.EventApprovalStopped:
		body["status"], body["finishTime"] = "TERMINATED", int64(1785229199000)
	case dws.EventApprovalFinished:
		body["status"], body["result"], body["finishTime"] = "FINISHED", "agree", int64(1785229199000)
	}
	encoded, _ := json.Marshal(map[string]any{
		"eventId": "oa-event", "eventKey": key, "occurredAtMs": int64(1785229200123), "subId": "oa-data-sub",
		"payload": map[string]any{
			"uid": 100001, "CORPID": "internal-corp", "clientId": "internal-client", "filterSubId": "internal-filter",
			"bizid": "internal-biz", "orgId": 100002, "sourceId": "open", "body": body,
			"event_time": int64(1785229199000), "futureField": map[string]any{"nested": true},
		},
	})
	return string(encoded)
}

func TestTypedApproval(t *testing.T) {
	header := func(key string) EventHeader {
		return EventHeader{Type: key, EventID: "oa-event", Timestamp: 1785229200123, SubscribeID: "outer-sub"}
	}
	approval := func(status string) Approval {
		return Approval{ProcessInstanceID: "process-instance-1", ProcessCode: "PROC-TEST-1", Title: "测试审批", Status: status, CreateTime: 1785229100000}
	}
	const at = 1785229199000
	tests := map[string]any{
		dws.EventApprovalTaskNew: &ApprovalTaskCreatedEvent{EventHeader: header(dws.EventApprovalTaskNew),
			Approval: approval("RUNNING"), TaskID: "approval-task-1", EventTime: at},
		dws.EventApprovalTaskDone: &ApprovalTaskFinishedEvent{EventHeader: header(dws.EventApprovalTaskDone),
			Approval: approval("FINISHED"), TaskID: "approval-task-1", Result: "agree", FinishTime: at, EventTime: at},
		dws.EventApprovalTaskMoved: &ApprovalTaskRedirectedEvent{EventHeader: header(dws.EventApprovalTaskMoved),
			Approval: approval("FINISHED"), TaskID: "approval-task-1", Result: "redirect", FinishTime: at, EventTime: at},
		dws.EventApprovalStarted: &ApprovalInstanceStartedEvent{EventHeader: header(dws.EventApprovalStarted),
			Approval: approval("RUNNING"), EventTime: at},
		dws.EventApprovalCC: &ApprovalInstanceCCEvent{EventHeader: header(dws.EventApprovalCC),
			Approval: approval("RUNNING"), EventTime: at},
		dws.EventApprovalStopped: &ApprovalInstanceTerminatedEvent{EventHeader: header(dws.EventApprovalStopped),
			Approval: approval("TERMINATED"), FinishTime: at, EventTime: at},
		dws.EventApprovalFinished: &ApprovalInstanceFinishedEvent{EventHeader: header(dws.EventApprovalFinished),
			Approval: approval("FINISHED"), Result: "agree", FinishTime: at, EventTime: at},
	}
	for key, want := range tests {
		got := mustTyped(t, outer(key), approvalData(key))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Typed = %#v", key, got)
		}
		encoded := marshalled(t, got, append(internalFields, "staff_id", "cc_time")...)
		// Task events carry task_id; instance events do not.
		if isTask := strings.Contains(key, "_task_"); strings.Contains(encoded, `"task_id"`) != isTask {
			t.Fatalf("%s: JSON = %s", key, encoded)
		}
	}

	// Optional body fields appear when DingTalk sends them.
	cc := strings.Replace(approvalData(dws.EventApprovalCC), `"processCode"`, `"ccTime":1785229150000,"staffId":"staff-1","processCode"`, 1)
	if got := mustTyped(t, outer(dws.EventApprovalCC), cc).(*ApprovalInstanceCCEvent); got.CCTime == nil ||
		*got.CCTime != 1785229150000 || got.StaffID != "staff-1" {
		t.Fatalf("cc = %#v", got)
	}

	// Doubly JSON-encoded frame data still decodes, keys and all.
	once, _ := json.Marshal(approvalData(dws.EventApprovalTaskNew))
	twice, _ := json.Marshal(string(once))
	if got := mustTyped(t, nil, string(twice)).(*ApprovalTaskCreatedEvent); got.Type != dws.EventApprovalTaskNew ||
		got.EventID != "oa-event" || got.SubscribeID != "oa-data-sub" {
		t.Fatalf("wrapped = %#v", got)
	}

	wantMalformed(t, outer(dws.EventApprovalStarted),
		`{"eventKey":"user_oa_approval_instance_started","payload":{"body":{"status":"RUNNING"},"event_time":1}}`, "processInstanceId is required")
	wantMalformed(t, outer(dws.EventApprovalTaskNew),
		`{"eventKey":"user_oa_approval_task_created","payload":{"body":{"processInstanceId":"p-1","status":"RUNNING"},"event_time":1}}`, "taskId is required")
	for _, key := range []string{dws.EventApprovalTaskDone, dws.EventApprovalFinished} {
		for _, tt := range []struct{ payload, detail string }{
			{``, "payload is missing"},
			{`,"payload":null`, "payload is missing"},
			{`,"payload":{}`, "payload is empty"},
			{`,"payload":[]`, "cannot unmarshal array"},
			{`,"payload":"invalid"`, "payload is missing"},
			{`,"payload":{"event_time":1}`, "payload body is missing"},
			{`,"payload":{"body":null,"event_time":1}`, "payload body is missing"},
			{`,"payload":{"body":{},"event_time":1}`, "payload body is empty"},
		} {
			wantMalformed(t, outer(key), fmt.Sprintf(`{"eventKey":%q%s}`, key, tt.payload), tt.detail)
		}
	}
}

func voipData() string {
	return `{
		"eventId":"voip-event","eventKey":"user_voip_call_receive_invite","occurredAtMs":1780630479124,"subId":"voip-data-sub",
		"payload":{
			"bizid":"VOIP_room-1_3559506650","event_time":1780630479123,"corpid":"ding-callee-corp","orgId":21001,
			"uid":3559506650,"filterSubId":"internal-filter",
			"body":{
				"callId":"call-1","callerUid":"0147333457361236773","callerCorpId":"ding-caller-corp",
				"calleeUid":"digital-3559506650","calleeCorpId":"ding-callee-corp","callType":"conference",
				"roomId":"room-1","roomCode":"sensitive-code","createTime":1780630479000
			}
		}
	}`
}

func TestTypedVoIPInviteNeverCarriesTheRoomCode(t *testing.T) {
	got := mustTyped(t, outer(dws.EventVoIPInvite), voipData())
	want := &VoIPInviteEvent{
		EventHeader: EventHeader{Type: dws.EventVoIPInvite, EventID: "voip-event", Timestamp: 1780630479124, SubscribeID: "outer-sub"},
		BizID:       "VOIP_room-1_3559506650", CorpID: "ding-callee-corp", OrgID: 21001, TargetUID: 3559506650,
		CallID: "call-1", CallerUID: "0147333457361236773", CallerCorpID: "ding-caller-corp",
		CalleeUID: "digital-3559506650", CalleeCorpID: "ding-callee-corp", CallType: "conference",
		RoomID: "room-1", CreateTime: 1780630479000, EventTime: 1780630479123,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Typed = %#v", got)
	}
	encoded := marshalled(t, got, "sensitive-code", "room_code", "roomCode", "filterSubId")
	if !strings.Contains(encoded, `"biz_id":"VOIP_room-1_3559506650"`) || !strings.Contains(encoded, `"caller_uid":"0147333457361236773"`) {
		t.Fatalf("JSON = %s", encoded)
	}

	// Providers that predate string ids send integers.
	legacy := strings.Replace(voipData(), `"callerUid":"0147333457361236773"`, `"callerUid":1000000001`, 1)
	legacy = strings.Replace(legacy, `"calleeUid":"digital-3559506650"`, `"calleeUid":3559506650`, 1)
	if got := mustTyped(t, outer(dws.EventVoIPInvite), legacy).(*VoIPInviteEvent); got.CallerUID != "1000000001" || got.CalleeUID != "3559506650" {
		t.Fatalf("legacy ids = %q/%q", got.CallerUID, got.CalleeUID)
	}

	for _, tt := range []struct{ payload, detail string }{
		{``, "payload is missing"},
		{`,"payload":{"bizid":"biz-1"}`, "payload body is missing"},
		{`,"payload":{"body":{"callId":"call-1","roomCode":"sensitive-code"}}`, "bizid is required"},
		{`,"payload":{"bizid":"biz-1","body":{"callerUid":{}}}`, "must be a string or legacy integer"},
	} {
		data := fmt.Sprintf(`{"eventKey":%q%s}`, dws.EventVoIPInvite, tt.payload)
		wantMalformed(t, outer(dws.EventVoIPInvite), data, tt.detail)
		if _, err := decodeFixture(t, outer(dws.EventVoIPInvite), data).Typed(); strings.Contains(err.Error(), "sensitive-code") {
			t.Fatalf("error leaks the room code: %v", err)
		}
	}
}

func todoData(key string) string {
	body := map[string]any{
		"taskId": "123456", "subject": "待办标题", "creatorId": "creator-staff-id",
		"executorIds": []string{"executor-1", "executor-2"}, "participantIds": []string{"participant-1"},
		"priority": int64(10), "statusStage": int64(0),
		"planStartDate": int64(1780630479000), "planFinishDate": int64(1780630480000),
		"startDate": int64(1780630479000), "finishDate": int64(1780630480000),
		"description": "任务描述", "source": "TODO", "sourceId": "source_xxx", "bizTag": "tag_xxx",
		"parentId": nil, "isMultiExecutor": false, "sceneType": "PERSONAL", "createTime": int64(1780630479000),
	}
	switch key {
	case dws.EventTodoUpdated:
		body["subject"], body["priority"], body["statusStage"], body["oldStatusStage"] = "更新后的标题", int64(20), int64(1), int64(0)
		body["finishDate"], body["updateTime"] = nil, int64(1780630480000)
	case dws.EventTodoDeleted:
		body = map[string]any{"taskId": "123456", "subject": "被删除的待办标题", "creatorId": "creator-staff-id",
			"createTime": int64(1780630479000), "deleteTime": int64(1780630480000)}
	}
	encoded, _ := json.Marshal(map[string]any{
		"eventId": "todo-event", "eventKey": key, "occurredAtMs": int64(1780630480123), "subId": "todo-data-sub",
		"payload": map[string]any{"uid": 100001, "clientId": "internal-client", "filterSubId": "internal-filter", "body": body},
	})
	return string(encoded)
}

func TestTypedTodo(t *testing.T) {
	created := mustTyped(t, outer(dws.EventTodoCreated), todoData(dws.EventTodoCreated)).(*TodoCreatedEvent)
	if created.Type != dws.EventTodoCreated || created.EventID != "todo-event" || created.SubscribeID != "outer-sub" ||
		created.TaskID != "123456" || created.Subject != "待办标题" || created.CreatorID != "creator-staff-id" ||
		len(created.ExecutorIDs) != 2 || len(created.ParticipantIDs) != 1 || created.Priority != 10 ||
		created.PlanStartDate == nil || *created.PlanStartDate != 1780630479000 || created.FinishDate == nil ||
		created.SourceID != "source_xxx" || created.SceneType != "PERSONAL" || created.CreateTime != 1780630479000 || created.ParentID != nil {
		t.Fatalf("created = %#v", created)
	}
	marshalled(t, created, `"old_status_stage"`, `"update_time"`, `"parent_id"`)

	updated := mustTyped(t, outer(dws.EventTodoUpdated), todoData(dws.EventTodoUpdated)).(*TodoUpdatedEvent)
	if updated.Subject != "更新后的标题" || updated.Priority != 20 || updated.StatusStage != 1 || updated.OldStatusStage != 0 ||
		updated.UpdateTime != 1780630480000 || updated.FinishDate != nil {
		t.Fatalf("updated = %#v", updated)
	}
	if encoded := marshalled(t, updated, `"finish_date"`, `"parent_id"`); !strings.Contains(encoded, `"old_status_stage":0`) {
		t.Fatalf("updated JSON = %s", encoded)
	}

	deleted := mustTyped(t, outer(dws.EventTodoDeleted), todoData(dws.EventTodoDeleted))
	want := &TodoDeletedEvent{
		EventHeader: EventHeader{Type: dws.EventTodoDeleted, EventID: "todo-event", Timestamp: 1780630480123, SubscribeID: "outer-sub"},
		TaskID:      "123456", Subject: "被删除的待办标题", CreatorID: "creator-staff-id", CreateTime: 1780630479000, DeleteTime: 1780630480000,
	}
	if !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted = %#v", deleted)
	}

	wantMalformed(t, outer(dws.EventTodoCreated),
		`{"eventKey":"user_todo_task_create","payload":{"body":{"subject":"missing id"}}}`, "taskId is required")
}

func TestTypedCardActionKeepsTheReviewedCallbackShape(t *testing.T) {
	got := mustTyped(t, map[string]any{"eventId": "outer-event", "eventType": dws.EventCardAction, "SUB_ID": "sub-card-example"}, `{
		"eventId":"card-event-example","eventKey":"user_card_action_triggered","occurredAtMs":1788441872239,"subId":"inner-sub-example",
		"payload":{
			"body":{
				"actionData":{"context":{
					"answers":{"q0":{"custom":"","selected":["o1"]},"q2":{"selected":[]}},
					"createUid":"user-create-example","orgId":"org-example","outcome":"answered",
					"questions":[
						{"allowCustom":true,"header":"会议标题","id":"q0","options":[{"description":"项目相关讨论会议","id":"o1","label":"项目讨论"}],
						 "prompt":"请问会议的标题是什么？","selection":"single"},
						{"allowCustom":false,"header":"参会人","id":"q2","inputKind":"person","options":[],
						 "prompt":"需要邀请哪些参会人？","selection":"multiple"}
					],
					"sourceProjectionVersion":"dingtalk-coding-surface-v1","sourceTurnId":"turn-example","futureContext":{"kept":true}
				}},
				"bizInfoDTO":{"appKey":"app-example","bizId":"card-example"},
				"context":{"answers":"{\"q0\":{\"custom\":\"\",\"selected\":[\"o1\"]}}","createUid":"user-create-example",
					"orgId":"org-example","outcome":"answered","questions":"[{\"id\":\"q0\"}]"},
				"conversationContextDTO":{"cid":"conversation-example"},
				"extension":{"spaceModel":"{\"spaces\":{}}","futureExtension":"opaque"},
				"operatorDTO":{"operatorUserAgent":"TestClient/1.0","uid":10001},
				"spaceId":"space-example","spaceType":"im_single","triggerTimestamp":1788441872151,"futureBody":{"kept":true}
			},
			"event_time":1788441872152,
			"futurePayload":"kept"
		}
	}`).(*CardActionEvent)
	if got.EventHeader != (EventHeader{Type: dws.EventCardAction, EventID: "card-event-example", Timestamp: 1788441872239, SubscribeID: "sub-card-example"}) {
		t.Fatalf("header = %#v", got.EventHeader)
	}
	if got.Payload["event_time"] != json.Number("1788441872152") || got.Payload["futurePayload"] != "kept" {
		t.Fatalf("payload = %#v", got.Payload)
	}
	body := got.Payload["body"].(map[string]any)
	context := body["actionData"].(map[string]any)["context"].(map[string]any)
	answers := context["answers"].(map[string]any)
	question := context["questions"].([]any)[1].(map[string]any)
	operator := body["operatorDTO"].(map[string]any)
	if body["triggerTimestamp"] != json.Number("1788441872151") || !reflect.DeepEqual(body["futureBody"], map[string]any{"kept": true}) ||
		context["createUid"] != "user-create-example" || !reflect.DeepEqual(context["futureContext"], map[string]any{"kept": true}) ||
		!reflect.DeepEqual(answers["q0"].(map[string]any)["selected"], []any{"o1"}) ||
		!reflect.DeepEqual(answers["q2"].(map[string]any)["selected"], []any{}) ||
		question["inputKind"] != "person" || question["allowCustom"] != false ||
		body["context"].(map[string]any)["questions"] != `[{"id":"q0"}]` ||
		operator["uid"] != json.Number("10001") || body["extension"].(map[string]any)["futureExtension"] != "opaque" {
		t.Fatalf("body = %#v", body)
	}
	var flat map[string]json.RawMessage
	if err := json.Unmarshal([]byte(marshalled(t, got)), &flat); err != nil || len(flat) != 5 {
		t.Fatalf("JSON top level = %v, %v; want type, event_id, timestamp, subscribe_id, payload", flat, err)
	}
}

func TestTypedCardActionPreservesUnknownBusinessPayload(t *testing.T) {
	got := mustTyped(t, outer(dws.EventCardAction), `{
		"eventId":"card-event","eventKey":"user_card_action_triggered","occurredAtMs":1788200000123,"subId":"inner-sub",
		"payload":{
			"callbackType":"future_callback_type","futureField":{"nested":true},"futureLargeId":9007199254740993,
			"uid":"transport-user","clientId":"transport-client","body":{"uid":"business-user","unknown":42}
		}
	}`).(*CardActionEvent)
	if got.Payload["callbackType"] != "future_callback_type" || got.Payload["futureLargeId"] != json.Number("9007199254740993") ||
		!reflect.DeepEqual(got.Payload["futureField"], map[string]any{"nested": true}) {
		t.Fatalf("payload = %#v", got.Payload)
	}
	if _, ok := got.Payload["uid"]; ok {
		t.Fatalf("payload kept the transport uid: %#v", got.Payload)
	}
	if _, ok := got.Payload["clientId"]; ok {
		t.Fatalf("payload kept the transport clientId: %#v", got.Payload)
	}
	if body := got.Payload["body"].(map[string]any); body["uid"] != "business-user" || body["unknown"] != json.Number("42") {
		t.Fatalf("body = %#v", body)
	}
	if encoded := marshalled(t, got); !strings.Contains(encoded, `"futureLargeId":9007199254740993`) {
		t.Fatalf("large integer changed: %s", encoded)
	}

	wantMalformed(t, outer(dws.EventCardAction), `{"eventId":"inner-event","eventKey":"user_card_action_triggered"}`, "payload is missing")
}

func TestTypedRejectsEmptyPayloads(t *testing.T) {
	keys := []string{dws.EventIMAt, dws.EventIMSingleChat, dws.EventIMGroup, dws.EventIMFromUser,
		dws.EventIMAllSingleChats, dws.EventIMAllGroups, dws.EventIMReadSingleChat, dws.EventIMReadGroup,
		dws.EventIMRecallSingle, dws.EventIMRecallGroup, dws.EventIMReactionSingle, dws.EventIMReactionGroup,
		dws.EventGroupMemberAdded, dws.EventGroupMemberExited, dws.EventVoIPInvite,
		dws.EventTodoCreated, dws.EventTodoUpdated, dws.EventTodoDeleted}
	payloads := []string{``, `,"payload":null`, `,"payload":{}`, `,"payload":{"event_time":1}`, `,"payload":{"body":null}`, `,"payload":{"body":{}}`}
	for _, key := range keys {
		for _, payload := range payloads {
			wantMalformed(t, outer(key), fmt.Sprintf(`{"eventKey":%q%s}`, key, payload), "payload")
		}
	}
	// A message body field of the wrong type does not fit either.
	wantMalformed(t, outer(dws.EventIMAt), `{"payload":{"body":{"content":{"rich":true}}}}`, "cannot unmarshal")
}

func TestTypedWithoutATypedForm(t *testing.T) {
	malformed := frameEvent(frame{Type: "EVENT", Data: "not json"})
	if !malformed.Malformed {
		t.Fatalf("fixture is not malformed: %#v", malformed)
	}
	unknown := decodeFixture(t, outer("user_future_event"), messageData("user_future_event"))
	for _, ev := range []Event{malformed, unknown, {Key: dws.EventIMAt, Malformed: true, Data: json.RawMessage(messageData(dws.EventIMAt))}} {
		if v, err := ev.Typed(); v != nil || !errors.Is(err, ErrNoTypedForm) || errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("Typed(%s) = %#v, %v; want ErrNoTypedForm", ev.Key, v, err)
		}
	}
	// Frame data that is not JSON is a malformed frame: recorded with its
	// key and text, never typed.
	if _, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": dws.EventIMAt}, Data: "not json"}); !errors.Is(err, errMalformedEvent) {
		t.Fatalf("decodeEvent(not json) = %v", err)
	}
	bad := frameEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": dws.EventIMAt}, Data: "not json"})
	if !bad.Malformed || bad.Key != dws.EventIMAt || string(bad.Data) != `"not json"` {
		t.Fatalf("frameEvent(not json) = %#v", bad)
	}
	if _, err := bad.Typed(); !errors.Is(err, ErrNoTypedForm) {
		t.Fatalf("Typed(malformed) = %v", err)
	}
}

func TestTypedHeaderFallsBackToTheEvent(t *testing.T) {
	// Without eventId and occurredAtMs in the data, the Event's own id and
	// OccurredAt stand in.
	ev := decodeFixture(t, outer(dws.EventIMSingleChat), `{"payload":{"body":{"content":"hello"}}}`)
	ev.OccurredAt = time.UnixMilli(123)
	got := mustTypedEvent(t, ev).(*MessageEvent)
	if got.EventHeader != (EventHeader{Type: dws.EventIMSingleChat, EventID: "outer-event", Timestamp: 123, SubscribeID: "outer-sub"}) ||
		got.Content != "hello" {
		t.Fatalf("Typed = %#v", got)
	}

	// JSON-encoded data, payload and body decode as decodeEvent reads them.
	wrapped, _ := json.Marshal(messageData(dws.EventIMSingleChat))
	if got := mustTyped(t, nil, string(wrapped)).(*MessageEvent); got.Content != "在吗" || got.SubscribeID != "data-sub" || got.EventID != "data-event" {
		t.Fatalf("wrapped data = %#v", got)
	}
	body, _ := json.Marshal(`{"openMessageId":"m-1","content":"hi"}`)
	payload, _ := json.Marshal(`{"event_time":7,"body":` + string(body) + `}`)
	data := `{"eventKey":"user_im_message_receive_o2o","payload":` + string(payload) + `}`
	if got := mustTyped(t, nil, data).(*MessageEvent); got.MessageID != "m-1" || got.Content != "hi" || got.EventTime != 7 {
		t.Fatalf("wrapped payload = %#v", got)
	}
	card := `{"eventKey":"user_card_action_triggered","payload":{"body":` + string(body) + `}}`
	if got := mustTyped(t, nil, card).(*CardActionEvent); got.Payload["body"].(map[string]any)["content"] != "hi" {
		t.Fatalf("wrapped card body = %#v", got.Payload)
	}
}
