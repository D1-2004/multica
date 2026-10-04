package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestToolCatalogIsParameterOnly(t *testing.T) {
	tools := Tools()
	if len(tools) != 4 {
		t.Fatalf("tools = %d", len(tools))
	}
	wantEffect := map[string]bool{ToolAsk: true, ToolShow: true, ToolApprove: true, ToolRead: false}
	for _, tool := range tools {
		effect, ok := wantEffect[tool.Name]
		if !ok || tool.Effect != effect {
			t.Fatalf("tool %s effect %v", tool.Name, tool.Effect)
		}
		if tool.InputSchema["type"] != "object" || tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s schema = %#v", tool.Name, tool.InputSchema)
		}
		props := tool.InputSchema["properties"].(map[string]any)
		for _, banned := range []string{"workspace_id", "agent_id", "sender_uid", "conversation_id", "idempotency_key", "scene_id", "message_id", "thread_id", "source_ref"} {
			if _, exists := props[banned]; exists {
				t.Fatalf("%s exposes %s", tool.Name, banned)
			}
		}
		def := tool.Definition()
		schema, _ := def["inputSchema"].(map[string]any)
		if def["name"] != tool.Name || def["description"] == "" || schema["additionalProperties"] != false {
			t.Fatalf("definition %#v", def)
		}
		delete(wantEffect, tool.Name)
	}
	if len(wantEffect) != 0 {
		t.Fatalf("missing %#v", wantEffect)
	}
	again := Tools()
	again[0].InputSchema["additionalProperties"] = true
	if Tools()[0].InputSchema["additionalProperties"] != false {
		t.Fatal("catalog schema is shared")
	}
}

