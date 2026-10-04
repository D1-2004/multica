package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEmployeeCompactClickUsesFrozenChoice(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService()
	req := baseRequest(KindConfirm)
	req.Header = "Long summary that must not repeat the question"
	req.Question = "写哪类文档？"
	req.EmployeeCompact = true
	req.OperatorUID = "10001"
	row, messages, err := svc.Stage(ctx, uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := svc.Get(ctx, row.PublicID)
	if err != nil || !stored.spec.EmployeeCompact {
		t.Fatalf("compact intent not persisted: %+v %v", stored.spec, err)
	}
	encoded, _ := json.Marshal(stored.spec)
	var restored storedRequest
	if err := json.Unmarshal(encoded, &restored); err != nil || !restored.EmployeeCompact {
		t.Fatalf("compact intent not restored: %+v %v", restored, err)
	}
	created, components := decodeCard(t, messages)
	for _, component := range components {
		item := component.(map[string]any)
		if item["component"] == "ChoicePicker" || item["component"] == "TextField" || item["id"] == "submit" || item["id"] == "title" || strings.HasPrefix(item["id"].(string), "hint-") {
			t.Fatalf("unnecessary compact control/copy: %#v", item)
		}
	}
	if component(t, components, "question")["text"] != req.Question {
		t.Fatal("compact question lost")
	}
	if _, exists := created.DataModel["clarification"].(map[string]any)["answers"]; exists {
		t.Fatal("single choice exposed an editable answer model")
	}
	for i, option := range stored.spec.Options {
		action := component(t, components, "choice-"+option.ID)["action"].(map[string]any)["event"].(map[string]any)
		fields := action["context"].(map[string]any)
		answer := fields["answers"].(map[string]any)["q0"].(map[string]any)
		selected := answer["selected"].([]any)
		if action["name"] != submitEvent || len(selected) != 1 || selected[0] != option.ID || answer["custom"] != "" || fields["outcome"] != "answered" || fields["sourceTurnId"] != row.PublicID || fields["sourceProjectionVersion"] != Version {
			t.Fatalf("choice %d has no frozen authority: %#v", i, action)
		}
		line := directChoiceLine(t, row.PublicID, "direct-"+option.ID, fields)
		got, err := svc.InspectNativeAnswer(ctx, testActor(), line)
		if err != nil || len(got.Selected) != 1 || got.Selected[0] != option.ID || got.Custom != "" {
			t.Fatalf("projected click does not round trip: %+v %v", got, err)
		}
	}
	// An old submit from a card already open before deployment still works.
	legacy := cardLine(t, row.PublicID, "old-submit", "answered", []string{"o1"}, 10001, row.ConversationID, "")
	if _, err := svc.InspectNativeAnswer(ctx, testActor(), legacy); err != nil {
		t.Fatalf("old submit rejected: %v", err)
	}
	for _, option := range stored.spec.Options {
		fields := directChoiceAction(row.PublicID, option.ID)["event"].(map[string]any)["context"].(map[string]any)
		if err := svc.Accept(ctx, testActor(), directChoiceLine(t, row.PublicID, "accepted-"+option.ID, fields), nil); err != nil {
			t.Fatal(err)
		}
	}
	closed, err := svc.Get(ctx, row.PublicID)
	if err != nil || closed.Status != StatusAnswered || len(closed.Result.Selected) != 1 || closed.Result.Selected[0] != "o0" {
		t.Fatalf("later click replaced first direct answer: %+v %v", closed, err)
	}
}

func TestEmployeeDirectChoiceRejectsEditedAuthority(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService()
	req := baseRequest(KindConfirm)
	req.EmployeeCompact = true
	req.OperatorUID = "10001"
	row, _, err := svc.Stage(ctx, uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{"outcome": "answered", "sourceTurnId": row.PublicID, "sourceProjectionVersion": Version, "answers": map[string]any{"q0": map[string]any{"selected": []string{"o0"}, "custom": ""}}}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"unknown option", func(v map[string]any) {
			v["answers"] = map[string]any{"q0": map[string]any{"selected": []string{"someone-open-id"}}}
		}},
		{"empty option", func(v map[string]any) { v["answers"] = map[string]any{"q0": map[string]any{"selected": []string{}}} }},
		{"wrong question", func(v map[string]any) {
			v["answers"] = map[string]any{"q1": map[string]any{"selected": []string{"o1"}}}
		}},
		{"wrong outcome", func(v map[string]any) { v["outcome"] = "approved" }},
		{"duplicate option", func(v map[string]any) {
			v["answers"] = map[string]any{"q0": map[string]any{"selected": []string{"o0", "o0"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{}
			for key, value := range valid {
				fields[key] = value
			}
			tc.edit(fields)
			if _, err := svc.InspectNativeAnswer(ctx, testActor(), directChoiceLine(t, row.PublicID, tc.name, fields)); err == nil {
				t.Fatal("edited choice accepted")
			}
		})
	}
	line := directChoiceLine(t, row.PublicID, "wrong-actor", valid)
	actor := testActor()
	actor.UID = "someone-else"
	if _, err := svc.InspectNativeAnswer(ctx, actor, line); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other identity accepted: %v", err)
	}
	legacyReq := baseRequest(KindConfirm)
	legacy, _, err := svc.Stage(ctx, uuid.New(), legacyReq)
	if err != nil {
		t.Fatal(err)
	}
	valid["sourceTurnId"] = legacy.PublicID
	if _, err := svc.InspectNativeAnswer(ctx, testActor(), directChoiceLine(t, legacy.PublicID, "old-direct", valid)); err != nil {
		t.Fatalf("static answer broke existing submit readers: %v", err)
	}
}

func TestEmployeeCompactMultipleKeepsOneConfirmation(t *testing.T) {
	svc, _ := testService()
	req := baseRequest(KindChoose)
	req.EmployeeCompact = true
	_, messages, err := svc.Stage(context.Background(), uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	_, components := decodeCard(t, messages)
	confirmations := 0
	for _, component := range components {
		item := component.(map[string]any)
		if item["component"] == "Button" {
			confirmations++
			if item["id"] != "submit" {
				t.Fatalf("unexpected multi action: %#v", item)
			}
		}
		if item["component"] == "TextField" {
			t.Fatal("multi choice still exposes large extra input")
		}
	}
	if confirmations != 1 || component(t, components, "choices")["variant"] != "multipleSelection" {
		t.Fatalf("multi choice controls = %d", confirmations)
	}
	if component(t, components, "submitLabel")["text"] != "确认" {
		t.Fatal("multi finish is unclear")
	}
	// Generic person and approval cards remain explicit existing interactions.
	for _, kind := range []Kind{KindPerson, KindApproval} {
		req = baseRequest(kind)
		req.EmployeeCompact = true
		_, messages, err = svc.Stage(context.Background(), uuid.New(), req)
		if err != nil {
			t.Fatal(err)
		}
		_, components = decodeCard(t, messages)
		if component(t, components, "submit")["component"] != "Button" {
			t.Fatalf("generic %s lost explicit confirmation", kind)
		}
		if kind == KindPerson && component(t, components, "person")["component"] != "UserPicker" {
			t.Fatal("native user picker changed")
		}
	}
}

func TestEmployeeCompactNamesRemainDistinguishable(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService()
	req := baseRequest(KindConfirm)
	req.EmployeeCompact = true
	req.Question = "哪位李明？"
	req.SourceQuote = "请交给李明处理"
	req.Options = []Option{
		{Label: "李明", Description: "人资 · 入职手续"},
		{Label: "李明", Description: "研发 · 新人导师"},
	}
	row, messages, err := svc.Stage(ctx, uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	_, components := decodeCard(t, messages)
	if component(t, components, "source-quote")["text"] != "“请交给李明处理”" {
		t.Fatal("source quote lost")
	}
	rootChildren := component(t, components, "root")["children"].([]any)
	for _, description := range []string{"description-o0", "description-o1"} {
		for _, child := range rootChildren {
			if child == description {
				t.Fatal("candidate description repeated outside the choice")
			}
		}
	}
	for i, option := range req.Options {
		id := row.spec.Options[i].ID
		button := component(t, components, "choice-"+id)
		container := component(t, components, button["child"].(string))
		if container["component"] != "Column" {
			t.Fatal("candidate description is not inside the choice")
		}
		children := container["children"].([]any)
		if len(children) != 2 || children[1] != "description-"+id || component(t, components, "description-"+id)["text"] != option.Description {
			t.Fatalf("candidate %s cannot be distinguished: %#v", id, container)
		}
	}
	closed, err := svc.ResolvedProjection(ctx, row.PublicID, Result{Outcome: "answered", Selected: []string{"o1"}, Labels: []string{"李明 · forged role"}})
	if err != nil {
		t.Fatal(err)
	}
	resolved := resolvedComponents(t, closed)
	if component(t, resolved, "resolved-0-label")["text"] != "李明 · 研发 · 新人导师" || strings.Contains(strings.Join(closed, ""), "人资") || strings.Contains(strings.Join(closed, ""), "forged role") {
		t.Fatalf("resolved candidate identity changed: %#v", resolved)
	}
	req.Kind = KindChoose
	_, messages, err = svc.Stage(ctx, uuid.New(), req)
	if err != nil {
		t.Fatal(err)
	}
	_, components = decodeCard(t, messages)
	options := component(t, components, "choices")["options"].([]any)
	if options[0].(map[string]any)["label"] == options[1].(map[string]any)["label"] {
		t.Fatal("multiple choice loses candidate descriptions")
	}
}

func TestResolvedProjectionUsesOnlyFrozenSelectedLabels(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService()
	for _, kind := range []Kind{KindConfirm, KindChoose} {
		req := baseRequest(kind)
		req.Question = "保留哪些内容？"
		row, _, err := svc.Stage(ctx, uuid.New(), req)
		if err != nil {
			t.Fatal(err)
		}
		result := Result{Outcome: "answered", Selected: []string{"o1"}, Labels: []string{"untrusted label"}, Custom: strings.Repeat("user words ", 500)}
		if kind == KindChoose {
			result.Selected = []string{"o1", "o0"}
		}
		messages, err := svc.ResolvedProjection(ctx, row.PublicID, result)
		if err != nil || len(messages) != 1 {
			t.Fatalf("resolved projection: %v %#v", err, messages)
		}
		components := resolvedComponents(t, messages)
		payload := strings.Join(messages, "")
		if strings.Contains(payload, "untrusted label") || strings.Contains(payload, "user words") || strings.Contains(payload, "createSurface") {
			t.Fatalf("resolved display trusts or repeats input: %s", payload)
		}
		if !strings.Contains(payload, "先放着") || (kind == KindConfirm && strings.Contains(payload, "就按这个发")) {
			t.Fatalf("resolved display not limited to selected choices: %s", payload)
		}
		for _, component := range components {
			item := component.(map[string]any)
			if _, found := item["action"]; found || item["component"] == "Button" || item["component"] == "ChoicePicker" || item["component"] == "TextField" {
				t.Fatalf("resolved display remains operable: %#v", item)
			}
		}
		if component(t, components, "resolved-0")["justify"] != "spaceBetween" || component(t, components, "resolved-0-check")["text"] != "✓" {
			t.Fatal("selected check is not right aligned")
		}
		for _, invalid := range []Result{
			{Outcome: "answered", Selected: []string{"not-an-option"}},
			{Outcome: "answered", Selected: []string{"o1", "o1"}},
			{Outcome: "answered"},
			{Outcome: "approved", Selected: []string{"o0"}},
		} {
			if _, err := svc.ResolvedProjection(ctx, row.PublicID, invalid); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid resolution accepted: %+v %v", invalid, err)
			}
		}
		text, err := svc.ResolvedProjection(ctx, row.PublicID, Result{Outcome: "answered", Custom: strings.Repeat("a", 9000)})
		if err != nil || len(component(t, resolvedComponents(t, text), "resolved-custom")["text"].(string)) != 160 {
			t.Fatalf("typed answer not bounded: %v", err)
		}
		skip, err := svc.ResolvedProjection(ctx, row.PublicID, Result{Outcome: "skipped"})
		if err != nil || component(t, resolvedComponents(t, skip), "resolved-skipped")["text"] != "先不选" {
			t.Fatalf("skip reopened choices: %v", err)
		}
	}
}

func directChoiceLine(t *testing.T, publicID, eventID string, fields map[string]any) []byte {
	t.Helper()
	line := cardLine(t, publicID, eventID, "answered", nil, 10001, "cid-1", "")
	var outer map[string]any
	if err := json.Unmarshal(line, &outer); err != nil {
		t.Fatal(err)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(outer["data"].(string)), &inner); err != nil {
		t.Fatal(err)
	}
	inner["payload"].(map[string]any)["body"].(map[string]any)["actionData"] = map[string]any{"name": submitEvent, "context": fields}
	encoded, _ := json.Marshal(inner)
	outer["data"] = string(encoded)
	encoded, _ = json.Marshal(outer)
	return encoded
}

func resolvedComponents(t *testing.T, messages []string) []any {
	t.Helper()
	if len(messages) != 1 {
		t.Fatalf("resolved messages = %d", len(messages))
	}
	var envelope struct {
		UpdateComponents struct {
			Components []any `json:"components"`
		} `json:"updateComponents"`
	}
	if err := json.Unmarshal([]byte(messages[0]), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.UpdateComponents.Components
}
