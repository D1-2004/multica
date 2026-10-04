package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/dws"
)

var (
	testWorkspace = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	testAgent     = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

func TestPublicIDs(t *testing.T) {
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	ask := Ref{Family: "ask", ID: id}
	if ask.PublicID() != "ask:"+id.String() || ask.SurfaceID() != "s-"+id.String() {
		t.Fatalf("ask ref = %s %s", ask.PublicID(), ask.SurfaceID())
	}
	for _, family := range []string{"ask", "show", "appr"} {
		got, ok := ParseRef(family + ":" + id.String())
		if !ok || got.Family != family || got.ID != id {
			t.Fatalf("parse %s = %#v %v", family, got, ok)
		}
	}
	for _, raw := range []string{"", "ask", "decision:" + id.String(), "ask:not-a-uuid", "show:" + uuid.Nil.String()} {
		if _, ok := ParseRef(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestProjectionShapes(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()

	approval, err := svc.Open(ctx, gw, baseRequest(KindApproval))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(approval.PublicID, "appr:") || approval.Status != StatusOpen {
		t.Fatalf("approval = %+v", approval)
	}
	created, components := decodeCard(t, gw.messages)
	if created.SurfaceID != "s-"+approval.ID.String() {
		t.Fatalf("surface = %s", created.SurfaceID)
	}
	clarification := created.DataModel["clarification"].(map[string]any)
	if clarification["sourceTurnId"] != approval.PublicID || clarification["sourceProjectionVersion"] != Version {
		t.Fatalf("clarification = %#v", clarification)
	}
	questions := clarification["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("questions = %#v", questions)
	}
	options := questions[0].(map[string]any)["options"].([]any)
	if len(options) != 2 || options[0].(map[string]any)["label"] != "同意" || options[1].(map[string]any)["label"] != "驳回" {
		t.Fatalf("options = %#v", options)
	}
	button := component(t, components, "submit")
	action := button["action"].(map[string]any)["event"].(map[string]any)
	context := action["context"].(map[string]any)
	for _, key := range []string{"sourceTurnId", "sourceProjectionVersion", "questions", "answers"} {
		path, _ := context[key].(map[string]any)
		if path["path"] == "" || !strings.HasPrefix(path["path"].(string), "/") {
			t.Fatalf("%s = %#v", key, context[key])
		}
	}
	if context["outcome"] != "answered" || action["name"] != submitEvent {
		t.Fatalf("action = %#v", action)
	}
	title := component(t, components, "title")
	if title["component"] != "Text" || title["bold"] != true || title["variant"] != "body" || title["colorToken"] != "common_level1_base_color" || title["text"] != "待审批" {
		t.Fatalf("title = %#v", title)
	}
	question := component(t, components, "question")
	if question["component"] != "Text" || question["variant"] != "body" || question["colorToken"] != "common_level1_base_color" || question["text"] != "要不要发？" {
		t.Fatalf("question = %#v", question)
	}
	if component(t, components, "skip")["variant"] != "borderless" || component(t, components, "skipLabel")["colorToken"] != "common_level2_base_color" {
		t.Fatalf("skip = %#v label %#v", component(t, components, "skip"), component(t, components, "skipLabel"))
	}

	gw.messages = nil
	if _, err := svc.Open(ctx, gw, baseRequest(KindPerson)); err != nil {
		t.Fatal(err)
	}
	_, components = decodeCard(t, gw.messages)
	person := component(t, components, "person")
	if person["component"] != "UserPicker" {
		t.Fatalf("person = %#v", person)
	}

	gw.messages = nil
	chartReq := baseRequest(KindChart)
	chartReq.Header = "本周处理量"
	chartReq.Chart = &Chart{Type: "line", Points: []ChartPoint{{X: "周一", Y: 4}, {X: "周二", Y: 9}}}
	chart, err := svc.Open(ctx, gw, chartReq)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(chart.PublicID, "show:") || chart.Status != StatusDelivered {
		t.Fatalf("chart = %+v", chart)
	}
	created, components = decodeCard(t, gw.messages)
	main := created.DataModel["charts"].(map[string]any)["main"].(map[string]any)
	if main["type"] != "lineChart" {
		t.Fatalf("chart model = %#v", main)
	}
	chartComponent := component(t, components, "chart")
	if chartComponent["data"].(map[string]any)["path"] != "/charts/main" {
		t.Fatalf("chart component = %#v", chartComponent)
	}
	if _, ok := chartComponent["data"].(map[string]any)["x"]; ok {
		t.Fatal("chart data was inlined")
	}
	if component(t, components, "title")["colorToken"] != "common_level1_base_color" {
		t.Fatalf("chart title = %#v", component(t, components, "title"))
	}

	gw.messages = nil
	if _, err := svc.Open(ctx, gw, baseRequest(KindConfirm)); err != nil {
		t.Fatal(err)
	}
	_, components = decodeCard(t, gw.messages)
	hint := component(t, components, "hint-o0")
	if hint["variant"] != "caption" || hint["colorToken"] != "common_level2_base_color" || hint["text"] != "就按这个发 · 现在发" {
		t.Fatalf("hint = %#v", hint)
	}
}

func TestAcceptClosesApproval(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	opened, err := svc.Open(ctx, gw, baseRequest(KindApproval))
	if err != nil {
		t.Fatal(err)
	}
	var finished []string
	finish := func(_ context.Context, bizID, surfaceID string) error {
		finished = append(finished, bizID+"|"+surfaceID)
		return errors.New("gateway finish failed")
	}
	line := cardLine(t, opened.PublicID, "ev-1", "answered", []string{"o0"}, 10001, opened.ConversationID, "dingexample")
	if err := svc.Accept(ctx, testActor(), line, finish); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || got.Result.Outcome != "approved" || len(got.Result.Selected) != 1 || got.Result.Selected[0] != "o0" || got.Result.Labels[0] != "同意" || got.OperatorUID != "10001" {
		t.Fatalf("result = %+v status %s", got.Result, got.Status)
	}
	if len(finished) != 1 || !strings.HasPrefix(finished[0], gw.biz+"|s-") {
		t.Fatalf("finish = %#v", finished)
	}
	if err := svc.Accept(ctx, testActor(), line, finish); err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 {
		t.Fatalf("duplicate event finished again: %#v", finished)
	}
	again := cardLine(t, opened.PublicID, "ev-2", "answered", []string{"o1"}, 10001, opened.ConversationID, "dingexample")
	if err := svc.Accept(ctx, testActor(), again, finish); err != nil {
		t.Fatal(err)
	}
	got, err = svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || got.EventID != "ev-1" {
		t.Fatalf("first answer lost: %+v", got)
	}
}

func TestAcceptIgnoresForeignAndUnknown(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	opened, err := svc.Open(ctx, gw, baseRequest(KindConfirm))
	if err != nil {
		t.Fatal(err)
	}
	foreign := cardLine(t, "coordinator-request", "ev-foreign", "answered", []string{"o1"}, "op", opened.ConversationID, "dingexample")
	foreign = rewriteTurn(t, foreign, "coordinator-request", "coordinator-user-decision-v1")
	if err := svc.Accept(ctx, testActor(), foreign, nil); err != nil {
		t.Fatal(err)
	}
	unknown := cardLine(t, "ask:"+uuid.NewString(), "ev-unknown", "answered", []string{"o0"}, 1, "cid", "dingexample")
	if err := svc.Accept(ctx, testActor(), unknown, nil); err != nil {
		t.Fatal(err)
	}
	other := testActor()
	other.UID = "someone-else"
	if err := svc.Accept(ctx, other, cardLine(t, opened.PublicID, "ev-fence", "answered", []string{"o0"}, 1, opened.ConversationID, "dingexample"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusOpen || got.EventID != "" {
		t.Fatalf("card changed: %+v", got)
	}
}

func TestChooseKeepsEverySelection(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	req := baseRequest(KindChoose)
	req.Options = []Option{{Label: "风险"}, {Label: "日期"}, {Label: "负责人"}}
	opened, err := svc.Open(ctx, gw, req)
	if err != nil {
		t.Fatal(err)
	}
	line := actionLine(t, opened.PublicID, "ev-multi", []string{"o0", "o2"}, "自定义")
	if err := svc.Accept(ctx, testActor(), line, nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAnswered || strings.Join(got.Result.Selected, ",") != "o0,o2" || strings.Join(got.Result.Labels, ",") != "风险,负责人" {
		t.Fatalf("choose = %+v", got.Result)
	}
}

func TestSendFailureRetriesWithTheSameID(t *testing.T) {
	svc, gw := testService()
	gw.err = errors.New("gateway down")
	ctx := context.Background()
	req := baseRequest(KindConfirm)
	req.IdempotencyKey = "same-question"
	failed, err := svc.Open(ctx, gw, req)
	if err == nil || failed.Status != StatusFailed || failed.PublicID == "" {
		t.Fatalf("failed open = %+v %v", failed, err)
	}
	gw.err = nil
	gw.sends = 0
	again, err := svc.Open(ctx, gw, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.PublicID != failed.PublicID || again.Status != StatusOpen || again.CardBizID != gw.biz || gw.sends != 1 {
		t.Fatalf("retry = %+v sends %d", again, gw.sends)
	}
	gw.sends = 0
	third, err := svc.Open(ctx, gw, req)
	if err != nil {
		t.Fatal(err)
	}
	if third.PublicID != again.PublicID || gw.sends != 0 {
		t.Fatalf("idempotent send happened again: %+v sends %d", third, gw.sends)
	}
}

func TestCorpFenceUsesTheSameIDSpace(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	opened, err := svc.Open(ctx, gw, baseRequest(KindApproval))
	if err != nil {
		t.Fatal(err)
	}
	// Numeric org on the row and a ding corp on the event are different spaces.
	if err := svc.Accept(ctx, testActor(), cardLine(t, opened.PublicID, "ev-corp", "answered", []string{"o1"}, 7, opened.ConversationID, "dingother"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusRejected || got.Result.Labels[0] != "驳回" {
		t.Fatalf("approval = %+v", got)
	}
}

func TestChartClickDoesNotReopen(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	req := baseRequest(KindChart)
	req.Header = "本周"
	req.Chart = &Chart{Points: []ChartPoint{{X: "周一", Y: 4}}}
	opened, err := svc.Open(ctx, gw, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Accept(ctx, testActor(), cardLine(t, opened.PublicID, "ev-chart", "answered", nil, 1, "", ""), nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusDelivered {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestClickKeepsTheSceneMessage(t *testing.T) {
	svc, gw := testService()
	ctx := context.Background()
	opened, err := svc.Open(ctx, gw, baseRequest(KindApproval))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Accept(ctx, testActor(), cardLine(t, opened.PublicID, "ev-scene", "answered", []string{"o0"}, 10001, opened.ConversationID, ""), nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, opened.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SceneID != "scene-1" || got.MessageID != opened.MessageID || got.MessageID == "" || got.ThreadID != "thread-1" || got.SourceRef != "receipt-1/msg-1" || got.Status != StatusApproved || got.Result.Outcome != "approved" {
		t.Fatalf("joined = %+v", got)
	}
	byMessage, err := svc.ByMessage(ctx, testAgent, opened.MessageID)
	if err != nil || byMessage.PublicID != opened.PublicID || byMessage.Result.Outcome != "approved" {
		t.Fatalf("by message = %+v %v", byMessage, err)
	}
	rows, err := svc.ForMessage(ctx, testAgent, "scene-1", opened.MessageID)
	if err != nil || len(rows) != 1 || rows[0].PublicID != opened.PublicID {
		t.Fatalf("for message = %+v %v", rows, err)
	}
	empty, err := svc.ForMessage(ctx, testAgent, "scene-1", "msg-other")
	if err != nil || len(empty) != 0 {
		t.Fatalf("other message = %+v %v", empty, err)
	}
	other, err := svc.ForMessage(ctx, testAgent, "scene-other", opened.MessageID)
	if err != nil || len(other) != 0 {
		t.Fatalf("other scene = %+v %v", other, err)
	}
	missing := baseRequest(KindConfirm)
	missing.SceneID = ""
	if _, err := svc.Open(ctx, gw, missing); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing scene = %v", err)
	}
}

func TestOpenStoresTheCardMessageIDFromTheSend(t *testing.T) {
	svc := newService(newMemStore())
	ctx := context.Background()
	gw := &scriptedGateway{receipts: []dws.A2UIReceipt{
		{BizID: "transformer_card_a", CardInstanceID: 1, MessageID: "msg-a", ConversationID: "cid-back"},
		{BizID: "transformer_card_b", CardInstanceID: 2, MessageID: "msg-a"},
		{BizID: "transformer_card_c", CardInstanceID: 3},
	}}
	first, err := svc.Open(ctx, gw, baseRequest(KindConfirm))
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageID != "msg-a" || first.ConversationID != "cid-back" || first.CardBizID != "transformer_card_a" || first.Status != StatusOpen {
		t.Fatalf("first = %+v", first)
	}
	if _, err := svc.Open(ctx, gw, baseRequest(KindConfirm)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate message = %v", err)
	}
	note := baseRequest(KindNote)
	note.Markdown = "记一笔"
	third, err := svc.Open(ctx, gw, note)
	if err != nil {
		t.Fatal(err)
	}
	if third.MessageID != "" || third.Status != StatusDelivered || third.CardBizID != "transformer_card_c" {
		t.Fatalf("unnumbered = %+v", third)
	}
	if _, err := svc.ByMessage(ctx, testAgent, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty message = %v", err)
	}
	if err := svc.Accept(ctx, testActor(), cardLine(t, first.PublicID, "ev-msg", "answered", []string{"o0"}, 10001, "cid-back", ""), nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ByMessage(ctx, testAgent, "msg-a")
	if err != nil || got.PublicID != first.PublicID || got.Result.Outcome != "answered" || got.ThreadID != "thread-1" || got.SourceRef != "receipt-1/msg-1" {
		t.Fatalf("reply = %+v %v", got, err)
	}
}

type scriptedGateway struct {
	receipts []dws.A2UIReceipt
	n        int
}

func (g *scriptedGateway) SendA2UI(context.Context, dws.A2UISend) (dws.A2UIReceipt, error) {
	if g.n >= len(g.receipts) {
		return dws.A2UIReceipt{}, errors.New("no receipt")
	}
	receipt := g.receipts[g.n]
	g.n++
	return receipt, nil
}

func (g *scriptedGateway) FinishA2UI(context.Context, string, string) error { return nil }

type fakeGateway struct {
	messages []string
	biz      string
	err      error
	sends    int
}

func (f *fakeGateway) SendA2UI(_ context.Context, req dws.A2UISend) (dws.A2UIReceipt, error) {
	f.sends++
	f.messages = req.Messages
	if f.err != nil {
		return dws.A2UIReceipt{}, f.err
	}
	if f.biz == "" {
		f.biz = "transformer_card_test"
	}
	return dws.A2UIReceipt{BizID: f.biz, CardInstanceID: 42, MessageID: fmt.Sprintf("card-msg-%d", f.sends)}, nil
}

func (f *fakeGateway) FinishA2UI(context.Context, string, string) error { return nil }

func testService() (*Service, *fakeGateway) {
	gw := &fakeGateway{biz: "transformer_card_test"}
	return newService(newMemStore()), gw
}

func testActor() Actor {
	return Actor{AgentID: testAgent, UID: "103262", OrgID: "439446171"}
}

func baseRequest(kind Kind) OpenRequest {
	req := OpenRequest{
		WorkspaceID: testWorkspace, AgentID: testAgent, SenderUID: "103262", SenderOrgID: "439446171",
		SceneID: "scene-1", ConversationID: "cid-1", ThreadID: "thread-1",
		SourceRef: "receipt-1/msg-1", Kind: kind, Question: "要不要发？",
	}
	if kind == KindConfirm || kind == KindChoose {
		req.Options = []Option{{Label: "就按这个发", Description: "现在发"}, {Label: "先放着"}}
	}
	return req
}

func cardLine(t *testing.T, publicID, eventID, outcome string, selected []string, operator any, cid, corp string) []byte {
	t.Helper()
	inner := map[string]any{
		"eventId": eventID, "eventKey": "user_card_action_triggered",
		"payload": map[string]any{
			"corpid": corp,
			"body": map[string]any{
				"actionData": map[string]any{"context": map[string]any{
					"outcome": outcome, "sourceTurnId": publicID, "sourceProjectionVersion": Version,
					"answers": map[string]any{"q0": map[string]any{"selected": selected, "custom": ""}},
				}},
				"conversationContextDTO": map[string]any{"cid": cid},
				"operatorDTO":            map[string]any{"uid": operator},
				"bizInfoDTO":             map[string]any{"bizId": "transformer_card_x"},
			},
		},
	}
	raw, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{
		"type": "event", "event_id": eventID, "event_corp_id": corp,
		"event_type": "user_card_action_triggered", "event_scope": "personal", "data": string(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func actionLine(t *testing.T, publicID, eventID string, selected []string, custom string) []byte {
	t.Helper()
	inner := map[string]any{
		"eventId": eventID, "eventKey": "user_card_action_triggered",
		"payload": map[string]any{
			"body": map[string]any{
				"a2uiEvent": map[string]any{"action": map[string]any{
					"name": submitEvent,
					"context": map[string]any{
						"outcome": "answered", "sourceTurnId": publicID, "sourceProjectionVersion": Version,
						"answers": map[string]any{"q0": map[string]any{"selected": selected, "custom": custom}},
					},
				}},
				"conversationContextDTO": map[string]any{"openConversationId": "cid-1"},
				"operatorDTO":            map[string]any{"openDingTalkId": "operator-open"},
			},
		},
	}
	raw, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{
		"type": "event", "event_id": eventID, "event_type": "user_card_action_triggered", "data": string(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func rewriteTurn(t *testing.T, line []byte, turn, version string) []byte {
	t.Helper()
	var envelope struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Data == "" {
		t.Fatal("line has no data")
	}
	updated := strings.ReplaceAll(envelope.Data, Version, version)
	var payload map[string]any
	if json.Unmarshal([]byte(updated), &payload) != nil {
		t.Fatal("inner")
	}
	body := payload["payload"].(map[string]any)["body"].(map[string]any)
	body["actionData"].(map[string]any)["context"].(map[string]any)["sourceTurnId"] = turn
	body["a2uiEvent"] = map[string]any{"action": map[string]any{
		"name": submitEvent,
		"context": map[string]any{
			"outcome": "answered", "sourceTurnId": turn, "sourceProjectionVersion": version,
			"answers": map[string]any{"q0": map[string]any{"selected": []string{"o1"}, "custom": ""}},
		},
	}}
	delete(body, "actionData")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var outer map[string]any
	if json.Unmarshal(line, &outer) != nil {
		t.Fatal("outer")
	}
	outer["data"] = string(raw)
	rewritten, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return rewritten
}

type createdSurface struct {
	SurfaceID string
	DataModel map[string]any
}

func decodeCard(t *testing.T, messages []string) (createdSurface, []any) {
	t.Helper()
	if len(messages) != 2 {
		t.Fatalf("messages = %d", len(messages))
	}
	var created struct {
		CreateSurface struct {
			SurfaceID string         `json:"surfaceId"`
			DataModel map[string]any `json:"dataModel"`
		} `json:"createSurface"`
	}
	var updated struct {
		UpdateComponents struct {
			Components []any `json:"components"`
		} `json:"updateComponents"`
	}
	if json.Unmarshal([]byte(messages[0]), &created) != nil || json.Unmarshal([]byte(messages[1]), &updated) != nil {
		t.Fatal("card json")
	}
	return createdSurface{SurfaceID: created.CreateSurface.SurfaceID, DataModel: created.CreateSurface.DataModel}, updated.UpdateComponents.Components
}

func component(t *testing.T, components []any, id string) map[string]any {
	t.Helper()
	for _, component := range components {
		item := component.(map[string]any)
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("missing %s", id)
	return nil
}
