package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCoordinatorWindowRestoresSameTextDifferentAuthorsAndReference(t *testing.T) {
	msg := channel.InboundMessage{MessageID: "m-b", Text: "同名：好\n同名：好", Source: channel.Source{ChatID: "cid-current"}}
	watermark := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	turn := inboundcoord.Turn{SenderName: "同名", PersonID: "uid-a", EvidenceID: "m-b", HistoryBefore: watermark}
	raw := []byte(`{"dispatch_event_data":{
		"conversation":{"openConversationId":"cid-current"},
		"sender":{"displayName":"同名","uid":"uid-a","staffId":"staff-a"},
		"messages":[
			{"openMsgId":"m-a","text":"好","senderDisplayName":"同名","senderUid":"uid-a","occurredAt":1788778801000},
			{"openMsgId":"m-b","text":"好","senderDisplayName":"同名","senderOpenDingTalkId":"open-b","occurredAt":1788778802000,
			 "referencedMessage":{"openMsgId":"question-b","senderUid":"agent-uid","text":"发给项目群吗？"}}
		]}}`)
	restoreCoordinatorWindow(&turn, raw, msg)
	if len(turn.Utterances) != 2 {
		t.Fatalf("two authors were flattened: %#v", turn.Utterances)
	}
	a, b := turn.Utterances[0], turn.Utterances[1]
	if a.Text != "好" || b.Text != "好" || a.SenderID != "uid-a" || b.SenderID != "open-b" || a.EvidenceID != "m-a" || b.EvidenceID != "m-b" {
		t.Fatalf("same text must retain distinct source identity: %#v", turn.Utterances)
	}
	if b.ReplyToEvidenceID != "question-b" || b.ReplyToSenderID != "agent-uid" || b.ReplyToContent != "发给项目群吗？" {
		t.Fatalf("reference evidence was lost: %#v", b)
	}
	if !turn.MessageTimestamp.Equal(time.UnixMilli(1788778802000)) || !turn.HistoryBefore.Equal(turn.MessageTimestamp) || !a.Timestamp.Equal(time.UnixMilli(1788778801000)) {
		t.Fatalf("source message times did not define the window: %#v", turn)
	}
}

func TestCoordinatorWindowRejectsStaleOrDifferentSceneBatch(t *testing.T) {
	msg := channel.InboundMessage{MessageID: "current", Text: "原生当前消息", Source: channel.Source{ChatID: "cid-current"}, ReplyTo: &channel.ReplyCtx{MessageID: "native-question"}}
	watermark := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for name, raw := range map[string]string{
		"other_scene":       `{"dispatch_event_data":{"conversation":{"openConversationId":"cid-other"},"messages":[{"openMsgId":"current","text":"别的群","senderUid":"other-person","occurredAt":1788778801000}]}}`,
		"old_batch":         `{"dispatch_event_data":{"conversation":{"openConversationId":"cid-current"},"messages":[{"openMsgId":"old-message","text":"旧任务","senderUid":"old-person","occurredAt":1788778801000}]}}`,
		"missing_scene":     `{"dispatch_event_data":{"messages":[{"openMsgId":"current","text":"未定位"}]}}`,
		"reaction_snapshot": `{"dispatch_event_data":{"conversation":{"openConversationId":"cid-current"},"messages":[{"openMsgId":"current","text":"被点赞的旧命令","reaction":{"action":"add"}}]}}`,
		"broken":            `{broken`,
	} {
		t.Run(name, func(t *testing.T) {
			turn := inboundcoord.Turn{SenderName: "当前人", PersonID: "current-person", EvidenceID: "current", HistoryBefore: watermark}
			restoreCoordinatorWindow(&turn, []byte(raw), msg)
			want := []inboundcoord.WindowUtterance{{Sender: "当前人", SenderID: "current-person", Text: "原生当前消息", EvidenceID: "current", ReplyToEvidenceID: "native-question"}}
			if !reflect.DeepEqual(turn.Utterances, want) || !turn.MessageTimestamp.IsZero() || !turn.HistoryBefore.Equal(watermark) {
				t.Fatalf("unrelated context replaced native evidence: %#v", turn)
			}
		})
	}
}