func TestToolAskChooseAndPerson(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	caller := testCaller()

	confirm, err := svc.Call(ctx, gw, caller, "call-confirm", ToolAsk, json.RawMessage(`{
		"question":"要不要发？",
		"options":[{"label":"就按这个发","description":"现在发"},{"label":"先放着"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(confirm.PublicID, "ask:") || confirm.Kind != string(KindConfirm) || confirm.Status != string(StatusOpen) {
		t.Fatalf("confirm = %+v", confirm)
	}
	if !strings.Contains(confirm.Text(), "public_id="+confirm.PublicID) {
		t.Fatal(confirm.Text())
	}
	again, err := svc.Call(ctx, gw, caller, "call-confirm", ToolAsk, json.RawMessage(`{"question":"要不要发？","options":[{"label":"就按这个发"},{"label":"先放着"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if again.PublicID != confirm.PublicID || gw.sends != 1 {
		t.Fatalf("retry id %s sends %d", again.PublicID, gw.sends)
	}

	choose, err := svc.Call(ctx, gw, caller, "call-choose", ToolAsk, json.RawMessage(`{
		"question":"圈哪些？","multiple":true,
		"options":[{"label":"本周"},{"label":"下周"}]
	}`))
	if err != nil || choose.Kind != string(KindChoose) || choose.PublicID == confirm.PublicID {
		t.Fatalf("choose = %+v %v", choose, err)
	}

	person, err := svc.Call(ctx, gw, caller, "call-person", ToolAsk, json.RawMessage(`{"question":"发给谁？","pick_person":true}`))
	if err != nil || person.Kind != string(KindPerson) {
		t.Fatalf("person = %+v %v", person, err)
	}
	if _, err := svc.Call(ctx, gw, caller, "call-bad-person", ToolAsk, json.RawMessage(`{"question":"发给谁？","pick_person":true,"options":[{"label":"冬翔"}]}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("person options = %v", err)
	}
}

func TestToolShowApproveAndRead(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	caller := testCaller()

	chart, err := svc.Call(ctx, gw, caller, "call-chart", ToolShow, json.RawMessage(`{
		"title":"本周",
		"chart":{"type":"bar","points":[{"x":"周一","y":4},{"x":"周二","y":9}]}
	}`))
	if err != nil || !strings.HasPrefix(chart.PublicID, "show:") || chart.Kind != string(KindChart) || chart.Status != string(StatusDelivered) {
		t.Fatalf("chart = %+v %v", chart, err)
	}
	if _, err := svc.Call(ctx, gw, caller, "call-both", ToolShow, json.RawMessage(`{"markdown":"结论","chart":{"points":[{"x":"周一","y":1}]}}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("both = %v", err)
	}
	note, err := svc.Call(ctx, gw, caller, "call-note", ToolShow, json.RawMessage(`{"markdown":"先按这个结论"}`))
	if err != nil || note.Kind != string(KindNote) || note.Status != string(StatusDelivered) {
		t.Fatalf("note = %+v %v", note, err)
	}

	approval, err := svc.Call(ctx, gw, caller, "call-appr", ToolApprove, json.RawMessage(`{}`))
	if err != nil || !strings.HasPrefix(approval.PublicID, "appr:") || approval.Status != string(StatusOpen) {
		t.Fatalf("approval = %+v %v", approval, err)
	}
	created, _ := decodeCard(t, gw.messages)
	questions := created.DataModel["clarification"].(map[string]any)["questions"].([]any)
	options := questions[0].(map[string]any)["options"].([]any)
	if options[0].(map[string]any)["label"] != "同意" || options[1].(map[string]any)["label"] != "驳回" {
		t.Fatalf("options = %#v", options)
	}

	pending, err := svc.Call(ctx, nil, caller, "", ToolRead, json.RawMessage(`{"public_id":"`+approval.PublicID+`"}`))
	if err != nil || pending.Status != string(StatusOpen) || pending.Outcome != "" {
		t.Fatalf("pending = %+v %v", pending, err)
	}
	if err := svc.Accept(ctx, testActor(), cardLine(t, approval.PublicID, "ev-appr", "answered", []string{"o0"}, 10001, "cid-1", ""), nil); err != nil {
		t.Fatal(err)
	}
	answered, err := svc.Call(ctx, nil, caller, "", ToolRead, json.RawMessage(`{"public_id":"`+approval.PublicID+`"}`))
	if err != nil || answered.Status != string(StatusApproved) || answered.Outcome != "approved" || answered.SceneID != "scene-1" || answered.MessageID == "" || answered.MessageID != approval.MessageID || answered.ThreadID != "thread-1" || answered.SourceRef != "receipt-1/msg-1" || len(answered.Selected) != 1 || answered.Selected[0] != "o0" {
		t.Fatalf("answered = %+v %v", answered, err)
	}
	if !strings.Contains(answered.Text(), "outcome=approved") {
		t.Fatal(answered.Text())
	}

	other := caller
	other.AgentID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	if _, err := svc.Call(ctx, nil, other, "", ToolRead, json.RawMessage(`{"public_id":"`+approval.PublicID+`"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other agent = %v", err)
	}
}

func TestToolRejectsHostFieldsAndUnknownCalls(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	caller := testCaller()
	for _, raw := range []string{
		`{"question":"要不要发？","workspace_id":"11111111-1111-4111-8111-111111111111","options":[{"label":"好"},{"label":"不"}]}`,
		`{"question":"要不要发？","conversation_id":"cid-x","options":[{"label":"好"},{"label":"不"}]}`,
	} {
		if _, err := svc.Call(ctx, gw, caller, "call-x", ToolAsk, json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s -> %v", raw, err)
		}
	}
	if _, err := svc.Call(ctx, gw, caller, "", ToolAsk, json.RawMessage(`{"question":"要不要发？","options":[{"label":"好"},{"label":"不"}]}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing call id = %v", err)
	}
	if _, err := svc.Call(ctx, gw, caller, "call-nope", "a2ui_send", json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown = %v", err)
	}
	if gw.sends != 0 {
		t.Fatalf("sends = %d", gw.sends)
	}
}

func testCaller() Caller {
	return Caller{
		WorkspaceID: testWorkspace, AgentID: testAgent,
		SenderUID: "103262", SenderOrgID: "439446171",
		SceneID: "scene-1", ConversationID: "cid-1",
		ThreadID: "thread-1", SourceRef: "receipt-1/msg-1",
	}
}
