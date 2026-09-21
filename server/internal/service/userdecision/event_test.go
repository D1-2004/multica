package userdecision

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixtureEvent(legacy bool) []byte {
	action := map[string]any{"name": "runtime.clarification.submit", "context": map[string]any{"outcome": "answered", "sourceTurnId": "req", "sourceProjectionVersion": Version, "openDingTalkId": "sender-not-operator", "answers": map[string]any{"q0": map[string]any{"selected": []string{"o1"}, "custom": "hello"}}}}
	body := map[string]any{"bizInfoDTO": map[string]string{"bizId": "card"}, "conversationContextDTO": map[string]string{"openConversationId": "cid"}, "operatorDTO": map[string]string{"openDingTalkId": "initiator"}, "triggerTimestamp": 1789962459946}
	if legacy {
		body["actionData"] = action
	} else {
		body["a2uiEvent"] = map[string]any{"action": action}
	}
	b, _ := json.Marshal(map[string]any{"eventId": "evt", "eventKey": "user_card_action_triggered", "payload": map[string]any{"corpid": "corp", "body": body}})
	return b
}
func TestParseEventProtocolsAndStreamEnvelope(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		raw := fixtureEvent(legacy)
		for _, wrapped := range []bool{false, true} {
			input := raw
			if wrapped {
				input, _ = json.Marshal(map[string]any{"type": "event", "data": string(raw)})
			}
			e, err := ParseEvent(input)
			if err != nil {
				t.Fatal(err)
			}
			if e.OperatorID != "initiator" || e.Custom != "hello" || e.Selected[0] != "o1" || e.CardID != "card" {
				t.Fatalf("incorrect event: %+v", e)
			}
		}
	}
}
func TestOperatorCannotBeReplacedByContextIdentity(t *testing.T) {
	raw := strings.Replace(string(fixtureEvent(false)), `"operatorDTO":{"openDingTalkId":"initiator"}`, `"operatorDTO":{}`, 1)
	if _, err := ParseEvent([]byte(raw)); err == nil {
		t.Fatal("accepted untrusted context operator")
	}
}
func TestInvalidAnswers(t *testing.T) {
	for _, replacement := range []string{`"selected":[],"custom":" "`, `"selected":[""],"custom":""`, `"selected":["o1","o2"],"custom":""`} {
		raw := strings.Replace(string(fixtureEvent(false)), `"custom":"hello","selected":["o1"]`, replacement, 1)
		if _, err := ParseEvent([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", replacement)
		}
	}
	raw := fixtureEvent(false)
	for range 6 {
		raw, _ = json.Marshal(map[string]any{"data": json.RawMessage(raw)})
	}
	if _, err := ParseEvent(raw); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}
func TestIdentityAndExpiration(t *testing.T) {
	e, err := ParseEvent(fixtureEvent(false))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(time.Hour)
	i := Identity{"pre", "corp", "cid", "card", "initiator", "req", Version}
	if err := i.Validate(e, "pre", now, expires); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Event){func(e *Event) { e.OperatorID = "other" }, func(e *Event) { e.CorpID = "other" }, func(e *Event) { e.CardID = "other" }, func(e *Event) { e.ConversationID = "other" }, func(e *Event) { e.Version = "other" }, func(e *Event) { e.RequestID = "other" }} {
		changed := e
		mutate(&changed)
		if err := i.Validate(changed, "pre", now, expires); err == nil {
			t.Fatal("accepted mismatched scope")
		}
	}
	if i.Validate(e, "online", now, expires) == nil || i.Validate(e, "pre", expires, expires) == nil || i.Validate(e, "pre", now, time.Time{}) == nil {
		t.Fatal("accepted wrong environment or expired decision")
	}
}
func TestCardDoesNotDisclosePlansOrPreselect(t *testing.T) {
	p := Proposal{Question: "怎么处理？", Options: []Option{{ID: "o1", Label: "新建工作", Kind: "start_work", Plan: json.RawMessage(`{"secret":"private-task-id"}`)}, {ID: "o2", Label: "直接回复：好的", Kind: "reply", Plan: json.RawMessage(`{}`)}}, RecommendedID: "o1", RawOutput: "private-model-analysis"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	card := strings.Join(Card("id", p), "\n")
	if strings.Contains(card, "private-") || strings.Contains(card, "recommended") || !strings.Contains(card, `"selected":[]`) {
		t.Fatal("card leaks private fields or preselects")
	}
	p.Options[1].ID = "o1"
	if p.Validate() == nil {
		t.Fatal("duplicate choice IDs accepted")
	}
}

func TestLegacyContextWithoutActionName(t *testing.T) {
	raw := strings.Replace(string(fixtureEvent(true)), `,"name":"runtime.clarification.submit"`, "", 1)
	e, err := ParseEvent([]byte(raw))
	if err != nil || e.Protocol != "actionData.context" {
		t.Fatalf("legacy context rejected: %v", err)
	}
	raw = strings.Replace(string(fixtureEvent(false)), `,"name":"runtime.clarification.submit"`, "", 1)
	if _, err := ParseEvent([]byte(raw)); err == nil {
		t.Fatal("modern protocol missing action name accepted")
	}
}

func TestExportDropsCredentialAndUnrelatedContactFields(t *testing.T) {
	in := map[string]any{"question": "选择哪个任务？", "credentials": map[string]any{"secret": "never"}, "nested": []any{map[string]any{"operatorUserAgent": "device", "mobile": "123", "option_id": "new", "access_token": "never"}}}
	raw, _ := json.Marshal(ExportValue(in))
	if strings.Contains(string(raw), "never") || strings.Contains(string(raw), "device") || strings.Contains(string(raw), "123") || !strings.Contains(string(raw), "option_id") {
		t.Fatal(string(raw))
	}
	if _, ok := in["credentials"]; !ok {
		t.Fatal("export mutated authoritative data")
	}
}