func TestCoordinatorWindowDoesNotInferIdentityOrTimeFromDisplayName(t *testing.T) {
	msg := channel.InboundMessage{MessageID: "m-b", Source: channel.Source{ChatID: "cid-current"}}
	watermark := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	turn := inboundcoord.Turn{HistoryBefore: watermark}
	restoreCoordinatorWindow(&turn, []byte(`{"dispatch_event_data":{
		"conversation":{"openConversationId":"cid-current"},"sender":{"displayName":"同名","uid":"top-person"},
		"messages":[{"openMsgId":"m-a","text":"第一条","senderDisplayName":"同名","senderUid":"uid-a"},
		{"openMsgId":"m-b","text":"第二条","senderDisplayName":"同名"}]}}`), msg)
	if len(turn.Utterances) != 2 || turn.Utterances[1].SenderID != "" || !turn.Utterances[1].Timestamp.IsZero() || !turn.MessageTimestamp.IsZero() || !turn.HistoryBefore.Equal(watermark) {
		t.Fatalf("unknown identity/time must remain unknown: %#v", turn)
	}
}

func TestCoordinatorWindowUsesExplicitSingleSenderEnvelope(t *testing.T) {
	msg := channel.InboundMessage{MessageID: "m-b", Source: channel.Source{ChatID: "cid-current"}}
	batch, ok := currentCoordinatorDispatchBatch([]byte(`{"dispatch_event_data":{
		"conversation":{"openConversationId":"cid-current"},"sender":{"displayName":"甲","uid":"uid-a"},
		"messages":[{"openMsgId":"m-a","text":"先查资料"},{"openMsgId":"m-b","text":"再整理下"}]}}`), msg)
	if !ok || len(batch) != 2 || batch[0].SenderID != "uid-a" || batch[1].SenderID != "uid-a" || batch[0].Sender != "甲" {
		t.Fatalf("the authoritative single-sender envelope was lost: %#v", batch)
	}
}

