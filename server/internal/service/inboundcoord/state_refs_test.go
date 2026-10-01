package inboundcoord

import (
	"encoding/json"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

// A read reference copied onto work is dropped, not a rejected plan: the
// same proposal resubmitted by a weaker model used to end the turn. On a
// non-work action it still fails (a likely mislabelled report_status), and
// a work proposal differing only by it is the same plan to the retry budget.
func TestStrayStateRefsAreDroppedFromWorkActions(t *testing.T) {
	d, err := parseValidatedWindowPlan(`{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"print the account's dws auth status","state_refs":["r1","r1"]}]}`,
		Turn{Source: SourceWeb, Message: "do work"}, nil, nil)
	if err != nil {
		t.Fatalf("start_work with a stray state_refs was rejected: %v", err)
	}
	if d.Action != ActionIssue {
		t.Fatalf("decision = %+v", d)
	}
	if _, err := parseValidatedWindowPlan(`{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好","state_refs":["r1"]}]}`,
		Turn{Source: SourceWeb, Message: "hi"}, nil, nil); err == nil {
		t.Fatal("a non-work action may not carry state_refs")
	}
	with := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"print the account's dws auth status","state_refs":["r1"]}]}`
	without := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"print the account's dws auth status"}]}`
	if proposalShape(with) != proposalShape(without) {
		t.Fatal("a stray state_refs made a work proposal look new")
	}
	status := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r1"],"reply":"x"}]}`
	other := `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"x"}]}`
	if proposalShape(status) == proposalShape(other) {
		t.Fatal("report_status lost its state_refs in the proposal shape")
	}
}

// Every model sees state_refs on report_status only, whatever the finish
// schema group.
func TestFinishSchemaOffersStateRefsOnlyToReportStatus(t *testing.T) {
	p, err := coordinatorWireParams(openai.ChatCompletionNewParams{Tools: []openai.ChatCompletionToolUnionParam{windowPlanTool(false)}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p.Tools[0].OfFunction.Function.Parameters)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	branches := schema["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)
	sawStatus := false
	for _, entry := range branches {
		props := entry.(map[string]any)["properties"].(map[string]any)
		kind := props["kind"].(map[string]any)["enum"].([]any)[0].(string)
		_, has := props["state_refs"]
		if kind == "report_status" {
			sawStatus = true
			if !has {
				t.Fatal("report_status lost state_refs")
			}
		} else if has {
			t.Fatalf("%s is offered state_refs", kind)
		}
	}
	if !sawStatus {
		t.Fatal("the expanded schema has no report_status branch")
	}
}