func TestCoordinatorItemContextKeepsSelectedSourcesAndReplacesWholeActor(t *testing.T) {
	raw := []byte(`{"completion_callback":{"url":"/callback","target":"origin"},"external_identity":{"dws":{"uid":"agent-uid","orgId":"org"}},
		"dispatch_event_data":{"conversation":{"openConversationId":"cid-current"},
		"sender":{"displayName":"同名","uid":"uid-a","staffId":"staff-a","openDingTalkId":"open-a"},
		"messages":[{"openMsgId":"m-a","text":"相同正文","senderDisplayName":"同名","senderUid":"uid-a"},
		{"openMsgId":"m-b","text":"相同正文","senderDisplayName":"同名","senderOpenDingTalkId":"open-b","occurredAt":1788778802000,"referencedMessage":{"openMsgId":"question-b","text":"再发一次？"}},
		{"openMsgId":"m-c","text":"补充别改正文","senderDisplayName":"丙","senderUid":"uid-c"}]}}`)
	msg := channel.InboundMessage{MessageID: "m-c", Source: channel.Source{ChatID: "cid-current"}}
	got, err := coordinatorItemTaskContext(raw, msg, inboundcoord.WindowItem{SourceRefs: []string{"u2", "u3"}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Callback json.RawMessage `json:"completion_callback"`
		Identity json.RawMessage `json:"external_identity"`
		Data     struct {
			Sender   coordinatorDispatchSender    `json:"sender"`
			Messages []coordinatorDispatchMessage `json:"messages"`
		} `json:"dispatch_event_data"`
	}
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Sender.UID != "" || envelope.Data.Sender.StaffID != "" || envelope.Data.Sender.OpenDingTalkID != "open-b" {
		t.Fatalf("selected actor retained another person's identity fields: %#v", envelope.Data.Sender)
	}
	if len(envelope.Data.Messages) != 2 || envelope.Data.Messages[0].OpenMsgID != "m-b" || envelope.Data.Messages[1].OpenMsgID != "m-c" || envelope.Data.Messages[0].ReferencedMessage.OpenMsgID != "question-b" {
		t.Fatalf("selected evidence was collapsed or rewritten: %#v", envelope.Data.Messages)
	}
	var original map[string]json.RawMessage
	_ = json.Unmarshal(raw, &original)
	if !reflect.DeepEqual(envelope.Callback, original["completion_callback"]) || !reflect.DeepEqual(envelope.Identity, original["external_identity"]) {
		t.Fatal("source scoping changed trusted callback or execution identity")
	}
	if _, err := coordinatorItemTaskContext(raw, msg, inboundcoord.WindowItem{SourceRefs: []string{"u4"}}); err == nil {
		t.Fatal("an unknown source must not receive the whole batch")
	}
	reordered, err := coordinatorItemTaskContext(raw, msg, inboundcoord.WindowItem{SourceRefs: []string{"u3", "u2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(reordered, &envelope); err != nil || envelope.Data.Sender.UID != "uid-c" {
		t.Fatalf("the primary actor must match the plan's first source: sender=%#v err=%v", envelope.Data.Sender, err)
	}
}

func TestCoordinatorWindowPersistsSelectedActorPerTask(t *testing.T) {
	f := newChannelPlanFixture(t)
	f.msg.MessageID = "m-b"
	f.msg.Text = "两个人都说好"
	raw, err := json.Marshal(map[string]any{
		"completion_callback": map[string]any{"url": "https://callback.invalid/update", "target": "trusted-origin"},
		"dispatch_event_data": map[string]any{
			"conversation": map[string]any{"openConversationId": f.msg.Source.ChatID},
			"sender":       map[string]any{"displayName": "同名", "uid": "uid-a", "staffId": "staff-a"},
			"messages": []map[string]any{
				{"openMsgId": "m-a", "text": "好", "senderDisplayName": "同名", "senderUid": "uid-a", "senderStaffId": "staff-a"},
				{"openMsgId": "m-b", "text": "好", "senderDisplayName": "同名", "senderOpenDingTalkId": "open-b", "occurredAt": 1788778802000, "referencedMessage": map[string]any{"openMsgId": "question-b", "text": "按这版发？", "senderUid": "agent-uid"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	one, two := newChannelWorkItem("item-1"), newChannelWorkItem("item-2")
	one.SourceRefs, two.SourceRefs = []string{"u1"}, []string{"u2"}
	plan := newChannelWorkPlan(one, two)
	if err := f.router.materializeChannelCoordinatorPlan(context.Background(), f.inst, f.identity, "dingtalk_chat", f.msg, raw, &plan); err != nil {
		t.Fatal(err)
	}
	for i, result := range plan.IssueResults {
		task, err := db.New(f.pool).GetAgentTask(context.Background(), util.MustParseUUID(result.TaskID))
		if err != nil {
			t.Fatal(err)
		}
		var stored struct {
			Data struct {
				Sender   coordinatorDispatchSender    `json:"sender"`
				Messages []coordinatorDispatchMessage `json:"messages"`
			} `json:"dispatch_event_data"`
			Callback json.RawMessage `json:"completion_callback"`
			Wrapup   json.RawMessage `json:"coordinator_wrapup_callback"`
		}
		if err := json.Unmarshal(task.Context, &stored); err != nil {
			t.Fatal(err)
		}
		if len(stored.Data.Messages) != 1 || len(stored.Callback) != 0 || len(stored.Wrapup) == 0 {
			t.Fatalf("task scope or callback isolation was lost: task=%s", result.TaskID)
		}
		if i == 0 && (stored.Data.Sender.UID != "uid-a" || stored.Data.Messages[0].OpenMsgID != "m-a") {
			t.Fatalf("first task source changed: %#v", stored.Data)
		}
		if i == 1 && (stored.Data.Sender.UID != "" || stored.Data.Sender.StaffID != "" || stored.Data.Sender.OpenDingTalkID != "open-b" || stored.Data.Messages[0].OpenMsgID != "m-b" || stored.Data.Messages[0].ReferencedMessage.SenderUID != "agent-uid") {
			t.Fatalf("second task borrowed first speaker's identity or lost its quote: %#v", stored.Data)
		}
	}
	f.counts(t, 2, 0, 2)
}
